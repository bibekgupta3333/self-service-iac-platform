// StatusStream reads a request's Server-Sent Events with fetch and survives what real
// networks do: it resumes with Last-Event-ID, backs off with jitter, stops on `end` and on
// answers that retrying cannot fix (401, 403, 404, 429), cuts a connection that goes silent
// (the backend heartbeats every 15 seconds), and reports "unavailable" when SSE does not work
// at all so the caller can fall back to polling.
//
// The backend ends every stream after its lifetime cap (5 minutes) and the client resumes, so
// a healthy idle stream closes regularly. A connection counts as a failure when it closed
// within 10 seconds without an event (heartbeats do not count), or went silent with nothing.
//
// All I/O is injected (`open`, `sleep`, `random`, `now`, `schedule`), so tests drive it
// without a network or real timers.

import { isApiError } from "@/lib/api/errors";

import { SseParser } from "./sse";
import { decodeFrame, type StreamEvent } from "./status";
import { IdleWatchdog, type Schedule, timerSchedule } from "./watchdog";

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
  /** A connection open at least this long is not a failure even if it delivered no event. */
  minHealthyMs?: number;
  /** Cut and reconnect when nothing (frame or heartbeat) arrives for this long; 0 disables. */
  idleTimeoutMs?: number;
  /** Successive connection attempts start at least this far apart. */
  minReconnectIntervalMs?: number;
  /** Injected for tests; resolves early when the signal aborts. */
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  random?: () => number;
  now?: () => number;
  schedule?: Schedule;
}

/** The backend heartbeats every 15 s; three missed heartbeats mean the connection is dead. */
export const IDLE_TIMEOUT_MS = 45_000;
export const MIN_HEALTHY_MS = 10_000;
export const MIN_RECONNECT_INTERVAL_MS = 3_000;

/**
 * backoffDelay is exponential backoff with "equal jitter": half the capped delay plus a random
 * share of the other half, so reconnecting clients spread out and never hammer at zero delay.
 * `failures` counts consecutive failed attempts (1 for the first retry). Pure.
 */
export function backoffDelay(failures: number, baseMs: number, maxMs: number, random: number): number {
  const capped = Math.min(maxMs, baseMs * 2 ** Math.max(0, failures - 1));
  return Math.round(capped / 2 + (capped / 2) * Math.min(1, Math.max(0, random)));
}

/** What one connection did, from the moment it was opened until it closed. */
export interface ConnectionOutcome {
  /** Any bytes arrived (a heartbeat is enough). */
  received: boolean;
  /** At least one status event was decoded and passed on. */
  delivered: boolean;
  /** The idle watchdog cut it. */
  idle: boolean;
  durationMs: number;
}

/**
 * connectionFailed decides whether a connection that closed without `end` counts towards
 * giving up. A connection that delivered an event, or stayed open for minHealthyMs (heartbeats
 * only, until the server's lifetime cap), proves the stream works. One that closed sooner
 * without an event is a failure even if a heartbeat arrived, so a proxy that sends a heartbeat
 * and then cuts every connection ends in the polling fallback. A silent connection cut by the
 * watchdog is a failure however long it lasted (a buffering proxy, a dead socket). Pure.
 */
export function connectionFailed(o: ConnectionOutcome, minHealthyMs: number): boolean {
  if (o.idle && !o.received) return true;
  return !o.delivered && o.durationMs < minHealthyMs;
}

