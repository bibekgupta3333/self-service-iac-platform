import { describe, expect, it } from "vitest";

import { decodeFrame, describeEntry, initialLive, liveAnnouncement, reduceLive, type StreamEvent } from "./status";

const frame = (event: string, body: unknown) => ({ event, data: JSON.stringify(body) });

describe("decodeFrame", () => {
  it("decodes a snapshot", () => {
    expect(decodeFrame(frame("snapshot", { request_id: "r", status: "pr_open", kind: "create", updated_at: "2026-01-01T00:00:00Z", seq: 4 }))).toEqual({
      type: "snapshot",
      seq: 4,
      status: "pr_open",
      at: "2026-01-01T00:00:00Z",
    });
  });

  it("decodes a status change with its cause", () => {
    expect(
      decodeFrame(frame("status", { seq: 9, occurred_at: "2026-01-01T00:01:00Z", status: "validating", from: "pr_open", event: "validation_started" })),
    ).toEqual({ type: "status", seq: 9, at: "2026-01-01T00:01:00Z", status: "validating", from: "pr_open", event: "validation_started" });
  });

  it("decodes end", () => {
    expect(decodeFrame(frame("end", { request_id: "r", status: "applied" }))).toEqual({ type: "end", status: "applied" });
  });

  it.each([
    ["not json", { event: "status", data: "{oops" }],
    ["not an object", { event: "status", data: "[1]" }],
    ["no status", frame("status", { seq: 1 })],
    ["negative seq", frame("status", { seq: -1, status: "x" })],
    ["fractional seq", frame("snapshot", { seq: 1.5, status: "x" })],
    ["unknown event", frame("ping", { status: "x" })],
    ["default event", { data: '{"status":"x"}' }],
  ])("ignores %s", (_name, f) => {
    expect(decodeFrame(f)).toBeNull();
  });
});

const status = (seq: number, to: string, event?: string): StreamEvent => ({ type: "status", seq, at: `t${seq}`, status: to, event });

describe("reduceLive", () => {
  it("appends status changes in order and tracks the resume point", () => {
    const s = [status(3, "pr_open", "pr_opened"), status(5, "validating", "validation_started")].reduce(
      reduceLive,
      initialLive("draft"),
    );
    expect(s.status).toBe("validating");
    expect(s.lastSeq).toBe(5);
    expect(s.entries.map((e) => e.seq)).toEqual([3, 5]);
    expect(s.ended).toBe(false);
  });

  it("drops a repeated or older frame", () => {
    const once = reduceLive(initialLive("draft"), status(3, "pr_open"));
    expect(reduceLive(once, status(3, "pr_open"))).toBe(once);
    expect(reduceLive(once, status(2, "draft"))).toBe(once);
  });

  it("a snapshot sets the status and position without adding an entry", () => {
    const s = reduceLive(initialLive("draft"), { type: "snapshot", seq: 6, status: "plan_ready", at: "t" });
    expect(s).toMatchObject({ status: "plan_ready", lastSeq: 6, entries: [] });
  });

  it("ignores a snapshot older than what was seen", () => {
    const s = reduceLive(initialLive("draft"), status(8, "approved"));
    expect(reduceLive(s, { type: "snapshot", seq: 2, status: "draft", at: "t" })).toBe(s);
  });

  it("end marks the request finished", () => {
    const s = reduceLive(reduceLive(initialLive("applying"), status(9, "applied")), { type: "end", status: "applied" });
    expect(s).toMatchObject({ status: "applied", ended: true });
  });

  it("a request that starts terminal is ended", () => {
    expect(initialLive("failed").ended).toBe(true);
    expect(initialLive("applying").ended).toBe(false);
  });

  it("a polled status updates the status and ends on a terminal one", () => {
    const s = reduceLive(initialLive("pr_open"), { type: "polled", status: "applying", at: "t" });
    expect(s).toMatchObject({ status: "applying", ended: false });
    expect(reduceLive(s, { type: "polled", status: "failed", at: "t2" })).toMatchObject({ status: "failed", ended: true });
  });

  it("does not mutate the previous state", () => {
    const before = initialLive("draft");
    const snapshot = JSON.stringify(before);
    reduceLive(before, status(1, "pr_open"));
    expect(JSON.stringify(before)).toBe(snapshot);
  });
});

describe("describeEntry and liveAnnouncement", () => {
  it("names known events and falls back to the transition", () => {
    expect(describeEntry({ event: "pr_merged", status: "applying" })).toBe("Pull request merged");
    expect(describeEntry({ event: "something_new", status: "applying", from: "approved" })).toBe("approved to applying");
    expect(describeEntry({ status: "draft" })).toBe("Request created");
    expect(describeEntry({ status: "applying" })).toBe("applying");
  });

  it("phrases the live region", () => {
    expect(liveAnnouncement("Applying", false)).toBe("Request status: Applying.");
    expect(liveAnnouncement("Applied", true)).toBe("Request applied. No further changes.");
  });
});
