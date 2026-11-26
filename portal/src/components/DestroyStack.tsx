"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { type FormEvent, type SyntheticEvent, useEffect, useRef, useState } from "react";

import type { ApiClient } from "@/lib/api/client";
import { isApiError } from "@/lib/api/errors";
import { browserApi } from "@/lib/api/factory";
import type { RequestSummary, StackSummary } from "@/lib/api/types";
import { classifyFailure, type FailureKind, type KeyedPayload, keyFor } from "@/lib/request/idempotency";
import {
  buildDestroyPayload,
  type DestroyErrors,
  explainLifecycleError,
  mapDestroyFieldErrors,
  nameMatches,
  validateDestroy,
} from "@/lib/request/lifecycle";
import type { ActionState } from "@/lib/stacks";

export interface DestroyStackProps {
  stack: Pick<StackSummary, "id" | "name" | "path" | "team" | "environment">;
  /** Whether the caller may destroy this stack now, and why not. */
  state: ActionState;
  /** Injected for tests; defaults to the browser ApiClient. */
  client?: Pick<ApiClient, "createRequest">;
  /** Injected for tests; defaults to crypto.randomUUID. */
  newIdempotencyKey?: () => string;
  /** Called once the destroy request is accepted; defaults to opening its page. */
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

/**
 * DestroyStack is the Destroy action: a button that opens a modal confirmation. Opening a
 * destroy request does not remove anything by itself, but merging its pull request does, so
 * the dialog says so plainly and asks for the stack's exact name and a justification before it
 * sends anything. The native <dialog> gives the focus trap, Escape and focus return.
 */
export function DestroyStack({ stack, state, client, newIdempotencyKey, onCreated }: DestroyStackProps) {
  const router = useRouter();
  const dialogRef = useRef<HTMLDialogElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const summaryRef = useRef<HTMLDivElement>(null);
  const [confirmName, setConfirmName] = useState("");
  const [justification, setJustification] = useState("");
  const [errors, setErrors] = useState<DestroyErrors>({});
  const [failure, setFailure] = useState<Failure | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [sent, setSent] = useState<KeyedPayload | null>(null);
  const [focusTick, setFocusTick] = useState(0);
  // Synchronous double-submit guard: state updates land only after the next render, so two
  // quick submits would both pass a check on `submitting`.
  const inFlight = useRef(false);

  // Focus moves to the error summary after React has rendered it.
  useEffect(() => {
    if (focusTick > 0) summaryRef.current?.focus();
  }, [focusTick]);

  const open = () => {
    if (!state.enabled) return;
    const dialog = dialogRef.current;
    if (!dialog) return;
    setErrors({});
    setFailure(null);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", ""); // environments without <dialog> support
    nameRef.current?.focus();
  };

  // A dismissed confirmation starts empty next time: the typed name must be typed again.
  const reset = () => {
    setConfirmName("");
    setJustification("");
    setErrors({});
    setFailure(null);
  };

  const close = () => {
    if (inFlight.current) return;
    const dialog = dialogRef.current;
    if (dialog?.open) {
      if (typeof dialog.close === "function") dialog.close();
      else dialog.removeAttribute("open");
    }
    reset();
    triggerRef.current?.focus();
  };

  // Escape fires `cancel` before `close`; while a request is in flight the dialog stays open.
  const onCancel = (e: SyntheticEvent<HTMLDialogElement>) => {
    if (inFlight.current) e.preventDefault();
  };

  const onClose = () => {
    // A browser may still close the dialog on a second Escape after the first `cancel` was
    // prevented; keep the fields (and the error summary to come) while the submit is in flight.
    if (!inFlight.current) reset();
    triggerRef.current?.focus();
  };

  const problems = (next: DestroyErrors, nextFailure: Failure | null) => {
    setErrors(next);
    setFailure(nextFailure);
    setFocusTick((t) => t + 1);
  };

  const payload = buildDestroyPayload(stack.id, confirmName, justification);
  const payloadText = JSON.stringify(payload);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (inFlight.current) return;
    const invalid = validateDestroy(stack.name, { confirmName, justification });
    if (Object.keys(invalid).length > 0) {
      problems(invalid, null);
      return;
    }
    // Same payload-bound key as the create form: a retry of the same body is replayed by the API.
    const keyed = keyFor(sent, payloadText, newIdempotencyKey ?? (() => crypto.randomUUID()));
    setSent(keyed);
    inFlight.current = true;
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
          title: "This confirmation was already sent with different values",
          detail: "An earlier submit may have created a request. Check your requests before submitting again.",
          requestId: isApiError(err) ? err.requestId : undefined,
          general: [],
        });
      } else if (isApiError(err)) {
        const explained = explainLifecycleError(err, "destroy");
        const mapped = mapDestroyFieldErrors(err.fieldErrors);
        problems(mapped.fields, {
          kind,
          title: explained.title,
          detail: explained.detail,
          hint: explained.hint || undefined,
          requestId: err.requestId,
          general: mapped.general,
        });
      } else {
        problems({}, { kind, title: "Request failed", detail: String(err), general: [] });
      }
    } finally {
      inFlight.current = false;
      setSubmitting(false);
    }
  }

  const reasonId = "destroy-unavailable";
  const matches = nameMatches(stack.name, confirmName);
  const fieldMessages = Object.entries(errors) as Array<[keyof DestroyErrors, string]>;

  return (
    <div>
      <button
        ref={triggerRef}
        type="button"
        onClick={open}
        aria-disabled={!state.enabled}
        aria-haspopup="dialog"
        aria-describedby={state.enabled ? undefined : reasonId}
        className={`inline-flex items-center rounded-md px-4 py-2 text-sm font-medium ${
          state.enabled ? "bg-red-700 text-white hover:bg-red-800" : "cursor-not-allowed bg-slate-200 text-slate-600"
        }`}
      >
        Destroy
      </button>
      {!state.enabled && (
        <p id={reasonId} className="mt-2 max-w-md text-sm text-slate-600">
          Destroy unavailable: {state.reason}
        </p>
      )}

      <dialog
        ref={dialogRef}
        aria-labelledby="destroy-title"
        aria-describedby="destroy-warning"
        onCancel={onCancel}
        onClose={onClose}
        className="m-auto w-[min(92vw,38rem)] rounded-lg border border-slate-300 bg-white p-0 shadow-xl backdrop:bg-slate-900/50"
      >
        <form onSubmit={onSubmit} noValidate className="space-y-4 p-6">
          <h2 id="destroy-title" className="text-lg font-semibold text-red-800">
            Destroy {stack.name}?
          </h2>
          <div id="destroy-warning" className="space-y-2 rounded-md border border-red-300 bg-red-50 p-4 text-sm text-red-950">
            <p className="font-semibold">Merging the destroy pull request removes the infrastructure.</p>
            <p>
              Every resource in <span className="font-mono">{stack.path}</span> is deleted, together with the data in it.
              This cannot be undone. Nothing is removed until the pull request is reviewed and merged.
            </p>
            {stack.environment === "production" && (
              <p className="font-semibold">This is a production stack.</p>
            )}
          </div>

          {(failure !== null || fieldMessages.length > 0) && (
            <div
              ref={summaryRef}
              tabIndex={-1}
              role="alert"
              aria-labelledby="destroy-error-title"
              className="rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-900"
            >
              <p id="destroy-error-title" className="font-medium">
                {failure ? failure.title : "Please fix the highlighted fields"}
              </p>
              {failure?.detail && <p className="mt-1">{failure.detail}</p>}
              {failure?.hint && <p className="mt-1">{failure.hint}</p>}
              {(fieldMessages.length > 0 || (failure?.general.length ?? 0) > 0) && (
                <ul className="mt-2 list-disc space-y-1 pl-5">
                  {fieldMessages.map(([key, msg]) => (
                    <li key={key}>
                      <a href={`#destroy-${key === "confirmName" ? "confirm" : "justification"}`} className="underline">
                        {key === "confirmName" ? "Stack name" : "Justification"}
                      </a>
                      : {msg}
                    </li>
                  ))}
                  {failure?.general.map((g) => (
                    <li key={g}>{g}</li>
                  ))}
                </ul>
              )}
              {failure?.kind === "ambiguous" && (
                <p className="mt-2">
                  The platform may have received the request. Submitting again without changes is safe; if you change anything,
                  check{" "}
                  <Link href="/requests?mine=true" className="underline">
                    your requests
                  </Link>{" "}
                  first.
                </p>
              )}
              {failure?.kind === "key_reused" && (
                <p className="mt-2">
                  See{" "}
                  <Link href="/requests?mine=true" className="underline">
                    your requests
                  </Link>
                  .
                </p>
              )}
              {failure?.requestId && (
                <p className="mt-2 text-xs">
                  Request ID: <code className="font-mono">{failure.requestId}</code>
                </p>
              )}
            </div>
          )}

          <div>
            <label htmlFor="destroy-confirm" className="text-sm font-medium text-slate-800">
              Type <span className="font-mono font-semibold">{stack.name}</span> to confirm
              <span className="ml-1 text-red-700" aria-hidden="true">
                *
              </span>
            </label>
            <input
              ref={nameRef}
              id="destroy-confirm"
              type="text"
              required
              autoComplete="off"
              spellCheck={false}
              value={confirmName}
              onChange={(e) => setConfirmName(e.target.value)}
              aria-invalid={errors.confirmName !== undefined}
              aria-describedby={errors.confirmName ? "destroy-confirm-error" : "destroy-warning"}
              className={FIELD}
            />
            {errors.confirmName && (
              <p id="destroy-confirm-error" className="mt-1 text-xs text-red-700">
                {errors.confirmName}
              </p>
            )}
          </div>

          <div>
            <label htmlFor="destroy-justification" className="text-sm font-medium text-slate-800">
              Justification
              <span className="ml-1 text-red-700" aria-hidden="true">
                *
              </span>
            </label>
            <textarea
              id="destroy-justification"
              required
              rows={3}
              value={justification}
              onChange={(e) => setJustification(e.target.value)}
              aria-invalid={errors.justification !== undefined}
              aria-describedby={`destroy-justification-hint${errors.justification ? " destroy-justification-error" : ""}`}
              className={FIELD}
            />
            <p id="destroy-justification-hint" className="mt-1 text-xs text-slate-600">
              Why this stack is being destroyed. It is recorded on the pull request and in the audit log.
            </p>
            {errors.justification && (
              <p id="destroy-justification-error" className="mt-1 text-xs text-red-700">
                {errors.justification}
              </p>
            )}
          </div>

          <div className="flex flex-wrap items-center justify-end gap-3">
            <span className="mr-auto text-xs text-slate-500" aria-live="polite">
              {submitting ? "Sending the request to the platform." : ""}
            </span>
            <button
              type="button"
              onClick={close}
              disabled={submitting}
              className="rounded-md border border-slate-300 bg-white px-4 py-2 text-sm font-medium hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={!matches || submitting}
              className="rounded-md bg-red-700 px-4 py-2 text-sm font-medium text-white hover:bg-red-800 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {submitting ? "Submitting…" : "Open destroy request"}
            </button>
          </div>
        </form>
      </dialog>
    </div>
  );
}
