// Pure conversion between form control values and the module `inputs` object.
//
// Controls hold text (numbers, lists and maps are edited as text) or booleans. toInputs
// parses them back, applies light client-side checks and leaves out every value the user
// did not change from the module default, so the request carries only deliberate choices.

import type { FieldModel } from "./fields";

export type FieldValue = string | boolean;
export type FormValues = Readonly<Record<string, FieldValue>>;

/** toText renders a default as the control's text. Pure. */
export function toText(field: FieldModel, value: unknown): FieldValue {
  if (field.kind === "boolean") return value === true;
  if (value === undefined || value === null) return "";
  switch (field.kind) {
    case "string-list":
      return Array.isArray(value) ? value.map(String).join("\n") : "";
    case "key-value":
      return typeof value === "object"
        ? Object.entries(value as Record<string, unknown>)
            .map(([k, v]) => `${k}=${String(v)}`)
            .join("\n")
        : "";
    case "json":
      return JSON.stringify(value, null, 2);
    default:
      return String(value);
  }
}

/** initialValues seeds every control from the schema default. Pure. */
export function initialValues(fields: readonly FieldModel[]): Record<string, FieldValue> {
  return Object.fromEntries(fields.map((f) => [f.name, toText(f, f.default)]));
}

type Parsed = { ok: true; value: unknown; empty: boolean } | { ok: false; error: string };

const ok = (value: unknown, empty = false): Parsed => ({ ok: true, value, empty });
const fail = (error: string): Parsed => ({ ok: false, error });

const lines = (s: string): string[] =>
  s
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

function matches(pattern: string | undefined, s: string): boolean {
  if (!pattern) return true;
  try {
    return new RegExp(pattern, "u").test(s);
  } catch {
    return true; // an unsupported pattern is left to the backend
  }
}

function checkRange(field: FieldModel, n: number): string | undefined {
  if (field.minimum !== undefined && n < field.minimum) return `must be at least ${field.minimum}`;
  if (field.maximum !== undefined && n > field.maximum) return `must be at most ${field.maximum}`;
  if (field.exclusiveMinimum !== undefined && n <= field.exclusiveMinimum)
    return `must be greater than ${field.exclusiveMinimum}`;
  if (field.exclusiveMaximum !== undefined && n >= field.exclusiveMaximum)
    return `must be less than ${field.exclusiveMaximum}`;
  return undefined;
}

/** parseValue converts one control value. Pure. */
export function parseValue(field: FieldModel, raw: FieldValue): Parsed {
  if (field.kind === "boolean") return ok(raw === true);
  const text = typeof raw === "string" ? raw : String(raw);
  const trimmed = text.trim();
  if (trimmed === "") return ok(undefined, true);

  switch (field.kind) {
    case "string": {
      if (field.minLength !== undefined && trimmed.length < field.minLength)
        return fail(`must be at least ${field.minLength} characters`);
      if (field.maxLength !== undefined && trimmed.length > field.maxLength)
        return fail(`must be at most ${field.maxLength} characters`);
      return matches(field.pattern, trimmed) ? ok(trimmed) : fail(`must match ${field.pattern}`);
    }
    case "integer": {
      if (!/^-?\d+$/.test(trimmed)) return fail("must be a whole number");
      const n = Number(trimmed);
      const e = checkRange(field, n);
      return e ? fail(e) : ok(n);
    }
    case "number": {
      const n = Number(trimmed);
      if (!Number.isFinite(n)) return fail("must be a number");
      const e = checkRange(field, n);
      return e ? fail(e) : ok(n);
    }
    case "enum": {
      const match = field.enumValues?.find((v) => String(v) === trimmed);
      return match === undefined ? fail(`must be one of ${field.enumValues?.join(", ")}`) : ok(match);
    }
    case "string-list": {
      const items = lines(text);
      const bad = items.find((i) => !matches(field.itemPattern, i));
      return bad === undefined ? ok(items) : fail(`"${bad}" must match ${field.itemPattern}`);
    }
    case "key-value": {
      const out: Record<string, string> = {};
      for (const line of lines(text)) {
        const i = line.indexOf("=");
        if (i <= 0) return fail(`"${line}" must look like key=value`);
        out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
      }
      return ok(out);
    }
    case "json":
      try {
        return ok(JSON.parse(trimmed) as unknown);
      } catch {
        return fail("must be valid JSON");
      }
  }
}

const sameValue = (a: unknown, b: unknown): boolean => JSON.stringify(a) === JSON.stringify(b);

export interface InputsResult {
  inputs: Record<string, unknown>;
  /** Client-side problems keyed by field name. */
  errors: Record<string, string>;
}

/**
 * toInputs builds the `inputs` object for a resource. Required fields are always sent;
 * optional fields are sent only when they differ from the schema default. Pure.
 */
export function toInputs(fields: readonly FieldModel[], values: FormValues): InputsResult {
  const inputs: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  for (const f of fields) {
    const parsed = parseValue(f, values[f.name] ?? toText(f, f.default));
    if (!parsed.ok) {
      errors[f.name] = parsed.error;
      continue;
    }
    if (parsed.empty) {
      if (f.required) errors[f.name] = "is required";
      // Clearing a nullable field whose default is not null means "null" (e.g. unreserved
      // Lambda concurrency); otherwise the module default (often null) applies.
      else if (f.nullable && f.hasDefault && f.default !== null) inputs[f.name] = null;
      continue;
    }
    if (!f.required && f.hasDefault && sameValue(parsed.value, f.default)) continue;
    if (!f.required && !f.hasDefault && f.kind === "boolean" && parsed.value === false) continue;
    inputs[f.name] = parsed.value;
  }
  return { inputs, errors };
}
