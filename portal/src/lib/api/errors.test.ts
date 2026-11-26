import { describe, expect, it } from "vitest";

import { ApiError, isApiError, parseProblem } from "./errors";

describe("parseProblem", () => {
  it("keeps the backend's problem fields", () => {
    const p = parseProblem(409, {
      type: "https://iacp.acme.internal/errors/stack_exists",
      title: "Request conflicts with current state",
      status: 409,
      code: "stack_exists",
      detail: "stack already exists",
      instance: "/v1/requests",
      request_id: "rid",
    });
    expect(p).toEqual({
      type: "https://iacp.acme.internal/errors/stack_exists",
      title: "Request conflicts with current state",
      status: 409,
      code: "stack_exists",
      detail: "stack already exists",
      instance: "/v1/requests",
      request_id: "rid",
      errors: [],
    });
  });

  it("drops malformed field errors and defaults a missing code", () => {
    const p = parseProblem(422, {
      code: "validation_failed",
      errors: [{ pointer: "/stack", message: "bad" }, { pointer: 3 }, "nope", null],
    });
    expect(p.errors).toEqual([{ pointer: "/stack", message: "bad", code: "invalid" }]);
    expect(p.title).toBe("HTTP 422");
  });

  it.each([
    [undefined, 500, "internal"],
    ["text", 404, "http_404"],
    [[1, 2], 400, "http_400"],
  ])("handles a non-object body %o with status %i", (body, status, code) => {
    const p = parseProblem(status, body, "rid");
    expect(p.code).toBe(code);
    expect(p.status).toBe(status);
    expect(p.request_id).toBe("rid");
    expect(p.errors).toEqual([]);
  });
});

describe("ApiError", () => {
  it("is recognisable and exposes the problem", () => {
    const e = new ApiError(parseProblem(404, { title: "Resource not found", code: "not_found" }));
    expect(isApiError(e)).toBe(true);
    expect(isApiError(new Error("x"))).toBe(false);
    expect(e.message).toBe("Resource not found");
    expect(e.name).toBe("ApiError");
  });
});
