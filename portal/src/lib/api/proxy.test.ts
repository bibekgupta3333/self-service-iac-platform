import { describe, expect, it } from "vitest";

import {
  decideProxy,
  isSafeSegment,
  MAX_BODY_BYTES,
  pickHeaders,
  portalOrigins,
  type ProxyRequestInfo,
  proxyTarget,
  rewriteLocation,
} from "./proxy";

describe("proxyTarget", () => {
  const base = "http://127.0.0.1:8080/";
  it.each([
    [["v1", "catalog", "modules"], "", "http://127.0.0.1:8080/v1/catalog/modules"],
    [["v1", "requests"], "?mine=true", "http://127.0.0.1:8080/v1/requests?mine=true"],
    [["v1", "requests"], "mine=true", "http://127.0.0.1:8080/v1/requests?mine=true"],
    [["v1", "catalog", "modules", "sqs", "versions", "1.0.0"], "?", "http://127.0.0.1:8080/v1/catalog/modules/sqs/versions/1.0.0"],
  ])("forwards %o%s", (segments, search, want) => {
    expect(proxyTarget(base, segments, search)).toBe(want);
  });

  it.each([
    [[]],
    [["v1"]],
    [["v2", "catalog"]],
    [["healthz"]],
    [["v1", "internal", "catalog", "index"]],
    [["v1", "Internal", "catalog", "index"]],
    [["v1", "INTERNAL"]],
    [["v1", "runs"]],
    [["v1", "RUNS"]],
    [["v1", "Runs"]],
    [["v1", "%69nternal", "catalog", "index"]],
    [["v1", "%2569nternal"]],
    [["v1", "Catalog", "modules"]],
    [["v1", "audit", "verify"]],
    [["v1", "auth", "login"]],
    [["v1", "webhooks"]],
    [["v1", "requests", ".."]],
    [["v1", "requests", "."]],
    [["v1", "catalog", "..", "internal"]],
    [["v1", "requests", "a/b"]],
    [["v1", "requests", "%2e%2e"]],
    [["v1", "requests", "%2F"]],
    [["v1", "requests", "a?b"]],
    [["v1", "requests", "a#b"]],
    [["v1", "requests", ""]],
    [["v1", "requests", "a\\b"]],
    [["v1", "requests", "caf\u00e9"]],
  ])("refuses %o", (segments) => {
    expect(proxyTarget(base, segments, "")).toBeNull();
  });
});

describe("isSafeSegment", () => {
  it.each([
    ["sqs", true],
    ["1.0.0", true],
    ["0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90", true],
    [".", false],
    ["..", false],
    ["", false],
    ["a%2Fb", false],
    ["a:b", false],
  ])("%s -> %s", (s, want) => expect(isSafeSegment(s)).toBe(want));
});

describe("decideProxy", () => {
  const base = "http://api.test";
  const info = (over: Partial<ProxyRequestInfo>): ProxyRequestInfo => ({
    method: "GET",
    segments: ["v1", "requests"],
    search: "",
    contentLength: null,
    origin: null,
    secFetchSite: null,
    allowedOrigins: ["https://portal.test"],
    ...over,
  });

  it("forwards GET and HEAD as upstream GET", () => {
    expect(decideProxy(base, info({}))).toEqual({
      kind: "forward",
      url: "http://api.test/v1/requests",
      upstreamMethod: "GET",
      head: false,
    });
    expect(decideProxy(base, info({ method: "HEAD" }))).toMatchObject({ kind: "forward", upstreamMethod: "GET", head: true });
  });

  it("forwards POST only to /v1/requests", () => {
    expect(decideProxy(base, info({ method: "POST", contentLength: "120" }))).toMatchObject({
      kind: "forward",
      upstreamMethod: "POST",
    });
    expect(decideProxy(base, info({ method: "POST", segments: ["v1", "catalog", "modules"] }))).toMatchObject({
      kind: "reject",
      status: 405,
      allow: "GET, HEAD",
    });
    expect(decideProxy(base, info({ method: "POST", segments: ["v1", "requests", "x", "cancel"] }))).toMatchObject({
      status: 405,
    });
  });

  it.each([["PUT"], ["DELETE"], ["PATCH"], ["OPTIONS"]])("refuses %s with 405 and Allow", (method) => {
    expect(decideProxy(base, info({ method }))).toEqual({
      kind: "reject",
      status: 405,
      code: "method_not_allowed",
      detail: `${method} is not supported by the portal proxy`,
      allow: "GET, HEAD, POST",
    });
  });

  it("refuses paths outside the allow-list with 404", () => {
    expect(decideProxy(base, info({ method: "POST", segments: ["v1", "RUNS"] }))).toMatchObject({ status: 404 });
  });

  it.each([
    [{ secFetchSite: "same-origin" }, "forward"],
    [{ secFetchSite: "same-origin", origin: "https://evil.test" }, "forward"],
    [{ secFetchSite: "same-site" }, 403],
    [{ secFetchSite: "cross-site" }, 403],
    [{ secFetchSite: "none" }, 403],
    [{ origin: "https://portal.test" }, "forward"],
    [{ origin: "https://evil.test" }, 403],
    [{ origin: "null" }, 403],
    [{}, "forward"],
  ])("POST with %o -> %s", (headers, want) => {
    const d = decideProxy(base, info({ method: "POST", ...headers }));
    if (want === "forward") expect(d.kind).toBe("forward");
    else expect(d).toMatchObject({ kind: "reject", status: want, code: "cross_site_request" });
  });

  it("does not apply the origin check to GET", () => {
    expect(decideProxy(base, info({ secFetchSite: "cross-site", origin: "https://evil.test" })).kind).toBe("forward");
  });

  it.each([
    [String(MAX_BODY_BYTES), "forward"],
    [String(MAX_BODY_BYTES + 1), 413],
    ["abc", 400],
    ["-1", 400],
    ["1e3", 400],
  ])("Content-Length %s -> %s", (contentLength, want) => {
    const d = decideProxy(base, info({ method: "POST", contentLength }));
    if (want === "forward") expect(d.kind).toBe("forward");
    else expect(d).toMatchObject({ kind: "reject", status: want });
  });
});

describe("portalOrigins", () => {
  it("includes the Host the browser used, the URL origin and the configured origin", () => {
    expect(portalOrigins("http://localhost:3000/api/iacp/v1/requests", "127.0.0.1:3000", "https://portal.example/")).toEqual([
      "http://localhost:3000",
      "http://127.0.0.1:3000",
      "https://portal.example",
    ]);
  });

  it("deduplicates and tolerates a missing Host", () => {
    expect(portalOrigins("http://localhost:3000/x", "localhost:3000", undefined)).toEqual(["http://localhost:3000"]);
    expect(portalOrigins("http://localhost:3000/x", null, "  ")).toEqual(["http://localhost:3000"]);
  });
});

describe("rewriteLocation", () => {
  it.each([
    ["/v1/requests/r1", "/api/iacp/v1/requests/r1"],
    [null, null],
    ["/elsewhere", null],
    ["//evil.test/v1/x", null],
    ["https://api.test/v1/requests/r1", null],
  ])("%s -> %s", (loc, want) => expect(rewriteLocation(loc, "/api/iacp/")).toBe(want));
});

describe("pickHeaders", () => {
  it("copies only allow-listed headers", () => {
    const h = pickHeaders(new Headers({ "Idempotency-Key": "k", Host: "evil", "X-Other": "1" }), [
      "idempotency-key",
      "x-request-id",
    ]);
    expect([...h.entries()]).toEqual([["idempotency-key", "k"]]);
  });
});
