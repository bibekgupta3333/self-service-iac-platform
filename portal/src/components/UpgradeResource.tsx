"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, useEffect, useRef, useState } from "react";

import { Badge } from "@/components/Badge";
import type { ApiClient } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import { browserApi } from "@/lib/api/factory";
import type { RequestSummary, StackSummary, UpgradeStep } from "@/lib/api/types";
import { formatDateTime } from "@/lib/format";
import { classifyFailure, type FailureKind, type KeyedPayload, keyFor } from "@/lib/request/idempotency";
import {
  buildUpgradePayload,
  explainUpgradeError,
  type ResourceUpgrade,
  upgradeLabel,
  type UpgradeErrors,
  validateUpgrade,
} from "@/lib/request/upgrade";
import type { ActionState } from "@/lib/stacks";

export interface UpgradeResourceProps {
  stack: Pick<StackSummary, "id" | "name" | "environment">;
  upgrade: ResourceUpgrade;
  /** Whether the caller may change this stack now, and why not (the update rules). */
  state: ActionState;
  /** Injected for tests; defaults to the browser ApiClient. */
  client?: Pick<ApiClient, "createRequest">;
  /** Injected for tests; defaults to crypto.randomUUID. */
  newIdempotencyKey?: () => string;
  /** Called once the upgrade request is accepted; defaults to opening its page. */
  onCreated?: (request: RequestSummary) => void;
}

interface Failure {
  kind: FailureKind;
  title: string;
  detail?: string;
  hint?: string;
  requestId?: string;
  general: string[];
}

const FIELD =
  "mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm aria-[invalid=true]:border-red-600";

/** Notes are Markdown from the module's CHANGELOG; they are shown as text, never as HTML. */
function Notes({ text }: { text: string }) {
  return <pre className="whitespace-pre-wrap break-words font-sans text-sm">{text}</pre>;
}

/** MigrationNotes lists the migration notes of every major step, prominently. */
function MigrationNotes({ id, module, steps }: { id: string; module: string; steps: UpgradeStep[] }) {
  return (
    <section id={id} aria-label="Migration notes" className="space-y-3 rounded-md border border-amber-400 bg-amber-50 p-4 text-amber-950">
      <p className="font-semibold">This upgrade crosses a major version. Read the migration notes before you continue.</p>
      {steps.map((s) => (
        <div key={s.version} className="space-y-1">
          <h4 className="text-sm font-semibold">
            {module} {s.version}
          </h4>
          {s.upgrade_notes ? (
            <Notes text={s.upgrade_notes} />
          ) : (
            <p className="text-sm">
              The module ships no migration notes for this version. Review its changelog and the plan carefully.
            </p>
          )}
        </div>
      ))}
    </section>
  );
}

/**
 * UpgradeResource shows one resource's pinned version and, when a newer one exists, the
 * upgrade path with the class of each step and an "Upgrade to" action. The action opens a
 * modal confirmation that puts a major version's migration notes first and requires them to
 * be acknowledged; the request is sent with an Idempotency-Key bound to its body.
 */
