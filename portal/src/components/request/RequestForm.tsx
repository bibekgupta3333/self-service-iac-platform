"use client";

import { useRouter } from "next/navigation";
import { type FormEvent, useMemo, useRef, useState } from "react";

import type { ApiClient } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import { browserApi } from "@/lib/api/factory";
import type { ModuleVersion, RequestSummary } from "@/lib/api/types";
import {
  buildCreatePayload,
  ENVIRONMENTS,
  fieldId,
  type FormErrors,
  type FormKey,
  hasErrors,
  inputErrors,
  mapFieldErrors,
  MAX_JUSTIFICATION,
  type StackFields,
  validateStackFields,
} from "@/lib/request/form";
import { SchemaAdapter } from "@/lib/schema/adapter";
import type { FieldValue } from "@/lib/schema/values";

import { SchemaField } from "./SchemaField";
import { TextField } from "./TextField";

export interface RequestFormProps {
  version: Pick<ModuleVersion, "module" | "version" | "inputs_schema">;
  /** Injected for tests; defaults to the browser ApiClient. */
  client?: Pick<ApiClient, "createRequest">;
  /** Injected for tests; defaults to crypto.randomUUID. */
  newIdempotencyKey?: () => string;
  /** Called after the request is accepted; defaults to navigating to its page. */
  onCreated?: (request: RequestSummary) => void;
}

interface Failure {
  title: string;
  detail?: string;
  requestId?: string;
  general: string[];
}

const STACK_LABELS: Record<keyof StackFields, string> = {
  team: "Team",
  environment: "Environment",
  stack: "Stack name",
  resourceName: "Resource name",
  justification: "Justification",
};

const labelFor = (key: FormKey): string =>
  key.startsWith("input:") ? key.slice("input:".length) : STACK_LABELS[key as keyof StackFields];

