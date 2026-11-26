import { describe, expect, it } from "vitest";

import { SchemaAdapter } from "./adapter";
import { toField } from "./fields";
import { lambdaSchema, sqsSchema } from "./fixtures";
import { clearBehaviour, emptyValueFor, initialValues, parseValue, toInputs, toText } from "./values";

const sqs = new SchemaAdapter({ module: "sqs", version: "1.0.0", inputs_schema: sqsSchema });
const lambda = new SchemaAdapter({ module: "lambda", version: "1.0.0", inputs_schema: lambdaSchema });

describe("toText / initialValues", () => {
  it("seeds controls from defaults", () => {
    expect(initialValues(sqs.fields)).toEqual({
      name: "",
      fifo: false,
      tags: "",
      visibility_timeout_seconds: "30",
      alarm_actions: "",
      max_receive_count: "5",
      kms_key_arn: "",
    });
  });

  it("renders lists, maps and JSON as text", () => {
    expect(toText(toField("a", { type: "array", items: { type: "string" } }, false), ["x", "y"])).toBe("x\ny");
    expect(toText(toField("t", { type: "object", additionalProperties: { type: "string" } }, false), { a: "1" })).toBe(
      "a=1",
    );
    expect(toText(toField("j", { type: "object" }, false), { a: 1 })).toBe('{\n  "a": 1\n}');
  });
});

describe("parseValue", () => {
  const int = toField("n", { type: "integer", minimum: 1, maximum: 10 }, false);
  it.each([
    ["5", { ok: true, value: 5, empty: false }],
    ["", { ok: true, value: undefined, empty: true }],
    ["1.5", { ok: false, error: "must be a whole number" }],
    ["0", { ok: false, error: "must be at least 1" }],
    ["11", { ok: false, error: "must be at most 10" }],
  ])("integer %s", (raw, want) => expect(parseValue(int, raw)).toEqual(want));

  it("treats exclusive bounds as exclusive", () => {
    const f = toField("f", { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 1 }, false);
    expect(f).toMatchObject({ exclusiveMinimum: 0, exclusiveMaximum: 1, minimum: undefined, maximum: undefined });
    expect(parseValue(f, "0")).toEqual({ ok: false, error: "must be greater than 0" });
    expect(parseValue(f, "1")).toEqual({ ok: false, error: "must be less than 1" });
    expect(parseValue(f, "0.5")).toEqual({ ok: true, value: 0.5, empty: false });
  });

  it("checks string patterns and lengths", () => {
    const s = toField("s", { type: "string", pattern: "^[a-z]+$", maxLength: 3 }, false);
    expect(parseValue(s, " abc ")).toEqual({ ok: true, value: "abc", empty: false });
    expect(parseValue(s, "ABC")).toEqual({ ok: false, error: "must match ^[a-z]+$" });
    expect(parseValue(s, "abcd")).toEqual({ ok: false, error: "must be at most 3 characters" });
  });

  it("keeps enum value types", () => {
    const e = toField("e", { type: "integer", enum: [1, 7, 30] }, false);
    expect(parseValue(e, "7")).toEqual({ ok: true, value: 7, empty: false });
    expect(parseValue(e, "8")).toEqual({ ok: false, error: "must be one of 1, 7, 30" });
  });

  it("parses lists and maps line by line", () => {
    const l = toField("l", { type: "array", items: { type: "string", pattern: "^arn:" } }, false);
    expect(parseValue(l, "arn:a\n\n arn:b ")).toEqual({ ok: true, value: ["arn:a", "arn:b"], empty: false });
    expect(parseValue(l, "nope")).toEqual({ ok: false, error: '"nope" must match ^arn:' });
    const m = toField("m", { type: "object", additionalProperties: { type: "string" } }, false);
    expect(parseValue(m, "team = core\nowner=a=b")).toEqual({ ok: true, value: { team: "core", owner: "a=b" }, empty: false });
    expect(parseValue(m, "=x")).toEqual({ ok: false, error: '"=x" must look like key=value' });
  });

  it("parses JSON", () => {
    const j = toField("j", { type: ["object", "null"] }, false);
    expect(parseValue(j, '{"queue_arn":"arn:x"}')).toEqual({ ok: true, value: { queue_arn: "arn:x" }, empty: false });
    expect(parseValue(j, "{")).toEqual({ ok: false, error: "must be valid JSON" });
  });
});

