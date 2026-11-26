import { describe, expect, it } from "vitest";

import { applyField, EMPTY_DRAFT, finishFrame, parseLine, SseParser } from "./sse";

/** feed pushes the chunks through one parser and collects everything it reported. */
function feed(chunks: string[], max?: number) {
  const p = new SseParser(max);
  return chunks.reduce(
    (acc, c) => {
      const r = p.push(c);
      return { frames: [...acc.frames, ...r.frames], comments: acc.comments + r.comments, dropped: acc.dropped + r.dropped };
    },
    { frames: [] as ReturnType<SseParser["push"]>["frames"], comments: 0, dropped: 0 },
  );
}

describe("parseLine", () => {
  it.each([
    ["", { kind: "blank" }],
    [": heartbeat", { kind: "comment" }],
    ["data: x", { kind: "field", field: "data", value: "x" }],
    ["data:  x", { kind: "field", field: "data", value: " x" }],
    ["data", { kind: "field", field: "data", value: "" }],
    ["id:7", { kind: "field", field: "id", value: "7" }],
  ])("%j", (line, want) => expect(parseLine(line)).toEqual(want));
});

describe("applyField and finishFrame", () => {
  it("builds a frame and counts its size", () => {
    const d = [
      ["id", "7"],
      ["event", "status"],
      ["data", "a"],
      ["data", "b"],
      ["unknown", "zzz"],
    ].reduce((acc, [f, v]) => applyField(acc, f as string, v as string, 10), EMPTY_DRAFT);
    expect(d.size).toBe(50);
    expect(finishFrame(d)).toEqual({ id: "7", event: "status", data: "a\nb" });
  });

  it("gives nothing for a draft without fields", () => {
    expect(finishFrame(EMPTY_DRAFT)).toBeUndefined();
    expect(finishFrame(applyField(EMPTY_DRAFT, "foo", "bar", 8))).toBeUndefined();
  });
});

describe("SseParser", () => {
  it("parses complete frames and keeps the unfinished tail for the next chunk", () => {
    const p = new SseParser();
    expect(p.push('id: 7\nevent: status\ndata: {"a":1}\n\nid: 8\nevent: sta').frames).toEqual([
      { id: "7", event: "status", data: '{"a":1}' },
    ]);
    expect(p.push('tus\ndata: {"b":2}\n\n').frames).toEqual([{ id: "8", event: "status", data: '{"b":2}' }]);
  });

  it("handles a frame split at every character", () => {
    const text = 'event: end\ndata: {"x":1}\n\n';
    expect(feed([...text]).frames).toEqual([{ event: "end", data: '{"x":1}' }]);
  });

  it("counts heartbeat comments and ignores unknown fields", () => {
    const out = feed([": heartbeat\n\nfoo: bar\nevent: end\ndata: {}\n\n: heartbeat\n\n"]);
    expect(out.frames).toEqual([{ event: "end", data: "{}" }]);
    expect(out.comments).toBe(2);
  });

  it("accepts CRLF and CR line endings, also a CRLF split across chunks", () => {
    expect(feed(["event: end\r\ndata: {}\r\n\r\n"]).frames).toEqual([{ event: "end", data: "{}" }]);
    expect(feed(["event: end\rdata: {}\r\rid: 1"]).frames).toEqual([{ event: "end", data: "{}" }]);
    // The CR ends the line; the LF that follows in the next chunk must not end another (blank) line.
    expect(feed(["data: a\r", "\ndata: b\r", "\n\r", "\n"]).frames).toEqual([{ data: "a\nb" }]);
  });

  it("keeps a CRLF split by an empty chunk as one line break", () => {
    // Without the guard the LF after the empty chunk would read as a blank line and end the frame early.
    expect(feed(["data: a\r", "", "\ndata: b\n\n"]).frames).toEqual([{ data: "a\nb" }]);
  });

  it("joins multiple data lines and strips one leading space", () => {
    expect(feed(["data: a\ndata:  b\n\n"]).frames).toEqual([{ data: "a\n b" }]);
  });

  it("reads retry only when it is a number, and drops ids with NUL", () => {
    expect(feed(["retry: 1500\ndata: x\n\n"]).frames[0]?.retry).toBe(1500);
    expect(feed(["retry: soon\ndata: x\n\n"]).frames[0]?.retry).toBeUndefined();
    expect(feed(["id: a\u0000b\ndata: x\n\n"]).frames[0]?.id).toBeUndefined();
  });

  it("returns nothing for an empty chunk", () => {
    expect(new SseParser().push("")).toEqual({ frames: [], comments: 0, dropped: 0 });
  });

  it("drops a frame over the cap, even one long line arriving in pieces, and resyncs at the blank line", () => {
    const big = "x".repeat(40);
    const out = feed(["id: 1\ndata: ", big.slice(0, 20), big.slice(20), "\ndata: more\n", "\n", "id: 2\ndata: ok\n\n"], 32);
    expect(out.dropped).toBe(1);
    expect(out.frames).toEqual([{ id: "2", data: "ok" }]);
  });

  it("drops a frame made of many small lines over the cap", () => {
    const lines = Array.from({ length: 10 }, (_, i) => `data: ${i}\n`).join("");
    const out = feed([lines, "\n", "data: next\n\n"], 40);
    expect(out.dropped).toBe(1);
    expect(out.frames).toEqual([{ data: "next" }]);
  });

  it("keeps a frame exactly at the cap", () => {
    const out = feed(["data: abc\n\n"], 10);
    expect(out).toEqual({ frames: [{ data: "abc" }], comments: 0, dropped: 0 });
  });

  it("does not end discarding on the empty remainder of the line that overflowed", () => {
    // The overflow happens mid-line; the end of that line is not a blank line.
    const out = feed(["data: 0123456789", "0123456789\n", "data: still the big frame\n\n", "data: next\n\n"], 12);
    expect(out.frames).toEqual([{ data: "next" }]);
  });

  it("parses a large frame in many chunks in linear time", () => {
    const p = new SseParser();
    const payload = "y".repeat(200 * 1024);
    p.push("data: ");
    const started = performance.now();
    let frames: unknown[] = [];
    for (let i = 0; i < payload.length; i += 16) frames = p.push(payload.slice(i, i + 16)).frames;
    frames = p.push("\n\n").frames;
    expect(performance.now() - started).toBeLessThan(2000);
    expect(frames).toHaveLength(1);
  });
});
