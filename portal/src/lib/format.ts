// Pure display helpers. Dates are formatted by hand in UTC so server and client render the
// same text (no hydration mismatches from locale or time zone).

import type { CatalogStatus, RequestStatus } from "@/lib/api/types";

export type Tone = "neutral" | "info" | "progress" | "success" | "warning" | "danger";

const pad = (n: number): string => String(n).padStart(2, "0");

/** formatDateTime renders an RFC 3339 timestamp as "YYYY-MM-DD HH:MM UTC". Pure. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC`;
}

/** categoriesOf lists the distinct module categories, sorted. Pure. */
export function categoriesOf(modules: readonly { category: string }[]): string[] {
  return [...new Set(modules.map((m) => m.category).filter(Boolean))].sort();
}

/** shortSha keeps the first 7 characters of a commit SHA. Pure. */
export const shortSha = (sha: string): string => sha.slice(0, 7);

export function catalogStatusTone(status: CatalogStatus): Tone {
  switch (status) {
    case "active":
      return "success";
    case "deprecated":
      return "warning";
    case "yanked":
      return "danger";
  }
}

const REQUEST_LABELS: Record<RequestStatus, string> = {
  draft: "Draft",
  pr_open: "PR open",
  validating: "Validating",
  plan_ready: "Plan ready",
  approved: "Approved",
  applying: "Applying",
  applied: "Applied",
  failed: "Failed",
  rejected: "Rejected",
  cancelled: "Cancelled",
};

/** requestStatusLabel returns the display label, or the raw value for unknown statuses. Pure. */
export const requestStatusLabel = (s: string): string => REQUEST_LABELS[s as RequestStatus] ?? s;

export function requestStatusTone(s: string): Tone {
  switch (s) {
    case "applied":
      return "success";
    case "failed":
      return "danger";
    case "rejected":
    case "cancelled":
      return "warning";
    case "validating":
    case "applying":
      return "progress";
    case "pr_open":
    case "plan_ready":
    case "approved":
      return "info";
    default:
      return "neutral";
  }
}

/** Statuses after which a request never changes again (docs/03 §4.1). */
export const TERMINAL_STATUSES: readonly RequestStatus[] = ["applied", "failed", "rejected", "cancelled"];

export const isTerminal = (s: string): boolean => (TERMINAL_STATUSES as readonly string[]).includes(s);

/** The happy path of the request state machine, in order. */
export const HAPPY_PATH: readonly RequestStatus[] = [
  "draft",
  "pr_open",
  "validating",
  "plan_ready",
  "approved",
  "applying",
  "applied",
];

export type StepState = "done" | "current" | "upcoming" | "unknown" | "stopped";

export interface TimelineStep {
  status: RequestStatus;
  label: string;
  state: StepState;
}

/**
 * timeline places the current status on the happy path. The API returns only the current
 * status (the transition history arrives with GET /v1/requests/{id}/events, WBS 7.4), so for
 * a request that left the path (failed, rejected, cancelled) the steps it reached are
 * unknown and the outcome is appended as a stopped step. Pure.
 */
export function timeline(status: string): TimelineStep[] {
  const idx = HAPPY_PATH.indexOf(status as RequestStatus);
  if (idx >= 0) {
    return HAPPY_PATH.map((s, i) => ({
      status: s,
      label: REQUEST_LABELS[s],
      state: i < idx || (i === idx && s === "applied") ? "done" : i === idx ? "current" : "upcoming",
    }));
  }
  const steps: TimelineStep[] = HAPPY_PATH.slice(0, -1).map((s) => ({
    status: s,
    label: REQUEST_LABELS[s],
    state: "unknown",
  }));
  return [...steps, { status: status as RequestStatus, label: requestStatusLabel(status), state: "stopped" }];
}
