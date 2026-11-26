import { describe, expect, it } from "vitest";

import { isSafeSegment, pickHeaders, proxyTarget } from "./proxy";

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

describe("pickHeaders", () => {
  it("copies only allow-listed headers", () => {
    const h = pickHeaders(new Headers({ "Idempotency-Key": "k", Host: "evil", "X-Other": "1" }), [
      "idempotency-key",
      "x-request-id",
    ]);
    expect([...h.entries()]).toEqual([["idempotency-key", "k"]]);
  });
});
