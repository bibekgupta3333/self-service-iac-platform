"use client";

import type { FieldModel } from "@/lib/schema/fields";
import type { FieldValue } from "@/lib/schema/values";

const CONTROL =
  "mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm aria-[invalid=true]:border-red-600";

export interface SchemaFieldProps {
  id: string;
  field: FieldModel;
  value: FieldValue;
  errors?: readonly string[];
  onChange: (name: string, value: FieldValue) => void;
}

function hintFor(field: FieldModel): string | undefined {
  switch (field.kind) {
    case "string-list":
      return "One value per line.";
    case "key-value":
      return "One key=value pair per line.";
    case "json":
      return "JSON value. Leave empty to use the module default.";
    default:
      return field.nullable && field.default !== null && field.hasDefault ? "Clear to set null." : undefined;
  }
}

/** SchemaField renders the control for one module input with its label, help and errors. */
export function SchemaField({ id, field, value, errors = [], onChange }: SchemaFieldProps) {
  const descId = `${id}-desc`;
  const errId = `${id}-error`;
  const hint = hintFor(field);
  const describedBy = [field.description || hint ? descId : "", errors.length > 0 ? errId : ""].filter(Boolean).join(" ") || undefined;
  const invalid = errors.length > 0;
  const common = {
    id,
    name: field.name,
    "aria-describedby": describedBy,
    "aria-invalid": invalid,
    required: field.required,
  };
  const text = typeof value === "string" ? value : "";

  let control;
  switch (field.kind) {
    case "boolean":
      control = (
        <input
          {...common}
          required={false}
          type="checkbox"
          checked={value === true}
          onChange={(e) => onChange(field.name, e.target.checked)}
          className="h-4 w-4 rounded border-slate-400"
        />
      );
      break;
    case "enum":
      control = (
        <select {...common} value={text} onChange={(e) => onChange(field.name, e.target.value)} className={CONTROL}>
          {(!field.required || !field.hasDefault) && <option value="">{field.required ? "Select…" : "Module default"}</option>}
          {field.enumValues?.map((v) => (
            <option key={String(v)} value={String(v)}>
              {String(v)}
            </option>
          ))}
        </select>
      );
      break;
    case "integer":
    case "number":
      control = (
        <input
          {...common}
          type="number"
          inputMode={field.kind === "integer" ? "numeric" : "decimal"}
          step={field.kind === "integer" ? 1 : "any"}
          min={field.minimum}
          max={field.maximum}
          value={text}
          onChange={(e) => onChange(field.name, e.target.value)}
          className={CONTROL}
        />
      );
      break;
    case "string-list":
    case "key-value":
    case "json":
      control = (
        <textarea
          {...common}
          rows={field.kind === "json" ? 5 : 3}
          spellCheck={false}
          value={text}
          onChange={(e) => onChange(field.name, e.target.value)}
          className={`${CONTROL} font-mono`}
        />
      );
      break;
    default:
      control = (
        <input
          {...common}
          type="text"
          autoComplete="off"
          spellCheck={false}
          value={text}
          onChange={(e) => onChange(field.name, e.target.value)}
          className={CONTROL}
        />
      );
  }

  const label = (
    <label htmlFor={id} className="text-sm font-medium text-slate-800">
      <span className="font-mono">{field.name}</span>
      {field.required && (
        <span className="ml-1 text-red-700" aria-hidden="true">
          *
        </span>
      )}
      {field.immutable && <span className="ml-2 text-xs font-normal text-amber-800">immutable after create</span>}
    </label>
  );

  return (
    <div className={field.ui === "danger-toggle" ? "rounded-md border border-red-200 bg-red-50/40 p-3" : undefined}>
      {field.kind === "boolean" ? (
        <div className="flex items-center gap-2">
          {control}
          {label}
        </div>
      ) : (
        <>
          {label}
          {control}
        </>
      )}
      {(field.description || hint) && (
        <p id={descId} className="mt-1 text-xs text-slate-600">
          {[field.description, hint].filter(Boolean).join(" ")}
        </p>
      )}
      {invalid && (
        <ul id={errId} className="mt-1 text-xs text-red-700">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
