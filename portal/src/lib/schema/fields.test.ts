import { describe, expect, it } from "vitest";

import {
  constraintsOf,
  DEFAULT_GROUP,
  escapePointerToken,
  formatDefault,
  groupFields,
  humanize,
  kindOf,
  schemaTypes,
  toField,
  toFieldModels,
} from "./fields";
import { lambdaSchema, sqsSchema } from "./fixtures";

describe("humanize", () => {
  it.each([
    ["visibility_timeout_seconds", "Visibility timeout seconds"],
    ["name", "Name"],
    ["", ""],
  ])("%s -> %s", (input, want) => expect(humanize(input)).toBe(want));
});

describe("escapePointerToken", () => {
  it("escapes ~ before /", () => expect(escapePointerToken("a/b~c")).toBe("a~1b~0c"));
});

describe("schemaTypes", () => {
  it.each([
    [{ type: "string" }, ["string"], false],
    [{ type: ["string", "null"] }, ["string"], true],
    [{ enum: ["Allow", "Deny"] }, ["string"], false],
    [{ enum: [1, 2, null] }, ["number"], true],
    [{}, [], false],
  ])("%o", (prop, types, nullable) => {
    expect(schemaTypes(prop)).toEqual({ types, nullable });
  });
});

describe("kindOf", () => {
  it.each([
    [{ type: "string" }, "string"],
    [{ type: ["string", "null"] }, "string"],
    [{ type: "integer" }, "integer"],
    [{ type: ["integer", "null"] }, "integer"],
    [{ type: "number" }, "number"],
    [{ type: "boolean" }, "boolean"],
    [{ type: "string", enum: ["a", "b"] }, "enum"],
    [{ type: "integer", enum: [1, 7] }, "enum"],
    [{ enum: ["Allow", "Deny"] }, "enum"],
    [{ type: "array", items: { type: "string" } }, "string-list"],
    [{ type: "array", items: { type: "object" } }, "json"],
    [{ type: "object", additionalProperties: { type: "string" } }, "key-value"],
    [{ type: ["object", "null"], properties: { a: {} } }, "json"],
    [{ type: ["string", "integer"] }, "json"],
    [{}, "json"],
  ])("%o -> %s", (prop, want) => expect(kindOf(prop)).toBe(want));
});

describe("toField", () => {
  it("captures constraints, group and flags", () => {
    const f = toField(
      "kms_key_arn",
      { type: ["string", "null"], default: null, pattern: "^arn:", "x-iacp-group": "Encryption", "x-iacp-immutable": true },
      false,
    );
    expect(f).toMatchObject({
      name: "kms_key_arn",
      pointer: "/kms_key_arn",
      label: "Kms key arn",
      kind: "string",
      typeLabel: "string | null",
      required: false,
      nullable: true,
      hasDefault: true,
      default: null,
      pattern: "^arn:",
      group: "Encryption",
      immutable: true,
    });
  });

  it("distinguishes no default from a null default", () => {
    expect(toField("a", { type: "string" }, true).hasDefault).toBe(false);
    expect(toField("a", { type: ["string", "null"], default: null }, false).hasDefault).toBe(true);
  });
});

describe("toFieldModels", () => {
  it("orders General first, required first, then by name", () => {
    expect(toFieldModels(sqsSchema).map((f) => `${f.group}:${f.name}`)).toEqual([
      "General:name",
      "General:fifo",
      "General:tags",
      "General:visibility_timeout_seconds",
      "Alarms:alarm_actions",
      "Dead-letter queue:max_receive_count",
      "Encryption:kms_key_arn",
    ]);
  });

  it("maps every lambda property to a control", () => {
    const kinds = Object.fromEntries(toFieldModels(lambdaSchema).map((f) => [f.name, f.kind]));
    expect(kinds).toEqual({
      name: "string",
      runtime: "enum",
      reserved_concurrent_executions: "integer",
      artifact_s3_uri: "string",
      log_retention_days: "enum",
      sqs_trigger: "json",
    });
  });

  it.each([[null], [undefined], [{}], [{ properties: [] }]])("returns no fields for %o", (schema) => {
    expect(toFieldModels(schema)).toEqual([]);
  });
});

describe("groupFields", () => {
  it("buckets consecutive fields by group", () => {
    const groups = groupFields(toFieldModels(sqsSchema));
    expect(groups.map((g) => [g.name, g.fields.length])).toEqual([
      [DEFAULT_GROUP, 4],
      ["Alarms", 1],
      ["Dead-letter queue", 1],
      ["Encryption", 1],
    ]);
  });
});

describe("display helpers", () => {
  const fields = Object.fromEntries(toFieldModels(sqsSchema).map((f) => [f.name, f]));

  it("formats defaults", () => {
    expect(formatDefault(fields.name!)).toBe("required");
    expect(formatDefault(fields.tags!)).toBe("—");
    expect(formatDefault(fields.kms_key_arn!)).toBe("null");
    expect(formatDefault(fields.visibility_timeout_seconds!)).toBe("30");
  });

  it("summarises constraints", () => {
    expect(constraintsOf(fields.visibility_timeout_seconds!)).toEqual(["0–43200"]);
    expect(constraintsOf(fields.name!)).toEqual(["pattern ^[a-z][a-z0-9-]{2,40}$", "immutable after create"]);
    expect(constraintsOf(fields.alarm_actions!)).toEqual(["items match ^arn:"]);
    expect(constraintsOf(toField("x", { type: "integer", minimum: 5 }, false))).toEqual(["≥ 5"]);
    expect(constraintsOf(toField("x", { enum: ["a", "b"] }, false))).toEqual(["one of a, b"]);
  });
});
