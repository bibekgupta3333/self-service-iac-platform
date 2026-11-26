import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";

import type { StreamEvent } from "./status";
import {
  backoffDelay,
  connectionFailed,
  reconnectDelay,
  StatusStream,
  type StreamNotice,
  type StreamState,
  stopNotice,
} from "./stream";

const SSE = { "content-type": "text/event-stream; charset=utf-8" };

/** sseResponse streams the given chunks, then closes (or errors when `fail` is set). */
function sseResponse(
  chunks: string[],
  opts: { fail?: boolean; hold?: boolean; headers?: Record<string, string>; onCancel?: () => void } = {},
): Response {
  const enc = new TextEncoder();
  let i = 0;
  const body = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (i < chunks.length) controller.enqueue(enc.encode(chunks[i++]));
      else if (opts.hold) return;
      else if (opts.fail) controller.error(new Error("connection reset"));
      else controller.close();
    },
    cancel() {
      opts.onCancel?.();
    },
  });
  return new Response(body, { status: 200, headers: opts.headers ?? SSE });
}

const status = (seq: number, to: string, extra = "") =>
  `id: ${seq}\nevent: status\ndata: {"seq":${seq},"occurred_at":"t${seq}","status":"${to}"${extra}}\n\n`;
const end = (to: string) => `event: end\ndata: {"status":"${to}"}\n\n`;

const problem = (status: number, code: string) =>
  new ApiError(parseProblem(status, { title: code, status, code }));

/** A fake clock and timer queue: tests move time and fire the idle watchdog by hand. */
function fakeTime() {
  let now = 0;
  const timers = new Set<() => void>();
  return {
    now: () => now,
    advance: (ms: number) => void (now += ms),
    schedule: (fn: () => void) => {
      timers.add(fn);
      return () => void timers.delete(fn);
    },
    /** fireAll runs every armed timer, as if its time had come, and says how many there were. */
    fireAll: () => {
      const due = [...timers];
      timers.clear();
      due.forEach((fn) => fn());
      return due.length;
    },
    armed: () => timers.size,
  };
}

function harness(open: (id: string | undefined, signal: AbortSignal) => Promise<Response>, extra = {}) {
  const events: StreamEvent[] = [];
  const states: Array<[StreamState, StreamNotice | undefined]> = [];
  const sleeps: number[] = [];
  const time = fakeTime();
  const stream = new StatusStream({
    open,
    onEvent: (e) => events.push(e),
    onState: (s, n) => states.push([s, n]),
    sleep: async (ms) => void sleeps.push(ms),
    random: () => 0.5,
    now: time.now,
    schedule: time.schedule,
    ...extra,
  });
  return { stream, events, states, sleeps, time, last: () => states.at(-1) };
}

describe("backoffDelay", () => {
  it("doubles up to the cap with equal jitter", () => {
    expect(backoffDelay(1, 1000, 30000, 0)).toBe(500);
    expect(backoffDelay(1, 1000, 30000, 1)).toBe(1000);
    expect(backoffDelay(2, 1000, 30000, 1)).toBe(2000);
    expect(backoffDelay(3, 1000, 30000, 0)).toBe(2000);
    expect(backoffDelay(10, 1000, 30000, 1)).toBe(30000);
    expect(backoffDelay(10, 1000, 30000, 0)).toBe(15000);
  });

  it("clamps a bad random value and a zero failure count", () => {
    expect(backoffDelay(0, 1000, 30000, 7)).toBe(1000);
    expect(backoffDelay(1, 1000, 30000, -3)).toBe(500);
  });
});

describe("connectionFailed", () => {
  it.each([
    [{ received: true, idle: false, durationMs: 0 }, false],
    [{ received: true, idle: true, durationMs: 50_000 }, false],
    [{ received: false, idle: false, durationMs: 9_999 }, true],
    [{ received: false, idle: false, durationMs: 10_000 }, false],
    [{ received: false, idle: true, durationMs: 45_000 }, true],
  ])("%j -> %s", (outcome, want) => expect(connectionFailed(outcome, 10_000)).toBe(want));
});

