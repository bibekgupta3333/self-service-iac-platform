// StatusStream reads a request's Server-Sent Events with fetch and survives what real
// networks do: it resumes with Last-Event-ID, backs off with jitter, stops on `end` and on
// answers that retrying cannot fix (401, 403, 404, 429), and reports "unavailable" when SSE
// does not work at all so the caller can fall back to polling.
//
// All I/O is injected (`open`, `sleep`, `random`), so tests drive it without a network.

import { isApiError } from "@/lib/api/errors";

import { parseSse } from "./sse";
import { decodeFrame, type StreamEvent } from "./status";

export type StreamState =
  | "connecting"
  | "open"
  | "reconnecting"
  /** The server said the request is terminal; nothing more will come. */
  | "ended"
  /** Retrying cannot help: see the notice. */
  | "stopped"
  /** SSE is not usable here; poll instead. */
  | "unavailable"
  /** close() was called. */
  | "closed";

export type StopCode = "too_many_streams" | "unauthorized" | "forbidden" | "not_found";

export interface StreamNotice {
  code?: StopCode;
  message: string;
}

export interface StatusStreamOptions {
  /** Opens the stream; rejects with ApiError for a non-2xx answer (ApiClient.openRequestEvents). */
  open: (lastEventId: string | undefined, signal: AbortSignal) => Promise<Response>;
  onEvent: (event: StreamEvent) => void;
  onState: (state: StreamState, notice?: StreamNotice) => void;
  /** Where to resume the first connection ("0" replays the full history). Default: none. */
  initialLastEventId?: string;
  baseDelayMs?: number;
  maxDelayMs?: number;
  /** Consecutive failed attempts before giving up on SSE. */
  maxFailures?: number;
  /** Injected for tests; resolves early when the signal aborts. */
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  random?: () => number;
}

/**
 * backoffDelay is exponential backoff with "equal jitter": half the capped delay plus a random
 * share of the other half, so reconnecting clients spread out and never hammer at zero delay.
 * `failures` counts consecutive failed attempts (1 for the first retry). Pure.
 */
export function backoffDelay(failures: number, baseMs: number, maxMs: number, random: number): number {
  const capped = Math.min(maxMs, baseMs * 2 ** Math.max(0, failures - 1));
  return Math.round(capped / 2 + (capped / 2) * Math.min(1, Math.max(0, random)));
}

const STOPS: Record<number, StreamNotice> = {
  401: { code: "unauthorized", message: "Your session has expired. Sign in again to follow this request live." },
  403: { code: "forbidden", message: "You are not allowed to follow this request live." },
  404: { code: "not_found", message: "This request was not found, or you cannot see it." },
  429: {
    code: "too_many_streams",
    message: "You have too many live request pages open. Close one to follow this request live.",
  },
};

/** stopNotice maps a failed open to the notice that ends retrying, or undefined to retry. Pure. */
export function stopNotice(err: unknown): StreamNotice | undefined {
  return isApiError(err) ? STOPS[err.status] : undefined;
}

const defaultSleep = (ms: number, signal: AbortSignal): Promise<void> =>
  new Promise((resolve) => {
    if (signal.aborted) return resolve();
    const done = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done, { once: true });
  });

export class StatusStream {
  private readonly o: Required<Omit<StatusStreamOptions, "initialLastEventId">> & { initialLastEventId?: string };
  private readonly controller = new AbortController();
  private lastEventId: string | undefined;
  private started = false;
  private closed = false;

  constructor(opts: StatusStreamOptions) {
    this.o = {
      baseDelayMs: 1000,
      maxDelayMs: 30_000,
      maxFailures: 5,
      sleep: defaultSleep,
      random: Math.random,
      ...opts,
    };
    this.lastEventId = opts.initialLastEventId;
  }

  /** The id of the last frame received (the Last-Event-ID the next connection sends). */
  get resumeId(): string | undefined {
    return this.lastEventId;
  }

  /** start runs the connection loop; the promise settles when the stream is over. Call once. */
  start(): Promise<void> {
    if (this.started) return Promise.resolve();
    this.started = true;
    return this.run();
  }

  /** close ends the stream and cancels any pending read or wait. Safe to call repeatedly. */
  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.controller.abort();
    this.o.onState("closed");
  }

  private async run(): Promise<void> {
    let failures = 0;
    let first = true;
    while (!this.closed) {
      this.o.onState(first ? "connecting" : "reconnecting");
      first = false;

      let response: Response;
      try {
        response = await this.o.open(this.lastEventId, this.controller.signal);
      } catch (err) {
        if (this.closed) return;
        const stop = stopNotice(err);
        if (stop) return this.finish("stopped", stop);
        failures += 1;
        if (failures >= this.o.maxFailures) return this.finish("unavailable", { message: "The live connection keeps failing." });
        await this.pause(failures);
        continue;
      }

      if (!response.body || !(response.headers.get("content-type") ?? "").includes("text/event-stream")) {
        // A 200 that is not an event stream (a proxy page, a buffering gateway): SSE is not usable.
        return this.finish("unavailable", { message: "The server did not answer with an event stream." });
      }

      this.o.onState("open");
      const outcome = await this.read(response.body);
      if (this.closed) return;
      if (outcome === "ended") return this.finish("ended");
      // The stream closed without `end` (the server's lifetime cap, a dropped connection).
      // Resume at once if it delivered something; a stream that closes empty counts as a failure.
      failures = outcome === "delivered" ? 0 : failures + 1;
      if (failures >= this.o.maxFailures) return this.finish("unavailable", { message: "The live connection keeps closing." });
      await this.pause(Math.max(1, failures));
    }
  }

  /** read consumes the body until it ends or errors. */
  private async read(body: ReadableStream<Uint8Array>): Promise<"ended" | "delivered" | "empty"> {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let delivered = false;
    // close() must release a read that is waiting for data, whether or not the transport
    // reacts to the abort signal itself.
    const release = () => void reader.cancel().catch(() => undefined);
    this.controller.signal.addEventListener("abort", release, { once: true });
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        const parsed = parseSse(buffer + decoder.decode(value, { stream: true }));
        buffer = parsed.rest;
        for (const frame of parsed.frames) {
          if (frame.id !== undefined) this.lastEventId = frame.id;
          const event = decodeFrame(frame);
          if (!event) continue;
          delivered = true;
          this.o.onEvent(event);
          if (event.type === "end") return "ended";
        }
      }
    } catch {
      // A read error is a dropped connection (or close()); the loop decides whether to retry.
    } finally {
      this.controller.signal.removeEventListener("abort", release);
      reader.cancel().catch(() => undefined);
    }
    return delivered ? "delivered" : "empty";
  }

  private pause(failures: number): Promise<void> {
    return this.o.sleep(backoffDelay(failures, this.o.baseDelayMs, this.o.maxDelayMs, this.o.random()), this.controller.signal);
  }

  private finish(state: StreamState, notice?: StreamNotice): void {
    this.closed = true;
    this.o.onState(state, notice);
  }
}
