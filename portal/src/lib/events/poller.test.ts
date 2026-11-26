import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";

import { pollStopNotice, RequestPoller, type Visibility } from "./poller";

const problem = (status: number) => new ApiError(parseProblem(status, { title: "x", status, code: "x" }));

/** A hand-driven interval and visibility: tests call tick() and setHidden(). */
function harness(read: () => Promise<{ status: string; updated_at: string }>, opts: { hidden?: boolean } = {}) {
  let hidden = opts.hidden ?? false;
  let listener: (() => void) | undefined;
  const ticks = new Set<() => void>();
  const visibility: Visibility = {
    hidden: () => hidden,
    onChange: (cb) => {
      listener = cb;
      return () => (listener = undefined);
    },
  };
  const statuses: string[] = [];
  const onStop = vi.fn();
  const poller = new RequestPoller({
    read: vi.fn(read),
    onStatus: (s) => statuses.push(s),
    onStop,
    isFinal: (s) => s === "applied",
    intervalMs: 5000,
    visibility,
    every: (fn) => {
      ticks.add(fn);
      return () => void ticks.delete(fn);
    },
  });
  return {
    poller,
    statuses,
    onStop,
    tick: () => ticks.forEach((fn) => fn()),
    armed: () => ticks.size,
    subscribed: () => listener !== undefined,
    setHidden: (h: boolean) => {
      hidden = h;
      listener?.();
    },
  };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("pollStopNotice", () => {
  it("stops on 401, 403 and 404 only", () => {
    expect(pollStopNotice(problem(401))?.code).toBe("unauthorized");
    expect(pollStopNotice(problem(403))?.code).toBe("forbidden");
    expect(pollStopNotice(problem(404))?.code).toBe("not_found");
    expect(pollStopNotice(problem(429))).toBeUndefined();
    expect(pollStopNotice(problem(503))).toBeUndefined();
    expect(pollStopNotice(new Error("x"))).toBeUndefined();
  });
});

describe("RequestPoller", () => {
  it("reads at once and on every tick, and stops at a final status", async () => {
    const answers = ["validating", "plan_ready", "applied"];
    const h = harness(async () => ({ status: answers.shift() ?? "applied", updated_at: "t" }));
    h.poller.start();
    await flush();
    h.tick();
    await flush();
    h.tick();
    await flush();
    expect(h.statuses).toEqual(["validating", "plan_ready", "applied"]);
    expect(h.armed()).toBe(0);
    expect(h.subscribed()).toBe(false);
  });

  it.each([401, 403, 404])("stops on %i and reports why", async (status) => {
    const read = vi.fn(async () => {
      throw problem(status);
    });
    const h = harness(read);
    h.poller.start();
    await flush();
    h.tick();
    await flush();
    expect(read).toHaveBeenCalledTimes(1);
    expect(h.onStop).toHaveBeenCalledTimes(1);
    expect(h.armed()).toBe(0);
  });

  it("keeps polling after a transient error", async () => {
    let n = 0;
    const h = harness(async () => {
      if (++n === 1) throw problem(503);
      return { status: "validating", updated_at: "t" };
    });
    h.poller.start();
    await flush();
    h.tick();
    await flush();
    expect(h.statuses).toEqual(["validating"]);
    expect(h.onStop).not.toHaveBeenCalled();
  });

  it("pauses while the tab is hidden and reads at once when it is shown again", async () => {
    const read = vi.fn(async () => ({ status: "validating", updated_at: "t" }));
    const h = harness(read);
    h.poller.start();
    await flush();
    expect(read).toHaveBeenCalledTimes(1);
    h.setHidden(true);
    expect(h.armed()).toBe(0);
    h.tick();
    await flush();
    expect(read).toHaveBeenCalledTimes(1);
    h.setHidden(false);
    await flush();
    expect(read).toHaveBeenCalledTimes(2);
    expect(h.armed()).toBe(1);
  });

  it("does not read when started in a hidden tab", async () => {
    const read = vi.fn(async () => ({ status: "validating", updated_at: "t" }));
    const h = harness(read, { hidden: true });
    h.poller.start();
    await flush();
    expect(read).not.toHaveBeenCalled();
    expect(h.armed()).toBe(0);
    h.setHidden(false);
    await flush();
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("does not overlap reads and reports nothing after stop", async () => {
    let resolve: (v: { status: string; updated_at: string }) => void = () => undefined;
    const read = vi.fn(() => new Promise<{ status: string; updated_at: string }>((r) => (resolve = r)));
    const h = harness(read);
    h.poller.start();
    h.tick();
    h.tick();
    expect(read).toHaveBeenCalledTimes(1);
    h.poller.stop();
    resolve({ status: "validating", updated_at: "t" });
    await flush();
    expect(h.statuses).toEqual([]);
  });
});
