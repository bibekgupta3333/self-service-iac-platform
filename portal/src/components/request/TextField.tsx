"use client";

import type { ReactNode } from "react";

export interface TextFieldProps {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  errors?: readonly string[];
  hint?: ReactNode;
  required?: boolean;
  multiline?: boolean;
  children?: ReactNode;
}

const CONTROL =
  "mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm aria-[invalid=true]:border-red-600";

/** TextField is a labelled text input (or a select when children are options). */
export function TextField({ id, label, value, onChange, errors = [], hint, required, multiline, children }: TextFieldProps) {
  const invalid = errors.length > 0;
  const describedBy = [hint ? `${id}-desc` : "", invalid ? `${id}-error` : ""].filter(Boolean).join(" ") || undefined;
  const common = { id, required, "aria-invalid": invalid, "aria-describedby": describedBy, className: CONTROL };
  return (
    <div>
      <label htmlFor={id} className="text-sm font-medium text-slate-800">
        {label}
        {required && (
          <span className="ml-1 text-red-700" aria-hidden="true">
            *
          </span>
        )}
      </label>
      {children ? (
        <select {...common} value={value} onChange={(e) => onChange(e.target.value)}>
          {children}
        </select>
      ) : multiline ? (
        <textarea {...common} rows={3} value={value} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <input
          {...common}
          type="text"
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {hint && (
        <p id={`${id}-desc`} className="mt-1 text-xs text-slate-600">
          {hint}
        </p>
      )}
      {invalid && (
        <ul id={`${id}-error`} className="mt-1 text-xs text-red-700">
          {errors.map((e) => (
            <li key={e}>{e}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
