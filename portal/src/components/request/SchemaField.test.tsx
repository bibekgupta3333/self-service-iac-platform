import { describe, expect, it } from "vitest";

import { toField } from "@/lib/schema/fields";

import { hintFor } from "./SchemaField";

describe("hintFor", () => {
  it.each([
    [toField("l", { type: "array", items: { type: "string" }, default: ["a"] }, false), "One value per line. Clear to send an empty value."],
    [toField("l", { type: "array", items: { type: "string" }, default: [] }, false), "One value per line."],
    [toField("t", { type: "object", additionalProperties: { type: "string" } }, false), "One key=value pair per line. Leave empty to leave it unset."],
    [toField("i", { type: "integer", default: 3 }, false), "Leave empty to use the module default."],
    [toField("z", { type: ["integer", "null"], default: 10 }, false), "Clear to set null."],
    [toField("k", { type: ["string", "null"], default: null }, false), undefined],
    [toField("b", { type: "boolean", default: false }, false), undefined],
    [toField("n", { type: "string" }, true), undefined],
  ])("%#", (field, want) => expect(hintFor(field)).toBe(want));
});
