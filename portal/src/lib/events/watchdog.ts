// IdleWatchdog fires once when nothing has kicked it for timeoutMs. The status stream kicks it
// on every frame or heartbeat; the backend heartbeats every 15 seconds, so a long silence means
// the connection is dead even though the socket never reported it.

/** Schedule runs fn after ms and returns a function that cancels it. */
export type Schedule = (fn: () => void, ms: number) => () => void;

export const timerSchedule: Schedule = (fn, ms) => {
  const t = setTimeout(fn, ms);
  return () => clearTimeout(t);
};

export class IdleWatchdog {
  private cancel: (() => void) | undefined;
  private done = false;

  constructor(
    private readonly timeoutMs: number,
    private readonly onIdle: () => void,
    private readonly schedule: Schedule = timerSchedule,
  ) {}

  /** start arms the watchdog. A timeout of 0 or less disables it. */
  start(): void {
    this.kick();
  }

  /** kick restarts the countdown; something arrived. */
  kick(): void {
    if (this.done || this.timeoutMs <= 0) return;
    this.cancel?.();
    this.cancel = this.schedule(() => {
      this.cancel = undefined;
      if (this.done) return;
      this.done = true;
      this.onIdle();
    }, this.timeoutMs);
  }

  /** stop disarms it for good. Safe to call repeatedly. */
  stop(): void {
    this.done = true;
    this.cancel?.();
    this.cancel = undefined;
  }
}
