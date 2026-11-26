// Pure translation of a module version's inputs JSON Schema (draft 2020-12, docs/04) into a
// flat field model used by both the inputs table and the request form.
//
// Module schemas are an object of top-level properties. Scalars, enums, string lists and
// string maps get dedicated controls; anything richer (nested objects, arrays of objects,
// conditionals) falls back to a JSON editor. The backend validates the full schema again on
// POST /v1/requests, so the client checks are a convenience, not the authority.

import type { JsonSchema } from "@/lib/api/types";

export type FieldKind = "string" | "integer" | "number" | "boolean" | "enum" | "string-list" | "key-value" | "json";

export type EnumValue = string | number | boolean;

export interface FieldModel {
  name: string;
  /** RFC 6901 pointer relative to the inputs object. */
  pointer: string;
  label: string;
  kind: FieldKind;
  /** Short type description for tables, e.g. "integer | null", "list of string". */
  typeLabel: string;
  required: boolean;
  nullable: boolean;
  description?: string;
  hasDefault: boolean;
  default?: unknown;
  enumValues?: readonly EnumValue[];
  pattern?: string;
  itemPattern?: string;
  minimum?: number;
  maximum?: number;
  /** JSON Schema 2020-12 exclusive bounds (numbers, not the draft-04 booleans). */
  exclusiveMinimum?: number;
  exclusiveMaximum?: number;
  minLength?: number;
  maxLength?: number;
  group: string;
  immutable: boolean;
  /** x-iacp-ui hint from module.yaml (select, key-value, danger-toggle, stack-output, …). */
  ui?: string;
}

export const DEFAULT_GROUP = "General";

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

const num = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) ? v : undefined);
const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);

