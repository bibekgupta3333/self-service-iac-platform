import { describe, expect, it } from "vitest";

import { pickHeaders, proxyTarget } from "./proxy";

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
    [["v1", "runs"]],
    [["v1", "requests", ".."]],
    [["v1", "requests", "a/b"]],
    [["v1", "requests", "a?b"]],
  ])("refuses %o", (segments) => {
    expect(proxyTarget(base, segments, "")).toBeNull();
  });
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
