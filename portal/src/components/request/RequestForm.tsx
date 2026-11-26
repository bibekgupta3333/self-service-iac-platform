"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";

import type { ApiClient } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import { browserApi } from "@/lib/api/factory";
import type { ModuleVersion, RequestSummary, ResourceInput, StackSummary } from "@/lib/api/types";
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
  validateJustification,
  validateStackFields,
} from "@/lib/request/form";
import { classifyFailure, type FailureKind, type KeyedPayload, keyFor } from "@/lib/request/idempotency";
import { buildUpdatePayload, explainLifecycleError, removedResourceNames } from "@/lib/request/lifecycle";
import { SchemaAdapter } from "@/lib/schema/adapter";
import { type FieldValue, unknownInputs, valuesFromInputs } from "@/lib/schema/values";

import { SchemaField } from "./SchemaField";
import { TextField } from "./TextField";

/** Turns the form into an update of an existing stack (WBS 7.6). */
export interface UpdateContext {
  stack: Pick<StackSummary, "id" | "name" | "team" | "environment" | "path">;
  /**
   * The stack's resources as the update will send them; the one at `editedIndex` gets the
   * form's inputs. With no current spec this is a single placeholder for the form's module.
   */
  resources: readonly ResourceInput[];
  editedIndex: number;
  /** The edited resource's stored inputs, or null when the form starts from module defaults. */
  initialInputs: Record<string, unknown> | null;
}

export interface RequestFormProps {
  version: Pick<ModuleVersion, "module" | "version" | "inputs_schema">;
  /** Set to update an existing stack instead of creating one. */
  update?: UpdateContext;
  /** Injected for tests; defaults to the browser ApiClient. */
  client?: Pick<ApiClient, "createRequest">;
  /** Injected for tests; defaults to crypto.randomUUID. */
  newIdempotencyKey?: () => string;
  /** Called after the request is accepted; defaults to navigating to its page. */
  onCreated?: (request: RequestSummary) => void;
}

