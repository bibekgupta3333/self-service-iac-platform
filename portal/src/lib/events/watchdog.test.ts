import { afterEach, describe, expect, it, vi } from "vitest";

import { IdleWatchdog } from "./watchdog";

describe("IdleWatchdog", () => {
  afterEach(() => void vi.useRealTimers());

  it("fires once after the timeout without a kick", () => {
    vi.useFakeTimers();
    const onIdle = vi.fn();
    const w = new IdleWatchdog(45_000, onIdle);
    w.start();
    vi.advanceTimersByTime(44_999);
    expect(onIdle).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(onIdle).toHaveBeenCalledTimes(1);
    w.kick(); // fired watchdogs stay fired
    vi.advanceTimersByTime(100_000);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("restarts the countdown on every kick", () => {
    vi.useFakeTimers();
    const onIdle = vi.fn();
    const w = new IdleWatchdog(45_000, onIdle);
    w.start();
    for (let i = 0; i < 10; i++) {
      vi.advanceTimersByTime(15_000); // a heartbeat every 15 seconds
      w.kick();
    }
    expect(onIdle).not.toHaveBeenCalled();
    vi.advanceTimersByTime(45_000);
    expect(onIdle).toHaveBeenCalledTimes(1);
  });

  it("never fires after stop, and a zero timeout disables it", () => {
    vi.useFakeTimers();
    const onIdle = vi.fn();
    const stopped = new IdleWatchdog(1000, onIdle);
    stopped.start();
    stopped.stop();
    stopped.stop();
    new IdleWatchdog(0, onIdle).start();
    vi.advanceTimersByTime(10_000);
    expect(onIdle).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });
});
