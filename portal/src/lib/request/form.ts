// Pure logic behind the request form: stack-level checks, payload building and mapping the
// backend's pointer-addressed validation errors back onto form fields.

import type { FieldError } from "@/lib/api/errors";
import type { CreateRequestInput } from "@/lib/api/types";

export const ENVIRONMENTS = ["development", "staging", "production"] as const;

export interface StackFields {
  team: string;
  environment: string;
  stack: string;
  resourceName: string;
  justification: string;
}

/** Keys of form controls that errors can attach to. Module inputs use `input:<name>`. */
export type FormKey = keyof StackFields | `input:${string}`;

export type FormErrors = Partial<Record<FormKey, string[]>>;

export interface MappedErrors {
  fields: FormErrors;
  /** Errors that do not belong to one control (kind, module/version, resources list). */
  general: string[];
}

// The same rules as backend/internal/requests ValidateShape; the backend stays authoritative.
const TEAM_RE = /^[a-z][a-z0-9-]{1,38}$/;
const STACK_RE = /^[a-z][a-z0-9-]{1,40}$/;
const RESOURCE_RE = /^[a-z][a-z0-9_]{0,62}$/;
export const MAX_JUSTIFICATION = 2000;

/** validateStackFields checks the non-schema fields. Pure. */
export function validateStackFields(f: StackFields): FormErrors {
  const errors: FormErrors = {};
  if (!TEAM_RE.test(f.team.trim())) errors.team = ["must be a team slug: lowercase letters, digits and dashes, 2-39 characters"];
  if (!f.environment.trim()) errors.environment = ["is required"];
  if (!STACK_RE.test(f.stack.trim()))
    errors.stack = ["must start with a letter and use lowercase letters, digits and dashes (2-41 characters)"];
  if (!RESOURCE_RE.test(f.resourceName.trim()))
    errors.resourceName = ["must start with a letter and use lowercase letters, digits and underscores"];
  if (f.justification.length > MAX_JUSTIFICATION) errors.justification = [`must be at most ${MAX_JUSTIFICATION} characters`];
  return errors;
}

/** buildCreatePayload assembles the POST /v1/requests body for one resource. Pure. */
export function buildCreatePayload(
  f: StackFields,
  module: string,
  version: string,
  inputs: Record<string, unknown>,
): CreateRequestInput {
  const justification = f.justification.trim();
  return {
    kind: "create",
    team: f.team.trim(),
    environment: f.environment.trim(),
    stack: f.stack.trim(),
    ...(justification ? { justification } : {}),
    resources: [{ name: f.resourceName.trim(), module, version, inputs }],
  };
}

/** decodePointer splits an RFC 6901 pointer into unescaped tokens. Pure. */
export function decodePointer(pointer: string): string[] {
  if (pointer === "" || pointer === "/") return [];
  return pointer
    .replace(/^\//, "")
    .split("/")
    .map((t) => t.replace(/~1/g, "/").replace(/~0/g, "~"));
}

const TOP_LEVEL: Partial<Record<string, keyof StackFields>> = {
  team: "team",
  environment: "environment",
  stack: "stack",
  justification: "justification",
};

/** keyForPointer returns the form control a pointer addresses and any remaining path. Pure. */
export function keyForPointer(pointer: string, resourceIndex = 0): { key: FormKey; rest: string[] } | undefined {
  const tokens = decodePointer(pointer);
  const [head, index, field, inputName, ...rest] = tokens;
  if (head === undefined) return undefined;
  const top = TOP_LEVEL[head];
  if (top) return { key: top, rest: tokens.slice(1) };
  if (head !== "resources" || index !== String(resourceIndex)) return undefined;
  if (field === "name" && inputName === undefined) return { key: "resourceName", rest: [] };
  if (field === "inputs" && inputName !== undefined) return { key: `input:${inputName}`, rest };
  return undefined;
}

const append = (errors: FormErrors, key: FormKey, msg: string): FormErrors => ({
  ...errors,
  [key]: [...(errors[key] ?? []), msg],
});

/**
 * mapFieldErrors distributes the backend's field errors over form controls. A deeper path
 * inside an input (e.g. /resources/0/inputs/tags/env) is kept as a prefix of the message.
 * Pure.
 */
export function mapFieldErrors(errors: readonly FieldError[], resourceIndex = 0): MappedErrors {
  return errors.reduce<MappedErrors>(
    (acc, e) => {
      const target = keyForPointer(e.pointer, resourceIndex);
      if (!target) return { ...acc, general: [...acc.general, e.pointer ? `${e.pointer}: ${e.message}` : e.message] };
      const msg = target.rest.length > 0 ? `${target.rest.join("/")}: ${e.message}` : e.message;
      return { ...acc, fields: append(acc.fields, target.key, msg) };
    },
    { fields: {}, general: [] },
  );
}

/** inputErrors lifts SchemaAdapter.toInputs errors (by input name) into form keys. Pure. */
export function inputErrors(byName: Readonly<Record<string, string>>): FormErrors {
  return Object.fromEntries(Object.entries(byName).map(([name, msg]) => [`input:${name}`, [msg]]));
}

/** hasErrors reports whether any control has an error. Pure. */
export const hasErrors = (e: FormErrors): boolean => Object.values(e).some((v) => (v?.length ?? 0) > 0);

/** fieldId is the DOM id of the control for key, used by labels and the error summary. Pure. */
export const fieldId = (key: FormKey): string =>
  key.startsWith("input:") ? `field-input-${key.slice("input:".length)}` : `field-${key}`;