interface Failure {
  kind: FailureKind;
  title: string;
  detail?: string;
  /** What to do next, for the API's conflict errors. */
  hint?: string;
  code?: string;
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

export function RequestForm({ version, update, client, newIdempotencyKey, onCreated }: RequestFormProps) {
  const router = useRouter();
  const adapter = useMemo(() => new SchemaAdapter(version), [version]);
  // The Idempotency-Key is bound to the payload it was sent with (src/lib/request/idempotency.ts):
  // resubmitting the same payload reuses it, so a request that reached the backend is never
  // created twice; changing the payload mints a new one. It is created on submit, never during
  // render, so server and client markup match.
  const [sent, setSent] = useState<KeyedPayload | null>(null);
  const [stack, setStack] = useState<StackFields>(() => ({
    team: "",
    environment: "development",
    stack: "",
    resourceName: update?.resources[update.editedIndex]?.name ?? adapter.defaultResourceName(),
    justification: "",
  }));
  const [values, setValues] = useState<Record<string, FieldValue>>(() =>
    update?.initialInputs ? valuesFromInputs(adapter.fields, update.initialInputs) : adapter.initialValues(),
  );
  // An update that drops resources is refused until the user acknowledges it (allow_removals).
  const [removal, setRemoval] = useState<{ names: string[] } | null>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const [focusTick, setFocusTick] = useState(0);
  const [errors, setErrors] = useState<FormErrors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const summaryRef = useRef<HTMLDivElement>(null);

  const { inputs, errors: inputProblems } = adapter.toInputs(values);
  const payload = update
    ? buildUpdatePayload({
        stackId: update.stack.id,
        resources: update.resources,
        editedIndex: update.editedIndex,
        inputs,
        justification: stack.justification,
        allowRemovals: acknowledged,
      })
    : buildCreatePayload(stack, adapter.module, adapter.version, inputs);
  const payloadText = JSON.stringify(payload);
  const currentKey = sent !== null && sent.payload === payloadText ? sent.key : null;

  const setStackField = (key: keyof StackFields) => (value: string) => setStack((s) => ({ ...s, [key]: value }));
  const setValue = (name: string, value: FieldValue) => setValues((v) => ({ ...v, [name]: value }));

  const showProblems = (next: FormErrors, nextFailure: Failure | null) => {
    setErrors(next);
    setFailure(nextFailure);
    // Focus moves in an effect, after React has rendered the summary (a microtask can run
    // before the first error's summary exists).
    setFocusTick((t) => t + 1);
  };

  useEffect(() => {
    if (focusTick > 0) summaryRef.current?.focus();
  }, [focusTick]);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const stackErrors = update ? validateJustification(stack.justification) : validateStackFields(stack);
    const clientErrors = { ...stackErrors, ...inputErrors(inputProblems) };
    if (hasErrors(clientErrors)) {
      showProblems(clientErrors, null);
      return;
    }
    if (removal && !acknowledged) {
      showProblems({}, {
        kind: "rejected",
        title: "Acknowledge the removal first",
        general: ["Tick the box under “Resources will be removed”, then submit again."],
      });
      return;
    }
    const keyed = keyFor(sent, payloadText, newIdempotencyKey ?? (() => crypto.randomUUID()));
    setSent(keyed);
    setSubmitting(true);
    setErrors({});
    setFailure(null);
    try {
      const { request } = await (client ?? browserApi()).createRequest(payload, keyed.key);
      if (onCreated) onCreated(request);
      else router.push(`/requests/${encodeURIComponent(request.id)}`);
    } catch (err) {
      const kind = classifyFailure(err);
      if (kind === "key_reused") {
        // Only reachable if the payload changed under the same key; start over with a new one.
        setSent(null);
        showProblems({}, {
          kind,
          title: "This form was already submitted with different values",
          detail: "An earlier submit may have created a request. Check your requests before submitting again.",
          requestId: isApiError(err) ? err.requestId : undefined,
          general: [],
        });
      } else if (isApiError(err)) {
        const mapped = mapFieldErrors(err.fieldErrors, update?.editedIndex ?? 0);
        const explained = update ? explainLifecycleError(err, "update") : undefined;
        const removed = err.code === "resources_removed";
        if (removed) {
          setRemoval({ names: removedResourceNames(err.problem.detail) });
          setAcknowledged(false);
        }
        showProblems(mapped.fields, {
          kind,
          title: explained?.title ?? err.problem.title,
          detail: explained ? explained.detail : err.problem.detail,
          hint: explained?.hint || undefined,
          code: err.code,
          requestId: err.requestId,
          general: removed ? [] : mapped.general,
        });
      } else {
        showProblems({}, { kind, title: "Request failed", detail: String(err), general: [] });
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
          {failure?.hint && <p className="mt-2">{failure.hint}</p>}
          {update && failure?.hint && failure.code !== "resources_removed" && (
            <p className="mt-2">
              <Link href={`/stacks/${encodeURIComponent(update.stack.id)}`} className="underline">
                Open the stack
              </Link>{" "}
              or{" "}
              <Link href="/stacks" className="underline">
                go to all stacks
              </Link>
              .
            </p>
          )}
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
          {(failure?.kind === "ambiguous" || failure?.kind === "key_reused") && (
            <p className="mt-2">
              {failure.kind === "ambiguous" &&
                "The platform may have received the request. Submitting again without changes is safe; if you change the form, check "}
              {failure.kind === "key_reused" && "See "}
              <Link href="/requests?mine=true" className="underline">
                your requests
              </Link>
              {failure.kind === "ambiguous" ? " first." : "."}
            </p>
          )}
          {failure?.requestId && (
            <p className="mt-2 text-xs">
              Request ID: <code className="font-mono">{failure.requestId}</code>
            </p>
          )}
        </div>
      )}

      {update && (
        <fieldset className="space-y-4 rounded-lg border border-slate-200 bg-white p-5">
          <legend className="px-1 text-base font-semibold">Stack</legend>
          <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            <div>
              <dt className="font-medium text-slate-600">Stack</dt>
              <dd className="font-mono text-xs">{update.stack.path}</dd>
            </div>
            <div>
              <dt className="font-medium text-slate-600">Resource</dt>
              <dd className="font-mono text-xs">{stack.resourceName}</dd>
            </div>
          </dl>
          {update.initialInputs === null && (
            <p role="note" className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">
              No request carrying this stack&apos;s spec was found, so the resources it really contains are unknown. The
              form starts from the module defaults, not from what is deployed. Check every value; anything you leave out
              falls back to its default.
            </p>
          )}
          {update.initialInputs !== null && unknownInputs(adapter.fields, update.initialInputs).length > 0 && (
            <p role="note" className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">
              The stored settings include{" "}
              <span className="font-mono">{unknownInputs(adapter.fields, update.initialInputs).join(", ")}</span>, which{" "}
              {adapter.module} {adapter.version} no longer has. They cannot be edited here and are dropped from this update.
            </p>
          )}
          <TextField
            id={fieldId("justification")}
            label="Justification"
            multiline
            value={stack.justification}
            onChange={setStackField("justification")}
            errors={errors.justification}
            hint={`Optional, shown on the pull request (max ${MAX_JUSTIFICATION} characters).`}
          />
        </fieldset>
      )}

      {!update && (
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
      )}

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

      {removal && (
        <fieldset className="space-y-2 rounded-lg border border-amber-300 bg-amber-50 p-5">
          <legend className="px-1 text-base font-semibold text-amber-900">Resources will be removed</legend>
          <p className="text-sm text-amber-900">
            {removal.names.length > 0 ? (
              <>
                This update leaves out <span className="font-mono">{removal.names.join(", ")}</span>. Merging its pull request
                destroys {removal.names.length === 1 ? "that resource" : "those resources"} and the data in{" "}
                {removal.names.length === 1 ? "it" : "them"}.
              </>
            ) : (
              "This update leaves out resources the stack has now. Merging its pull request destroys them and their data."
            )}
          </p>
          <label htmlFor="ack-removals" className="flex items-start gap-2 text-sm font-medium text-amber-950">
            <input
              id="ack-removals"
              type="checkbox"
              checked={acknowledged}
              onChange={(e) => setAcknowledged(e.target.checked)}
              className="mt-0.5 h-4 w-4 rounded border-slate-400"
            />
            <span>I understand this update removes resources, and I want to send it with allow_removals.</span>
          </label>
        </fieldset>
      )}

      <details className="rounded-lg border border-slate-200 bg-white p-5">
        <summary className="cursor-pointer text-base font-semibold">Preview request</summary>
        <p className="mt-2 text-sm text-slate-600">
          The exact call the portal will make.
          {Object.keys(inputProblems).length > 0 && " Inputs with invalid values are left out until fixed."}
        </p>
        <pre className="mt-3 overflow-x-auto rounded-md bg-slate-900 p-4 text-xs text-slate-100">
          <code>
            {`POST /v1/requests\nIdempotency-Key: ${currentKey ?? "<a new UUID, created on submit and reused while the values stay the same>"}\nContent-Type: application/json\n\n${JSON.stringify(payload, null, 2)}`}
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
          {submitting ? "Submitting…" : update ? "Submit update request" : "Submit request"}
        </button>
        <span className="text-xs text-slate-500" aria-live="polite">
          {submitting ? "Sending the request to the platform." : ""}
        </span>
      </div>
    </form>
  );
}
