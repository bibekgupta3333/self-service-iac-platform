// RequestPoller is the fallback when the status stream cannot be used: it reads the request
// on an interval, stops at a terminal status or on an answer that retrying cannot fix
// (401, 403, 404), and pauses while the tab is hidden so a background tab costs nothing.
//
// I/O is injected (`read`, `every`, `visibility`), so tests drive it without timers or a DOM.

import { isApiError } from "@/lib/api/errors";

import { type StreamNotice, stopNotice } from "./stream";

/** Visibility is the part of the page visibility API the poller needs. */
export interface Visibility {
  hidden(): boolean;
  /** Calls cb on every visibility change; returns the unsubscribe function. */
  onChange(cb: () => void): () => void;
}

/** documentVisibility adapts `document` (absent on the server). */
export function documentVisibility(): Visibility | undefined {
  if (typeof document === "undefined") return undefined;
  return {
    hidden: () => document.visibilityState === "hidden",
    onChange: (cb) => {
      document.addEventListener("visibilitychange", cb);
      return () => document.removeEventListener("visibilitychange", cb);
    },
  };
}

/** Every runs fn every ms and returns the function that stops it. */
export type Every = (fn: () => void, ms: number) => () => void;

const intervalEvery: Every = (fn, ms) => {
  const t = setInterval(fn, ms);
  return () => clearInterval(t);
};

const POLL_STOPS = new Set([401, 403, 404]);

/** pollStopNotice maps a failed read to the notice that ends polling, or undefined to try again next tick. Pure. */
export function pollStopNotice(err: unknown): StreamNotice | undefined {
  return isApiError(err) && POLL_STOPS.has(err.status) ? stopNotice(err) : undefined;
}

export interface RequestPollerOptions {
  read: () => Promise<{ status: string; updated_at: string }>;
  onStatus: (status: string, at: string) => void;
  /** Polling stopped for good on an error; the notice says why. */
  onStop: (notice: StreamNotice) => void;
  /** Whether a status is final (polling stops after it). */
  isFinal: (status: string) => boolean;
  intervalMs: number;
  visibility?: Visibility;
  every?: Every;
}

export class RequestPoller {
  private readonly every: Every;
  private cancelTimer: (() => void) | undefined;
  private unsubscribe: (() => void) | undefined;
  private inFlight = false;
  private running = false;

  constructor(private readonly o: RequestPollerOptions) {
    this.every = o.every ?? intervalEvery;
  }

  /** start reads at once (unless the tab is hidden) and then on every interval. Idempotent. */
  start(): void {
    if (this.running) return;
    this.running = true;
    this.unsubscribe = this.o.visibility?.onChange(() => this.onVisibility());
    if (!this.o.visibility?.hidden()) this.resume();
  }

  /** stop ends polling for good. Safe to call repeatedly. */
  stop(): void {
    this.running = false;
    this.pause();
    this.unsubscribe?.();
    this.unsubscribe = undefined;
  }

  private onVisibility(): void {
    if (!this.running) return;
    if (this.o.visibility?.hidden()) this.pause();
    else this.resume(); // back in view: read now rather than wait out the interval
  }

  private resume(): void {
    if (this.cancelTimer) return;
    void this.tick();
    this.cancelTimer = this.every(() => void this.tick(), this.o.intervalMs);
  }

  private pause(): void {
    this.cancelTimer?.();
    this.cancelTimer = undefined;
  }

  private async tick(): Promise<void> {
    if (!this.running || this.inFlight) return;
    this.inFlight = true;
    try {
      const r = await this.o.read();
      if (!this.running) return;
      this.o.onStatus(r.status, r.updated_at);
      if (this.o.isFinal(r.status)) this.stop();
    } catch (err) {
      if (!this.running) return;
      const notice = pollStopNotice(err);
      if (notice) {
        this.stop();
        this.o.onStop(notice);
      }
      // Anything else is retried on the next tick; the page keeps its last known status.
    } finally {
      this.inFlight = false;
    }
  }
}