/** reconnectDelay is how long to wait so the next attempt starts minIntervalMs after the last one did. Pure. */
export function reconnectDelay(startedAt: number, now: number, minIntervalMs: number): number {
  return Math.max(0, minIntervalMs - Math.max(0, now - startedAt));
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

type Settings = Required<Omit<StatusStreamOptions, "initialLastEventId">>;

/** One connection attempt: its own abort signal (tied to the stream's) and its idle watchdog. */
class Attempt {
  readonly controller = new AbortController();
  readonly watchdog: IdleWatchdog;
  readonly startedAt: number;
  idle = false;
  private readonly unlink: () => void;

  constructor(parent: AbortSignal, s: Settings) {
    this.startedAt = s.now();
    this.watchdog = new IdleWatchdog(
      s.idleTimeoutMs,
      () => {
        this.idle = true;
        this.controller.abort();
      },
      s.schedule,
    );
    const abort = () => this.controller.abort();
    parent.addEventListener("abort", abort, { once: true });
    this.unlink = () => parent.removeEventListener("abort", abort);
    this.watchdog.start();
  }

  get signal(): AbortSignal {
    return this.controller.signal;
  }

  dispose(): void {
    this.watchdog.stop();
    this.unlink();
  }
}

export class StatusStream {
  private readonly o: Settings;
  private readonly controller = new AbortController();
  private lastEventId: string | undefined;
  private started = false;
  private closed = false;

  constructor(opts: StatusStreamOptions) {
    this.o = {
      baseDelayMs: 1000,
      maxDelayMs: 30_000,
      maxFailures: 5,
      minHealthyMs: MIN_HEALTHY_MS,
      idleTimeoutMs: IDLE_TIMEOUT_MS,
      minReconnectIntervalMs: MIN_RECONNECT_INTERVAL_MS,
      sleep: defaultSleep,
      random: Math.random,
      now: Date.now,
      schedule: timerSchedule,
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

      const attempt = new Attempt(this.controller.signal, this.o);
      let response: Response;
      try {
        response = await this.o.open(this.lastEventId, attempt.signal);
      } catch (err) {
        attempt.dispose();
        if (this.closed) return;
        const stop = stopNotice(err);
        if (stop) return this.finish("stopped", stop);
        failures += 1;
        if (failures >= this.o.maxFailures) return this.finish("unavailable", { message: "The live connection keeps failing." });
        await this.pause(failures);
        continue;
      }

      if (!response.body || !(response.headers.get("content-type") ?? "").includes("text/event-stream")) {
        // A 200 that is not an event stream (a proxy page, a buffering gateway): SSE is not
        // usable. Cancel the body so the connection is released rather than left to drain.
        attempt.dispose();
        void response.body?.cancel().catch(() => undefined);
        return this.finish("unavailable", { message: "The server did not answer with an event stream." });
      }

      this.o.onState("open");
      const { ended, received, delivered } = await this.read(response.body, attempt);
      attempt.dispose();
      if (this.closed) return;
      if (ended) return this.finish("ended");
      // The stream closed without `end`: the server's lifetime cap, a dropped connection, or
      // the watchdog. Resume; give up only after repeated connections that failed (connectionFailed).
      const outcome: ConnectionOutcome = {
        received,
        delivered,
        idle: attempt.idle,
        durationMs: this.o.now() - attempt.startedAt,
      };
      if (connectionFailed(outcome, this.o.minHealthyMs)) {
        failures += 1;
        if (failures >= this.o.maxFailures) return this.finish("unavailable", { message: "The live connection keeps closing." });
        await this.pause(failures);
      } else {
        failures = 0;
        // A stream that delivers one event per connection must not reconnect in a tight loop.
        const wait = reconnectDelay(attempt.startedAt, this.o.now(), this.o.minReconnectIntervalMs);
        if (wait > 0) await this.o.sleep(wait, this.controller.signal);
      }
    }
  }

  /** read consumes the body until it ends, errors, or the attempt is aborted (close or watchdog). */
  private async read(
    body: ReadableStream<Uint8Array>,
    attempt: Attempt,
  ): Promise<{ ended: boolean; received: boolean; delivered: boolean }> {
    const reader = body.getReader();
    const decoder = new TextDecoder();
    const parser = new SseParser();
    let received = false;
    let delivered = false;
    // An abort must release a read that is waiting for data, whether or not the transport
    // reacts to the abort signal itself.
    const release = () => void reader.cancel().catch(() => undefined);
    attempt.signal.addEventListener("abort", release, { once: true });
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        // Any bytes prove the connection is alive, also a large frame still trickling in.
        if (value.byteLength > 0) {
          received = true;
          attempt.watchdog.kick();
        }
        const parsed = parser.push(decoder.decode(value, { stream: true }));
        for (const frame of parsed.frames) {
          if (frame.id !== undefined) this.lastEventId = frame.id;
          const event = decodeFrame(frame);
          if (!event) continue;
          delivered = true;
          this.o.onEvent(event);
          if (event.type === "end") return { ended: true, received, delivered };
        }
      }
    } catch {
      // A read error is a dropped connection (or an abort); the loop decides whether to retry.
    } finally {
      attempt.signal.removeEventListener("abort", release);
      reader.cancel().catch(() => undefined);
    }
    return { ended: false, received, delivered };
  }

  private pause(failures: number): Promise<void> {
    return this.o.sleep(backoffDelay(failures, this.o.baseDelayMs, this.o.maxDelayMs, this.o.random()), this.controller.signal);
  }

  private finish(state: StreamState, notice?: StreamNotice): void {
    this.closed = true;
    this.o.onState(state, notice);
  }
}