describe("reconnectDelay", () => {
  it("waits out the rest of the minimum interval, never less than zero", () => {
    expect(reconnectDelay(1000, 1000, 3000)).toBe(3000);
    expect(reconnectDelay(1000, 2500, 3000)).toBe(1500);
    expect(reconnectDelay(1000, 9000, 3000)).toBe(0);
    expect(reconnectDelay(5000, 1000, 3000)).toBe(3000); // a clock that went backwards
  });
});

describe("stopNotice", () => {
  it.each([
    [401, "unauthorized"],
    [403, "forbidden"],
    [404, "not_found"],
    [429, "too_many_streams"],
  ])("stops on %i", (status, code) => {
    expect(stopNotice(problem(status, "x"))?.code).toBe(code);
  });

  it("retries on server errors, network errors and other errors", () => {
    expect(stopNotice(problem(502, "api_unreachable"))).toBeUndefined();
    expect(stopNotice(ApiError.network(new Error("down")))).toBeUndefined();
    expect(stopNotice(new Error("boom"))).toBeUndefined();
  });
});

describe("StatusStream", () => {
  it("delivers events in order and stops after end", async () => {
    const open = vi.fn(async () => sseResponse([status(3, "pr_open"), status(4, "validating") + end("applied")]));
    const h = harness(open);
    await h.stream.start();
    expect(h.events.map((e) => e.type)).toEqual(["status", "status", "end"]);
    expect(h.states.map(([s]) => s)).toEqual(["connecting", "open", "ended"]);
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("handles a frame split across chunks", async () => {
    const whole = status(3, "pr_open");
    const h = harness(async () => sseResponse([whole.slice(0, 20), whole.slice(20), end("applied")]));
    await h.stream.start();
    expect(h.events.map((e) => e.type)).toEqual(["status", "end"]);
  });

  it("sends the initial Last-Event-ID, then resumes from the last id after a drop", async () => {
    const seen: Array<string | undefined> = [];
    const responses = [
      sseResponse([status(3, "pr_open"), status(4, "validating")], { fail: true }),
      sseResponse([status(5, "plan_ready"), end("applied")]),
    ];
    const h = harness(async (id) => {
      seen.push(id);
      return responses.shift() as Response;
    }, { initialLastEventId: "0" });
    await h.stream.start();
    expect(seen).toEqual(["0", "4"]);
    expect(h.events.filter((e) => e.type === "status")).toHaveLength(3);
    expect(h.states.map(([s]) => s)).toEqual(["connecting", "open", "reconnecting", "open", "ended"]);
    expect(h.stream.resumeId).toBe("5");
  });

  it("does not reconnect after end, even when the body would continue", async () => {
    const open = vi.fn(async () => sseResponse([end("failed"), status(9, "applied")]));
    const h = harness(open);
    await h.stream.start();
    expect(open).toHaveBeenCalledTimes(1);
    expect(h.events).toHaveLength(1);
  });

  it("reconnects after a clean close without end (the server's lifetime cap)", async () => {
    const responses = [sseResponse([status(2, "pr_open")]), sseResponse([end("applied")])];
    const open = vi.fn(async () => responses.shift() as Response);
    const h = harness(open);
    await h.stream.start();
    expect(open).toHaveBeenCalledTimes(2);
    expect(h.last()?.[0]).toBe("ended");
  });

  it.each([
    [401, "unauthorized"],
    [404, "not_found"],
    [429, "too_many_streams"],
  ])("stops without retrying on %i", async (status, code) => {
    const open = vi.fn(async () => {
      throw problem(status, code);
    });
    const h = harness(open);
    await h.stream.start();
    expect(open).toHaveBeenCalledTimes(1);
    expect(h.last()?.[0]).toBe("stopped");
    expect(h.last()?.[1]?.code).toBe(code);
    expect(h.sleeps).toEqual([]);
  });

  it("backs off between failed attempts and then reports unavailable", async () => {
    const open = vi.fn(async () => {
      throw problem(502, "api_unreachable");
    });
    const h = harness(open, { maxFailures: 4 });
    await h.stream.start();
    expect(open).toHaveBeenCalledTimes(4);
    expect(h.sleeps).toEqual([750, 1500, 3000]); // random 0.5: 3/4 of base * 2^n
    expect(h.last()?.[0]).toBe("unavailable");
  });

  it("recovers from a transient failure and resets the failure count", async () => {
    const responses: Array<Response | Error> = [problem(503, "x"), problem(503, "x"), sseResponse([status(2, "pr_open"), end("applied")])];
    const h = harness(async () => {
      const next = responses.shift() as Response | Error;
      if (next instanceof Error) throw next;
      return next;
    });
    await h.stream.start();
    expect(h.last()?.[0]).toBe("ended");
    expect(h.sleeps).toHaveLength(2);
  });

  it("reports unavailable when the answer is not an event stream, and cancels its body", async () => {
    let cancelled = false;
    const h = harness(async () =>
      sseResponse(["<html>gateway</html>"], { hold: true, headers: { "content-type": "text/html" }, onCancel: () => (cancelled = true) }),
    );
    await h.stream.start();
    expect(h.last()?.[0]).toBe("unavailable");
    expect(h.events).toEqual([]);
    await vi.waitFor(() => expect(cancelled).toBe(true));
    expect(h.time.armed()).toBe(0);
  });

  it("reports unavailable when the response has no body", async () => {
    const h = harness(async () => new Response(null, { status: 200, headers: SSE }));
    await h.stream.start();
    expect(h.last()?.[0]).toBe("unavailable");
  });

  it("gives up when connections keep closing quickly with nothing at all", async () => {
    const open = vi.fn(async () => sseResponse([]));
    const h = harness(open, { maxFailures: 3 });
    await h.stream.start();
    expect(open).toHaveBeenCalledTimes(3);
    expect(h.last()?.[0]).toBe("unavailable");
  });

  it("counts a heartbeat as delivery: a quickly closing stream that heartbeats is not a failure", async () => {
    let n = 0;
    const open = vi.fn(async () => (++n <= 8 ? sseResponse([": heartbeat\n\n"]) : sseResponse([end("applied")])));
    const h = harness(open, { maxFailures: 3 });
    await h.stream.start();
    expect(open).toHaveBeenCalledTimes(9);
    expect(h.last()?.[0]).toBe("ended");
  });

  it("keeps a healthy idle stream going past many lifetime caps", async () => {
    let n = 0;
    const h = harness(async () => {
      // Each connection lasts the backend's 5-minute cap with heartbeats only.
      h.time.advance(300_000);
      n += 1;
      return n <= 12 ? sseResponse([": heartbeat\n\n", ": heartbeat\n\n"]) : sseResponse([end("applied")]);
    });
    await h.stream.start();
    expect(n).toBe(13); // an hour of idle streaming, never "unavailable"
    expect(h.last()?.[0]).toBe("ended");
  });

  it("does not count an empty connection that stayed open past minHealthyMs", async () => {
    let n = 0;
    const h = harness(async () => {
      h.time.advance(15_000); // open for 15 seconds, then closed with nothing sent
      n += 1;
      return n <= 6 ? sseResponse([]) : sseResponse([end("applied")]);
    }, { maxFailures: 3 });
    await h.stream.start();
    expect(n).toBe(7);
    expect(h.last()?.[0]).toBe("ended");
  });

  it("cuts a silent connection with the idle watchdog and resumes from the last id", async () => {
    const seen: Array<string | undefined> = [];
    const h = harness(async (id) => {
      seen.push(id);
      return seen.length === 1 ? sseResponse([status(4, "validating")], { hold: true }) : sseResponse([end("applied")]);
    });
    const done = h.stream.start();
    await vi.waitFor(() => expect(h.events).toHaveLength(1));
    expect(h.time.fireAll()).toBe(1); // 45 seconds of silence
    await done;
    expect(seen).toEqual([undefined, "4"]);
    expect(h.last()?.[0]).toBe("ended");
    expect(h.time.armed()).toBe(0);
  });

  it("counts a watchdog cut with nothing received as a failure", async () => {
    const open = vi.fn(async () => sseResponse([], { hold: true }));
    const h = harness(open, { maxFailures: 2 });
    const done = h.stream.start();
    await vi.waitFor(() => expect(h.last()?.[0]).toBe("open"));
    h.time.fireAll();
    await vi.waitFor(() => expect(open).toHaveBeenCalledTimes(2));
    await vi.waitFor(() => expect(h.last()?.[0]).toBe("open"));
    h.time.fireAll();
    await done;
    expect(h.last()?.[0]).toBe("unavailable");
  });

  it("cuts an open that hangs before any answer", async () => {
    let calls = 0;
    const h = harness((_id, signal) => {
      calls += 1;
      if (calls > 1) return Promise.resolve(sseResponse([end("applied")]));
      return new Promise<Response>((_resolve, reject) => signal.addEventListener("abort", () => reject(new Error("aborted"))));
    });
    const done = h.stream.start();
    await vi.waitFor(() => expect(calls).toBe(1));
    h.time.fireAll();
    await done;
    expect(calls).toBe(2);
    expect(h.last()?.[0]).toBe("ended");
  });

  it("waits a minimum interval before reconnecting a stream that delivers one event per connection", async () => {
    let n = 0;
    const h = harness(async () => {
      n += 1;
      return n <= 4 ? sseResponse([status(n, "pr_open")]) : sseResponse([end("applied")]);
    }, { minReconnectIntervalMs: 3000 });
    await h.stream.start();
    expect(h.sleeps).toEqual([3000, 3000, 3000, 3000]);
  });

  it("reconnects at once after a long connection", async () => {
    let n = 0;
    const h = harness(async () => {
      n += 1;
      if (n === 1) h.time.advance(300_000); // this connection lasts five minutes
      return n === 1 ? sseResponse([status(1, "pr_open")]) : sseResponse([end("applied")]);
    });
    await h.stream.start();
    expect(h.sleeps).toEqual([]);
  });

  it("ignores frames it cannot decode and keeps reading", async () => {
    const h = harness(async () => sseResponse(["event: status\ndata: {bad\n\n", "event: nope\ndata: {}\n\n", end("applied")]));
    await h.stream.start();
    expect(h.events).toEqual([{ type: "end", status: "applied" }]);
  });

  it("close aborts the pending request and stops without further callbacks", async () => {
    let aborted = false;
    const h = harness(
      (_id, signal) =>
        new Promise<Response>((_resolve, reject) => {
          signal.addEventListener("abort", () => {
            aborted = true;
            reject(new Error("aborted"));
          });
        }),
    );
    const done = h.stream.start();
    h.stream.close();
    await done;
    expect(aborted).toBe(true);
    expect(h.last()?.[0]).toBe("closed");
    h.stream.close(); // idempotent
    expect(h.states.filter(([s]) => s === "closed")).toHaveLength(1);
  });

  it("close during a read ends the loop without reconnecting", async () => {
    const open = vi.fn(async () => {
      // A body that never produces data.
      return new Response(new ReadableStream<Uint8Array>({ start() {} }), { status: 200, headers: SSE });
    });
    const h = harness(open);
    const done = h.stream.start();
    await vi.waitFor(() => expect(h.last()?.[0]).toBe("open"));
    h.stream.close();
    await done; // the waiting read is released, and the loop must not start another attempt
    expect(open).toHaveBeenCalledTimes(1);
    expect(h.last()?.[0]).toBe("closed");
  });

  it("start is idempotent", async () => {
    const open = vi.fn(async () => sseResponse([end("applied")]));
    const h = harness(open);
    await Promise.all([h.stream.start(), h.stream.start()]);
    expect(open).toHaveBeenCalledTimes(1);
  });
});
