import { describe, expect, it } from "vitest";

import { parseSse } from "./sse";

describe("parseSse", () => {
  it("parses complete frames and keeps the unfinished tail", () => {
    const out = parseSse('id: 7\nevent: status\ndata: {"a":1}\n\nid: 8\nevent: sta');
    expect(out.frames).toEqual([{ id: "7", event: "status", data: '{"a":1}' }]);
    expect(out.rest).toBe("id: 8\nevent: sta");
  });

  it("resumes from the tail when the next chunk arrives", () => {
    const first = parseSse("event: end\ndata: {");
    const second = parseSse(`${first.rest}"x":1}\n\n`);
    expect(second.frames).toEqual([{ event: "end", data: '{"x":1}' }]);
    expect(second.rest).toBe("");
  });

  it("ignores heartbeat comments and unknown fields", () => {
    const out = parseSse(": heartbeat\n\nfoo: bar\nevent: end\ndata: {}\n\n");
    expect(out.frames).toEqual([{ event: "end", data: "{}" }]);
  });

  it("accepts CRLF and CR line endings, also split across chunks", () => {
    expect(parseSse("event: end\r\ndata: {}\r\n\r\n").frames).toEqual([{ event: "end", data: "{}" }]);
    expect(parseSse("event: end\rdata: {}\r\rid: 1").frames).toEqual([{ event: "end", data: "{}" }]);
    const held = parseSse("event: end\ndata: {}\n\r");
    expect(held.rest.endsWith("\r")).toBe(true);
    expect(parseSse(`${held.rest}\n`).frames).toEqual([{ event: "end", data: "{}" }]);
  });

  it("joins multiple data lines and strips one leading space", () => {
    expect(parseSse("data: a\ndata:  b\n\n").frames).toEqual([{ data: "a\n b" }]);
  });

  it("reads retry only when it is a number, and drops ids with NUL", () => {
    expect(parseSse("retry: 1500\ndata: x\n\n").frames[0]?.retry).toBe(1500);
    expect(parseSse("retry: soon\ndata: x\n\n").frames[0]?.retry).toBeUndefined();
    expect(parseSse("id: a\u0000b\ndata: x\n\n").frames[0]?.id).toBeUndefined();
  });

  it("returns nothing for an empty buffer", () => {
    expect(parseSse("")).toEqual({ frames: [], rest: "" });
  });
});
