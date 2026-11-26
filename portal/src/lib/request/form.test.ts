import { describe, expect, it } from "vitest";

import {
  buildCreatePayload,
  decodePointer,
  fieldId,
  hasErrors,
  inputErrors,
  keyForPointer,
  mapFieldErrors,
  type StackFields,
  validateStackFields,
} from "./form";

const valid: StackFields = {
  team: "payments",
  environment: "development",
  stack: "order-events",
  resourceName: "queue",
  justification: "",
};

describe("validateStackFields", () => {
  it("accepts valid fields", () => expect(validateStackFields(valid)).toEqual({}));

  it("flags each bad field", () => {
    const errors = validateStackFields({
      team: "Payments",
      environment: "",
      stack: "1x",
      resourceName: "bad-name",
      justification: "x".repeat(2001),
    });
    expect(Object.keys(errors).sort()).toEqual(["environment", "justification", "resourceName", "stack", "team"]);
  });
});

describe("buildCreatePayload", () => {
  it("trims values and omits an empty justification", () => {
    const p = buildCreatePayload({ ...valid, team: " payments " }, "sqs", "1.0.0", { name: "orders" });
    expect(p).toEqual({
      kind: "create",
      team: "payments",
      environment: "development",
      stack: "order-events",
      resources: [{ name: "queue", module: "sqs", version: "1.0.0", inputs: { name: "orders" } }],
    });
  });

  it("keeps a justification", () => {
    expect(buildCreatePayload({ ...valid, justification: " FEAT-1 " }, "sqs", "1.0.0", {}).justification).toBe("FEAT-1");
  });
});

describe("decodePointer", () => {
  it.each([
    ["", []],
    ["/", []],
    ["/team", ["team"]],
    ["/resources/0/inputs/a~1b~0c", ["resources", "0", "inputs", "a/b~c"]],
  ])("%s", (ptr, want) => expect(decodePointer(ptr)).toEqual(want));
});

describe("keyForPointer", () => {
  it.each([
    ["/team", { key: "team", rest: [] }],
    ["/stack", { key: "stack", rest: [] }],
    ["/justification", { key: "justification", rest: [] }],
    ["/resources/0/name", { key: "resourceName", rest: [] }],
    ["/resources/0/inputs/name", { key: "input:name", rest: [] }],
    ["/resources/0/inputs/tags/env", { key: "input:tags", rest: ["env"] }],
  ])("%s", (ptr, want) => expect(keyForPointer(ptr)).toEqual(want));

  it.each([["/kind"], ["/resources"], ["/resources/1/inputs/name"], ["/resources/0/version"], [""]])(
    "%s has no control",
    (ptr) => expect(keyForPointer(ptr)).toBeUndefined(),
  );
});

describe("mapFieldErrors", () => {
  it("routes errors to controls and keeps the rest as general errors", () => {
    const mapped = mapFieldErrors([
      { pointer: "/team", code: "pattern", message: "must be a team slug" },
      { pointer: "/resources/0/inputs/name", code: "pattern", message: "bad name" },
      { pointer: "/resources/0/inputs/name", code: "minLength", message: "too short" },
      { pointer: "/resources/0/inputs/tags/iacp:x", code: "not", message: "reserved" },
      { pointer: "/resources/0/version", code: "module_version_unavailable", message: "sqs@1.0.0 is yanked" },
      { pointer: "", code: "x", message: "whole body" },
    ]);
    expect(mapped).toEqual({
      fields: {
        team: ["must be a team slug"],
        "input:name": ["bad name", "too short"],
        "input:tags": ["iacp:x: reserved"],
      },
      general: ["/resources/0/version: sqs@1.0.0 is yanked", "whole body"],
    });
  });
});

describe("helpers", () => {
  it("lifts input errors", () => {
    expect(inputErrors({ name: "is required" })).toEqual({ "input:name": ["is required"] });
  });

  it("detects errors", () => {
    expect(hasErrors({})).toBe(false);
    expect(hasErrors({ team: [] })).toBe(false);
    expect(hasErrors({ team: ["x"] })).toBe(true);
  });

  it("builds DOM ids", () => {
    expect(fieldId("team")).toBe("field-team");
    expect(fieldId("input:kms_key_arn")).toBe("field-input-kms_key_arn");
  });
});
