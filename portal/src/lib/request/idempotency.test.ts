import { describe, expect, it } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";

import { classifyFailure, keyFor } from "./idempotency";

const counter = () => {
  let n = 0;
  return () => `k${++n}`;
};

describe("keyFor", () => {
  it("mints a key for the first payload", () => {
    expect(keyFor(null, "a", counter())).toEqual({ key: "k1", payload: "a" });
  });

  it("reuses the key while the payload is unchanged", () => {
    const prev = { key: "k1", payload: "a" };
    expect(keyFor(prev, "a", counter())).toBe(prev);
  });

  it("mints a new key when the payload changes", () => {
    expect(keyFor({ key: "k1", payload: "a" }, "b", () => "k2")).toEqual({ key: "k2", payload: "b" });
  });
});

describe("classifyFailure", () => {
  const api = (status: number, code: string) => new ApiError(parseProblem(status, { status, code }));

  it.each([
    [api(422, "validation_failed"), "rejected"],
    [api(403, "forbidden"), "rejected"],
    [api(409, "stack_exists"), "rejected"],
    [api(422, "idempotency_key_reuse"), "key_reused"],
    [api(502, "api_unreachable"), "ambiguous"],
    [api(500, "internal"), "ambiguous"],
    [ApiError.network(new TypeError("fetch failed")), "ambiguous"],
    [new Error("boom"), "ambiguous"],
  ])("%o -> %s", (err, want) => expect(classifyFailure(err)).toBe(want));
});