describe("toInputs", () => {
  it("sends only required values and values that differ from defaults", () => {
    const values = { ...sqs.initialValues(), name: "orders", visibility_timeout_seconds: "60", tags: "app=shop" };
    expect(sqs.toInputs(values)).toEqual({
      inputs: { name: "orders", visibility_timeout_seconds: 60, tags: { app: "shop" } },
      errors: {},
    });
  });

  it("reports missing required fields and parse errors by field name", () => {
    const values = { ...sqs.initialValues(), max_receive_count: "500" };
    expect(sqs.toInputs(values).errors).toEqual({ name: "is required", max_receive_count: "must be at most 100" });
  });

  it("sends booleans that differ from the default", () => {
    expect(sqs.toInputs({ ...sqs.initialValues(), name: "orders", fifo: true }).inputs).toEqual({
      name: "orders",
      fifo: true,
    });
  });

  it("sends null when a nullable field with a non-null default is cleared", () => {
    const values = {
      ...lambda.initialValues(),
      name: "fn-orders",
      runtime: "nodejs22.x",
      artifact_s3_uri: "s3://b/k.zip",
      reserved_concurrent_executions: "",
    };
    expect(lambda.toInputs(values)).toEqual({
      inputs: { name: "fn-orders", runtime: "nodejs22.x", artifact_s3_uri: "s3://b/k.zip", reserved_concurrent_executions: null },
      errors: {},
    });
  });

  it("falls back to defaults for fields missing from the values", () => {
    expect(sqs.toInputs({ name: "orders" })).toEqual({ inputs: { name: "orders" }, errors: {} });
  });
});

describe("clearing optional fields", () => {
  const list = toField("l", { type: "array", items: { type: "string" }, default: ["a"] }, false);
  const map = toField("m", { type: "object", additionalProperties: { type: "string" }, default: { a: "1" } }, false);
  const str = toField("s", { type: "string", default: "x" }, false);
  const strPattern = toField("p", { type: "string", pattern: "^[a-z]+$", default: "x" }, false);
  const nonEmptyList = toField("n", { type: "array", items: { type: "string" }, minItems: 1, default: ["a"] }, false);
  const int = toField("i", { type: "integer", default: 3 }, false);
  const nullable = toField("z", { type: ["integer", "null"], default: 10 }, false);
  const nullDefault = toField("k", { type: ["string", "null"], default: null }, false);
  const noDefault = toField("t", { type: "object", additionalProperties: { type: "string" } }, false);
  const emptyDefault = toField("e", { type: "array", items: { type: "string" }, default: [] }, false);

  it.each([
    [list, "empty", { value: [] }],
    [map, "empty", { value: {} }],
    [str, "empty", { value: "" }],
    [strPattern, "default", undefined],
    [nonEmptyList, "default", undefined],
    [int, "default", undefined],
    [nullable, "null", undefined],
    [nullDefault, "default", { value: "" }],
    [noDefault, "omit", { value: {} }],
    [emptyDefault, "default", { value: [] }],
  ])("%#: %o", (field, behaviour, empty) => {
    expect(clearBehaviour(field)).toBe(behaviour);
    expect(emptyValueFor(field)).toEqual(empty);
  });

  it("sends the explicit empty value only where the schema allows it", () => {
    const fields = [list, map, str, strPattern, nonEmptyList, int, nullable, nullDefault, noDefault, emptyDefault];
    const cleared = Object.fromEntries(fields.map((f) => [f.name, ""]));
    expect(toInputs(fields, cleared)).toEqual({ inputs: { l: [], m: {}, s: "", z: null }, errors: {} });
  });

  it("is required for required fields", () => {
    expect(clearBehaviour(toField("r", { type: "string" }, true))).toBe("required");
  });
});

describe("SchemaAdapter", () => {
  it("looks fields up by name and groups them", () => {
    expect(sqs.field("fifo")?.kind).toBe("boolean");
    expect(sqs.field("missing")).toBeUndefined();
    expect(sqs.groups()[0]?.name).toBe("General");
  });

  it.each([
    ["sqs", "sqs"],
    ["api-gateway", "api_gateway"],
    ["3d", "d"],
    ["--", "resource"],
  ])("defaultResourceName(%s) = %s", (module, want) => {
    expect(new SchemaAdapter({ module, version: "1.0.0", inputs_schema: {} }).defaultResourceName()).toBe(want);
  });
});