/** escapePointerToken applies RFC 6901 escaping (~ → ~0, / → ~1). Pure. */
export const escapePointerToken = (t: string): string => t.replace(/~/g, "~0").replace(/\//g, "~1");

/** humanize turns snake_case into a sentence-case label. Pure. */
export function humanize(name: string): string {
  const words = name.split(/[_\s]+/).filter(Boolean);
  if (words.length === 0) return name;
  const text = words.join(" ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** schemaTypes lists the JSON types a property allows, with "null" split out. Pure. */
export function schemaTypes(prop: Record<string, unknown>): { types: string[]; nullable: boolean } {
  const raw = prop.type;
  let list: string[] = Array.isArray(raw) ? raw.filter((t): t is string => typeof t === "string") : typeof raw === "string" ? [raw] : [];
  if (list.length === 0 && Array.isArray(prop.enum)) {
    list = [...new Set(prop.enum.map((v) => (v === null ? "null" : typeof v === "number" ? "number" : typeof v)))];
  }
  return { types: list.filter((t) => t !== "null"), nullable: list.includes("null") };
}

function isStringMap(prop: Record<string, unknown>): boolean {
  return isRecord(prop.additionalProperties) && prop.additionalProperties.type === "string" && !isRecord(prop.properties);
}

function isStringList(prop: Record<string, unknown>): boolean {
  return isRecord(prop.items) && prop.items.type === "string" && !Array.isArray(prop.items.enum);
}

/** kindOf picks the control for a property. Pure. */
export function kindOf(prop: Record<string, unknown>): FieldKind {
  const { types } = schemaTypes(prop);
  if (Array.isArray(prop.enum) && prop.enum.some((v) => v !== null)) return "enum";
  if (types.length !== 1) return "json";
  switch (types[0]) {
    case "string":
      return "string";
    case "integer":
      return "integer";
    case "number":
      return "number";
    case "boolean":
      return "boolean";
    case "array":
      return isStringList(prop) ? "string-list" : "json";
    case "object":
      return isStringMap(prop) ? "key-value" : "json";
    default:
      return "json";
  }
}

function typeLabelOf(prop: Record<string, unknown>, kind: FieldKind): string {
  const { types, nullable } = schemaTypes(prop);
  let base: string;
  switch (kind) {
    case "enum":
      base = "enum";
      break;
    case "string-list":
      base = "list of string";
      break;
    case "key-value":
      base = "map of string";
      break;
    default:
      base = types.length > 0 ? types.join(" | ") : "any";
  }
  return nullable ? `${base} | null` : base;
}

/** toField converts one property schema. Pure. */
export function toField(name: string, prop: Record<string, unknown>, required: boolean): FieldModel {
  const kind = kindOf(prop);
  const { nullable } = schemaTypes(prop);
  const enumValues = Array.isArray(prop.enum)
    ? prop.enum.filter((v): v is EnumValue => typeof v === "string" || typeof v === "number" || typeof v === "boolean")
    : undefined;
  return {
    name,
    pointer: `/${escapePointerToken(name)}`,
    label: humanize(name),
    kind,
    typeLabel: typeLabelOf(prop, kind),
    required,
    nullable,
    description: str(prop.description),
    hasDefault: Object.hasOwn(prop, "default"),
    default: prop.default,
    enumValues,
    pattern: str(prop.pattern),
    itemPattern: isRecord(prop.items) ? str(prop.items.pattern) : undefined,
    minimum: num(prop.minimum),
    maximum: num(prop.maximum),
    exclusiveMinimum: num(prop.exclusiveMinimum),
    exclusiveMaximum: num(prop.exclusiveMaximum),
    minLength: num(prop.minLength),
    maxLength: num(prop.maxLength),
    group: str(prop["x-iacp-group"]) ?? DEFAULT_GROUP,
    immutable: prop["x-iacp-immutable"] === true,
    ui: str(prop["x-iacp-ui"]),
  };
}

/**
 * toFieldModels flattens the top-level properties of an inputs schema. Key order is not
 * reliable after a round trip through jsonb, so fields are ordered deterministically: the
 * General group first, then other groups alphabetically; required fields first within a
 * group, then by name. Pure.
 */
export function toFieldModels(schema: JsonSchema | null | undefined): FieldModel[] {
  if (!isRecord(schema) || !isRecord(schema.properties)) return [];
  const required = new Set(Array.isArray(schema.required) ? schema.required.filter((r) => typeof r === "string") : []);
  const fields = Object.entries(schema.properties)
    .filter((entry): entry is [string, Record<string, unknown>] => isRecord(entry[1]))
    .map(([name, prop]) => toField(name, prop, required.has(name)));
  const groupRank = (g: string) => (g === DEFAULT_GROUP ? "" : g);
  return fields.toSorted(
    (a, b) =>
      groupRank(a.group).localeCompare(groupRank(b.group)) ||
      Number(b.required) - Number(a.required) ||
      a.name.localeCompare(b.name),
  );
}

export interface FieldGroup {
  name: string;
  fields: FieldModel[];
}

/** groupFields keeps the order of toFieldModels and buckets by group. Pure. */
export function groupFields(fields: readonly FieldModel[]): FieldGroup[] {
  const groups: FieldGroup[] = [];
  for (const f of fields) {
    const last = groups.at(-1);
    if (last && last.name === f.group) last.fields.push(f);
    else groups.push({ name: f.group, fields: [f] });
  }
  return groups;
}

/** formatDefault renders a default value for display. Pure. */
export function formatDefault(field: FieldModel): string {
  if (!field.hasDefault) return field.required ? "required" : "—";
  return JSON.stringify(field.default);
}

/** constraintsOf summarises validation keywords for display. Pure. */
export function constraintsOf(field: FieldModel): string[] {
  const out: string[] = [];
  if (field.minimum !== undefined && field.maximum !== undefined) out.push(`${field.minimum}–${field.maximum}`);
  else if (field.minimum !== undefined) out.push(`≥ ${field.minimum}`);
  else if (field.maximum !== undefined) out.push(`≤ ${field.maximum}`);
  if (field.exclusiveMinimum !== undefined) out.push(`> ${field.exclusiveMinimum}`);
  if (field.exclusiveMaximum !== undefined) out.push(`< ${field.exclusiveMaximum}`);
  if (field.minLength !== undefined) out.push(`min length ${field.minLength}`);
  if (field.maxLength !== undefined) out.push(`max length ${field.maxLength}`);
  if (field.pattern) out.push(`pattern ${field.pattern}`);
  if (field.itemPattern) out.push(`items match ${field.itemPattern}`);
  if (field.enumValues && field.enumValues.length > 0) out.push(`one of ${field.enumValues.map(String).join(", ")}`);
  if (field.immutable) out.push("immutable after create");
  return out;
}