export function RequestForm({ version, client, newIdempotencyKey, onCreated }: RequestFormProps) {
  const router = useRouter();
  const adapter = useMemo(() => new SchemaAdapter(version), [version]);
  // One key per form session, created on the first submit (not during render, so server and
  // client markup match). Retries after a network error or a validation failure reuse it, so a
  // request that did reach the backend is never created twice (docs/03 §5).
  const [idempotencyKey, setIdempotencyKey] = useState<string | null>(null);
  const [stack, setStack] = useState<StackFields>(() => ({
    team: "",
    environment: "development",
    stack: "",
    resourceName: adapter.defaultResourceName(),
    justification: "",
  }));
  const [values, setValues] = useState<Record<string, FieldValue>>(() => adapter.initialValues());
  const [errors, setErrors] = useState<FormErrors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const summaryRef = useRef<HTMLDivElement>(null);

  const { inputs, errors: inputProblems } = adapter.toInputs(values);
  const payload = buildCreatePayload(stack, adapter.module, adapter.version, inputs);

  const setStackField = (key: keyof StackFields) => (value: string) => setStack((s) => ({ ...s, [key]: value }));
  const setValue = (name: string, value: FieldValue) => setValues((v) => ({ ...v, [name]: value }));

  const showProblems = (next: FormErrors, nextFailure: Failure | null) => {
    setErrors(next);
    setFailure(nextFailure);
    // Let React render the summary before moving focus to it.
    queueMicrotask(() => summaryRef.current?.focus());
  };

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const clientErrors = { ...validateStackFields(stack), ...inputErrors(inputProblems) };
    if (hasErrors(clientErrors)) {
      showProblems(clientErrors, null);
      return;
    }
    const key = idempotencyKey ?? (newIdempotencyKey ?? (() => crypto.randomUUID()))();
    if (idempotencyKey === null) setIdempotencyKey(key);
    setSubmitting(true);
    setErrors({});
    setFailure(null);
    try {
      const { request } = await (client ?? browserApi()).createRequest(payload, key);
      if (onCreated) onCreated(request);
      else router.push(`/requests/${encodeURIComponent(request.id)}`);
    } catch (err) {
      if (isApiError(err)) {
        const mapped = mapFieldErrors(err.fieldErrors);
        showProblems(mapped.fields, {
          title: err.problem.title,
          detail: err.problem.detail,
          requestId: err.requestId,
          general: mapped.general,
        });
      } else {
        showProblems({}, { title: "Request failed", detail: String(err), general: [] });
      }
    } finally {
      setSubmitting(false);
    }
  }

  const errorEntries = (Object.entries(errors) as Array<[FormKey, string[] | undefined]>).filter(
    ([, msgs]) => (msgs?.length ?? 0) > 0,
  );
  const showSummary = failure !== null || errorEntries.length > 0;

  return (
    <form onSubmit={onSubmit} noValidate className="space-y-8" aria-describedby="form-intro">
      <p id="form-intro" className="text-sm text-slate-600">
        Fields marked <span className="text-red-700">*</span> are required. Inputs left at their default are not sent.
      </p>

      {showSummary && (
        <div
          ref={summaryRef}
          tabIndex={-1}
          role="alert"
          aria-labelledby="error-summary-title"
          className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900"
        >
          <p id="error-summary-title" className="font-medium">
            {failure ? failure.title : "Please fix the highlighted fields"}
          </p>
          {failure?.detail && <p className="mt-1">{failure.detail}</p>}
          {(errorEntries.length > 0 || (failure?.general.length ?? 0) > 0) && (
            <ul className="mt-2 list-disc space-y-1 pl-5">
              {errorEntries.map(([key, msgs]) => (
                <li key={key}>
                  <a href={`#${fieldId(key)}`} className="underline">
                    {labelFor(key)}
                  </a>
                  : {msgs?.join("; ")}
                </li>
              ))}
              {failure?.general.map((g) => (
                <li key={g}>{g}</li>
              ))}
            </ul>
          )}
          {failure?.requestId && (
            <p className="mt-2 text-xs">
              Request ID: <code className="font-mono">{failure.requestId}</code>
            </p>
          )}
        </div>
      )}

      <fieldset className="space-y-4 rounded-lg border border-slate-200 bg-white p-5">
        <legend className="px-1 text-base font-semibold">Stack</legend>
        <div className="grid gap-4 sm:grid-cols-3">
          <TextField
            id={fieldId("team")}
            label="Team"
            required
            value={stack.team}
            onChange={setStackField("team")}
            errors={errors.team}
            hint="Team slug, e.g. payments. You need the developer role in it."
          />
          <TextField
            id={fieldId("environment")}
            label="Environment"
            required
            value={stack.environment}
            onChange={setStackField("environment")}
            errors={errors.environment}
          >
            {ENVIRONMENTS.map((env) => (
              <option key={env} value={env}>
                {env}
              </option>
            ))}
          </TextField>
          <TextField
            id={fieldId("stack")}
            label="Stack name"
            required
            value={stack.stack}
            onChange={setStackField("stack")}
            errors={errors.stack}
            hint="Becomes environments/<env>/<team>/<stack>."
          />
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          <TextField
            id={fieldId("resourceName")}
            label="Resource name"
            required
            value={stack.resourceName}
            onChange={setStackField("resourceName")}
            errors={errors.resourceName}
            hint="Terraform module block name inside the stack."
          />
          <div className="sm:col-span-2">
            <TextField
              id={fieldId("justification")}
              label="Justification"
              multiline
              value={stack.justification}
              onChange={setStackField("justification")}
              errors={errors.justification}
              hint={`Optional, shown on the pull request (max ${MAX_JUSTIFICATION} characters).`}
            />
          </div>
        </div>
      </fieldset>

      {adapter.groups().map((group) => (
        <fieldset key={group.name} className="space-y-4 rounded-lg border border-slate-200 bg-white p-5">
          <legend className="px-1 text-base font-semibold">
            {group.name === "General" ? `${adapter.module} inputs` : group.name}
          </legend>
          <div className="grid gap-4 sm:grid-cols-2">
            {group.fields.map((f) => (
              <SchemaField
                key={f.name}
                id={fieldId(`input:${f.name}`)}
                field={f}
                value={values[f.name] ?? ""}
                errors={errors[`input:${f.name}`]}
                onChange={setValue}
              />
            ))}
          </div>
        </fieldset>
      ))}

      <details className="rounded-lg border border-slate-200 bg-white p-5">
        <summary className="cursor-pointer text-base font-semibold">Preview request</summary>
        <p className="mt-2 text-sm text-slate-600">
          The exact call the portal will make.
          {Object.keys(inputProblems).length > 0 && " Inputs with invalid values are left out until fixed."}
        </p>
        <pre className="mt-3 overflow-x-auto rounded-md bg-slate-900 p-4 text-xs text-slate-100">
          <code>
            {`POST /v1/requests\nIdempotency-Key: ${idempotencyKey ?? "<a UUID created on the first submit and reused for retries>"}\nContent-Type: application/json\n\n${JSON.stringify(payload, null, 2)}`}
          </code>
        </pre>
        <p className="mt-2 text-xs text-slate-500">
          A preview of the generated Terraform files needs the backend&apos;s dry-run render endpoint (
          <code className="font-mono">POST /v1/requests/{"{id}"}/preview</code>), which is not built yet.
        </p>
      </details>

      <div className="flex items-center gap-3">
        <button
          type="submit"
          disabled={submitting}
          className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-60"
        >
          {submitting ? "Submitting…" : "Submit request"}
        </button>
        <span className="text-xs text-slate-500" aria-live="polite">
          {submitting ? "Sending the request to the platform." : ""}
        </span>
      </div>
    </form>
  );
}