export function UpgradeResource({ stack, upgrade, state, client, newIdempotencyKey, onCreated }: UpgradeResourceProps) {
  const router = useRouter();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const summaryRef = useRef<HTMLDivElement>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const [justification, setJustification] = useState("");
  const [errors, setErrors] = useState<UpgradeErrors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [sent, setSent] = useState<KeyedPayload | null>(null);
  const [focusTick, setFocusTick] = useState(0);

  useEffect(() => {
    if (focusTick > 0) summaryRef.current?.focus();
  }, [focusTick]);

  const label = upgradeLabel(upgrade);
  const target = upgrade.target;
  const breaking = upgrade.majorSteps.length > 0;
  const base = `upgrade-${upgrade.resource}`;

  const open = () => {
    if (!state.enabled || !target) return;
    const dialog = dialogRef.current;
    if (!dialog) return;
    setErrors({});
    setFailure(null);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  };

  const close = () => {
    const dialog = dialogRef.current;
    if (dialog?.open) {
      if (typeof dialog.close === "function") dialog.close();
      else dialog.removeAttribute("open");
    }
    triggerRef.current?.focus();
  };

  const problems = (next: UpgradeErrors, nextFailure: Failure | null) => {
    setErrors(next);
    setFailure(nextFailure);
    setFocusTick((t) => t + 1);
  };

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!target) return;
    const invalid = validateUpgrade(breaking, { acknowledged, justification });
    if (Object.keys(invalid).length > 0) {
      problems(invalid, null);
      return;
    }
    const payload = buildUpgradePayload({
      stackId: stack.id,
      resource: upgrade.resource,
      toVersion: target,
      justification,
      acknowledgeBreaking: breaking && acknowledged,
    });
    const keyed = keyFor(sent, JSON.stringify(payload), newIdempotencyKey ?? (() => crypto.randomUUID()));
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
        setSent(null);
        problems({}, {
          kind,
          title: "This upgrade was already sent with different values",
          detail: "An earlier submit may have created a request. Check your requests before submitting again.",
          requestId: isApiError(err) ? err.requestId : undefined,
          general: [],
        });
      } else if (isApiError(err)) {
        const explained = explainUpgradeError(err);
        problems({}, {
          kind,
          title: explained.title,
          detail: explained.detail,
          hint: explained.hint || undefined,
          requestId: err.requestId,
          general: err.fieldErrors.map((f) => (f.pointer ? `${f.pointer}: ${f.message}` : f.message)),
        });
      } else {
        problems({}, { kind, title: "Request failed", detail: String(err), general: [] });
      }
    } finally {
      setSubmitting(false);
    }
  }

  const reasonId = `${base}-unavailable`;
  const fieldMessages = Object.entries(errors) as Array<[keyof UpgradeErrors, string]>;

  return (
    <div className="space-y-3 rounded-lg border border-slate-200 bg-white p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h3 className="font-mono text-sm font-semibold">{upgrade.resource}</h3>
        <span className="text-sm text-slate-700">
          {upgrade.module} <span className="font-mono">v{upgrade.pinned}</span>
        </span>
        <Badge tone={label.tone}>{label.text}</Badge>
      </div>

      {!upgrade.path && (
        <p className="text-sm text-slate-600">The catalog has no upgrade path for this version.</p>
      )}

      {target && (
        <>
          <ol aria-label={`Upgrade path of ${upgrade.resource}`} className="flex flex-wrap items-center gap-2 text-sm">
            <li className="font-mono">{upgrade.pinned}</li>
            {upgrade.steps.map((s) => (
              <li key={s.version} className="flex items-center gap-2">
                <span aria-hidden="true">→</span>
                <span className="font-mono">{s.version}</span>
                <Badge tone={s.breaking ? "warning" : "neutral"}>{s.change}</Badge>
                {s.status === "deprecated" && <Badge tone="warning">deprecated</Badge>}
              </li>
            ))}
          </ol>
          {upgrade.moreAfter && upgrade.path && (
            <p className="text-sm text-slate-600">
              Versions up to {upgrade.path.to} exist. They are one more major away and need a separate upgrade after this one.
            </p>
          )}
          {breaking && <MigrationNotes id={`${base}-notes`} module={upgrade.module} steps={upgrade.majorSteps} />}
          <details className="text-sm">
            <summary className="cursor-pointer text-indigo-700">Changelog of each version</summary>
            <div className="mt-2 space-y-3">
              {upgrade.steps.map((s) => (
                <div key={s.version}>
                  <h4 className="font-semibold">
                    {s.version} <span className="font-normal text-slate-600">({formatDateTime(s.released_at)})</span>
                  </h4>
                  {s.changelog_md ? <Notes text={s.changelog_md} /> : <p className="text-slate-600">No changelog.</p>}
                </div>
              ))}
            </div>
          </details>
          <div>
            <button
              ref={triggerRef}
              type="button"
              onClick={open}
              aria-disabled={!state.enabled}
              aria-haspopup="dialog"
              aria-describedby={state.enabled ? undefined : reasonId}
              className={`inline-flex items-center rounded-md px-4 py-2 text-sm font-medium ${
                state.enabled ? "bg-indigo-600 text-white hover:bg-indigo-700" : "cursor-not-allowed bg-slate-200 text-slate-600"
              }`}
            >
              Upgrade to {target}
            </button>
            {!state.enabled && (
              <p id={reasonId} className="mt-2 max-w-md text-sm text-slate-600">
                Upgrade unavailable: {state.reason}
              </p>
            )}
          </div>

          <dialog
            ref={dialogRef}
            aria-labelledby={`${base}-title`}
            onClose={() => triggerRef.current?.focus()}
            className="m-auto w-[min(92vw,42rem)] rounded-lg border border-slate-300 bg-white p-0 shadow-xl backdrop:bg-slate-900/50"
          >
            <form onSubmit={onSubmit} noValidate className="space-y-4 p-6">
              <h2 id={`${base}-title`} className="text-lg font-semibold">
                Upgrade {upgrade.resource} to {upgrade.module} {target}?
              </h2>
              {breaking && <MigrationNotes id={`${base}-dialog-notes`} module={upgrade.module} steps={upgrade.majorSteps} />}
              <p className="text-sm text-slate-700">
                The stack&apos;s inputs stay as they are and the module source is pinned to{" "}
                <span className="font-mono">v{target}</span>. Submitting opens a pull request; nothing changes until its plan is
                reviewed and it is merged.
                {stack.environment === "production" && <strong> This is a production stack.</strong>}
              </p>

              {(failure !== null || fieldMessages.length > 0) && (
                <div
                  ref={summaryRef}
                  tabIndex={-1}
                  role="alert"
                  aria-labelledby={`${base}-error-title`}
                  className="rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-900"
                >
                  <p id={`${base}-error-title`} className="font-medium">
                    {failure ? failure.title : "Please fix the highlighted fields"}
                  </p>
                  {failure?.detail && <p className="mt-1">{failure.detail}</p>}
                  {failure?.hint && <p className="mt-1">{failure.hint}</p>}
                  {(fieldMessages.length > 0 || (failure?.general.length ?? 0) > 0) && (
                    <ul className="mt-2 list-disc space-y-1 pl-5">
                      {fieldMessages.map(([key, msg]) => (
                        <li key={key}>{msg}</li>
                      ))}
                      {failure?.general.map((g) => (
                        <li key={g}>{g}</li>
                      ))}
                    </ul>
                  )}
                  {(failure?.kind === "ambiguous" || failure?.kind === "key_reused") && (
                    <p className="mt-2">
                      The platform may have received the request. Check{" "}
                      <Link href="/requests?mine=true" className="underline">
                        your requests
                      </Link>{" "}
                      before you change anything; submitting again without changes is safe.
                    </p>
                  )}
                  {failure?.requestId && (
                    <p className="mt-2 text-xs">
                      Request ID: <code className="font-mono">{failure.requestId}</code>
                    </p>
                  )}
                </div>
              )}

              {breaking && (
                <div>
                  <label className="flex items-start gap-2 text-sm font-medium text-slate-800">
                    <input
                      type="checkbox"
                      checked={acknowledged}
                      onChange={(e) => setAcknowledged(e.target.checked)}
                      aria-invalid={errors.acknowledged !== undefined}
                      aria-describedby={`${base}-dialog-notes`}
                      className="mt-0.5"
                    />
                    <span>
                      I read the migration notes for{" "}
                      {upgrade.majorSteps.map((s) => `${upgrade.module} ${s.version}`).join(", ")} and the stack is ready for them.
                    </span>
                  </label>
                </div>
              )}

              <div>
                <label htmlFor={`${base}-justification`} className="text-sm font-medium text-slate-800">
                  Justification <span className="font-normal text-slate-600">(optional)</span>
                </label>
                <textarea
                  id={`${base}-justification`}
                  rows={2}
                  value={justification}
                  onChange={(e) => setJustification(e.target.value)}
                  aria-invalid={errors.justification !== undefined}
                  className={FIELD}
                />
              </div>

              <div className="flex flex-wrap items-center justify-end gap-3">
                <span className="mr-auto text-xs text-slate-500" aria-live="polite">
                  {submitting ? "Sending the request to the platform." : ""}
                </span>
                <button
                  type="button"
                  onClick={close}
                  className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-medium hover:bg-slate-100"
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  disabled={submitting || (breaking && !acknowledged)}
                  className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {submitting ? "Submitting…" : "Open upgrade request"}
                </button>
              </div>
            </form>
          </dialog>
        </>
      )}
    </div>
  );
}
