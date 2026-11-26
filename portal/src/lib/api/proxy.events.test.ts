import { describe, expect, it } from "vitest";

import {
  decideProxy,
  FORWARD_REQUEST_HEADERS,
  FORWARD_RESPONSE_HEADERS,
  isEventsPath,
  type ProxyRequestInfo,
} from "./proxy";

const ID = "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90";
const base = "http://api.test";

const info = (over: Partial<ProxyRequestInfo>): ProxyRequestInfo => ({
  method: "GET",
  segments: ["v1", "requests", ID, "events"],
  search: "",
  contentLength: null,
  origin: null,
  secFetchSite: null,
  allowedOrigins: ["https://portal.test"],
  ...over,
});

describe("isEventsPath", () => {
  it.each([
    [["v1", "requests", ID, "events"], true],
    [["v1", "requests", ID.toUpperCase(), "events"], true], // the backend parses any-case UUIDs
    // exact and case-sensitive after the id
    [["v1", "requests", ID, "Events"], false],
    [["v1", "requests", ID, "EVENTS"], false],
    [["v1", "Requests", ID, "events"], false],
    [["V1", "requests", ID, "events"], false],
    // the id must be a UUID
    [["v1", "requests", "req-7k3p9x", "events"], false],
    [["v1", "requests", "..", "events"], false],
    [["v1", "requests", `${ID}x`, "events"], false],
    [["v1", "requests", "", "events"], false],
    // exactly four segments
    [["v1", "requests", ID], false],
    [["v1", "requests", ID, "events", "extra"], false],
    [["v1", "requests", ID, "events", ".."], false],
    [["v1", "catalog", ID, "events"], false],
    [["v1", "internal", "requests", ID, "events"], false],
  ] as [string[], boolean][])("%j -> %s", (segments, want) => expect(isEventsPath(segments)).toBe(want));
});

describe("the event stream through decideProxy", () => {
  it("forwards GET as a streamed upstream GET", () => {
    expect(decideProxy(base, info({}))).toEqual({
      kind: "forward",
      url: `http://api.test/v1/requests/${ID}/events`,
      upstreamMethod: "GET",
      head: false,
      authFlow: false,
      stream: true,
    });
  });

  it("passes the query string and the case-insensitive id through unchanged", () => {
    expect(decideProxy(base, info({ search: "?x=1", segments: ["v1", "requests", ID.toUpperCase(), "events"] }))).toMatchObject({
      kind: "forward",
      url: `http://api.test/v1/requests/${ID.toUpperCase()}/events?x=1`,
      stream: true,
    });
  });

  // HEAD would be answered by draining an upstream GET, which never ends on a stream.
  it.each(["HEAD", "POST", "PUT", "PATCH", "DELETE"])("refuses %s", (method) => {
    const d = decideProxy(base, info({ method, contentLength: "2", secFetchSite: "same-origin" }));
    expect(d.kind).toBe("reject");
    if (d.kind === "reject") expect(d.status).toBe(405);
    if (d.kind === "reject" && ["HEAD", "POST"].includes(method)) expect(d.allow).toBe("GET");
  });

  it("is not reachable by traversal, encoded separators or another case", () => {
    for (const segments of [
      ["v1", "requests", ID, "events", ".."],
      ["v1", "requests", "..", ID, "events"],
      ["v1", "requests", `${ID}%2Fevents`],
      ["v1", "requests", ID, "ev%65nts"],
      ["v1", "requests", ID, "events%2F..%2Fx"],
    ]) {
      const d = decideProxy(base, info({ segments }));
      // either refused outright, or forwarded as an ordinary non-stream request
      expect(d.kind === "reject" || d.stream === undefined).toBe(true);
    }
    expect(decideProxy(base, info({ segments: ["v1", "requests", ID, "events", ".."] }))).toMatchObject({ kind: "reject", status: 404 });
    expect(decideProxy(base, info({ segments: ["v1", "requests", ID, "ev%65nts"] }))).toMatchObject({ kind: "reject", status: 404 });
  });

  it("does not make other request paths streams, and keeps their POST rules", () => {
    expect(decideProxy(base, info({ segments: ["v1", "requests", ID] }))).toEqual({
      kind: "forward",
      url: `http://api.test/v1/requests/${ID}`,
      upstreamMethod: "GET",
      head: false,
      authFlow: false,
    });
    expect(decideProxy(base, info({ method: "POST", segments: ["v1", "requests", ID, "cancel"] }))).toMatchObject({ status: 405 });
  });

  it("forwards Last-Event-ID and passes the proxy-buffering hint back", () => {
    expect(FORWARD_REQUEST_HEADERS).toContain("last-event-id");
    expect(FORWARD_RESPONSE_HEADERS).toContain("x-accel-buffering");
  });
});
