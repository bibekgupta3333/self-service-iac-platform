// Pure logic for the stack pages: list filters and links, health wording, output rows and
// which actions a stack currently offers. No I/O.

import type { StackStatus, StackSummary } from "@/lib/api/types";
import { type Capabilities, decide } from "@/lib/auth/permissions";
import type { Tone } from "@/lib/format";
import { ENVIRONMENTS } from "@/lib/request/form";

export const STACK_STATUSES: readonly StackStatus[] = [
  "provisioning",
  "active",
  "drifted",
  "updating",
  "destroying",
  "destroyed",
  "failed",
];

/** The statuses an update or destroy may start from (backend requests.editableStatuses). */
const EDITABLE: readonly StackStatus[] = ["active", "drifted", "failed"];

export interface StackQuery {
  team?: string;
  env?: string;
  status?: StackStatus;
  cursor?: string;
}

const first = (v: string | string[] | undefined): string | undefined => (Array.isArray(v) ? v[0] : v);

/**
 * parseStackQuery reads the list filters from the URL's search params. Values the API would
 * refuse (an unknown environment or status) are dropped rather than sent. Pure.
 */
export function parseStackQuery(sp: Record<string, string | string[] | undefined>): StackQuery {
  const team = first(sp.team)?.trim();
  const env = first(sp.env)?.trim();
  const status = first(sp.status)?.trim();
  const cursor = first(sp.cursor)?.trim();
  return {
    ...(team ? { team } : {}),
    ...(env && (ENVIRONMENTS as readonly string[]).includes(env) ? { env } : {}),
    ...(status && (STACK_STATUSES as readonly string[]).includes(status) ? { status: status as StackStatus } : {}),
    ...(cursor ? { cursor } : {}),
  };
}

/** stacksHref builds the /stacks link for a filter state. Pure. */
export function stacksHref(q: StackQuery): string {
  const p = new URLSearchParams();
  if (q.team) p.set("team", q.team);
  if (q.env) p.set("env", q.env);
  if (q.status) p.set("status", q.status);
  if (q.cursor) p.set("cursor", q.cursor);
  const s = p.toString();
  return s ? `/stacks?${s}` : "/stacks";
}

export interface Health {
  label: string;
  tone: Tone;
  description: string;
}

/** stackHealth summarises a stack status for people. Pure. */
export function stackHealth(status: string): Health {
  switch (status) {
    case "active":
      return { label: "Healthy", tone: "success", description: "The last apply succeeded and no drift is recorded." };
    case "drifted":
      return {
        label: "Drifted",
        tone: "warning",
        description: "The deployed resources no longer match the code. Update to codify the change, or reconcile.",
      };
    case "failed":
      return {
        label: "Failed",
        tone: "danger",
        description: "The last change did not apply. Fix forward with an update, or destroy the stack.",
      };
    case "provisioning":
    case "updating":
    case "destroying":
      return { label: "Change in progress", tone: "progress", description: `The stack is ${status}; wait for the request to finish.` };
    case "destroyed":
      return { label: "Destroyed", tone: "neutral", description: "The infrastructure was removed. The stack accepts no more requests." };
    default:
      return { label: status, tone: "neutral", description: "" };
  }
}

export const stackStatusLabel = (s: string): string => s.charAt(0).toUpperCase() + s.slice(1);

export interface OutputRow {
  name: string;
  /** The value as the API returned it, as text. Redacted values arrive redacted and stay so. */
  text: string;
}

/** outputRows lists the outputs sorted by name; strings are shown as-is, everything else as JSON. Pure. */
export function outputRows(outputs: Readonly<Record<string, unknown>> | null | undefined): OutputRow[] {
  return Object.entries(outputs ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => ({ name, text: typeof value === "string" ? value : (JSON.stringify(value) ?? "") }));
}

export interface ActionState {
  enabled: boolean;
  /** Why it is disabled; empty when enabled. */
  reason: string;
}

export interface StackActions {
  update: ActionState;
  destroy: ActionState;
}

const ok: ActionState = { enabled: true, reason: "" };

/**
 * stackActions says whether Update and Destroy are offered for stack: the caller's role first,
 * then the stack's state, mirroring what the API will check (stack_destroyed, stack_busy,
 * destroy_pending). The API stays authoritative; this only avoids offering a doomed action.
 * Pure.
 */
export function stackActions(stack: StackSummary, caps: Capabilities): StackActions {
  const state = (action: "update" | "destroy"): ActionState => {
    const d = decide(caps, action, stack);
    if (!d.allowed) return { enabled: false, reason: d.reason };
    if (stack.status === "destroyed") return { enabled: false, reason: "The stack was destroyed and accepts no more requests." };
    if (!EDITABLE.includes(stack.status)) {
      return { enabled: false, reason: `The stack is ${stack.status}; wait for the request in flight to finish.` };
    }
    const open = stack.latest_request;
    if (open && open.kind === "destroy" && !["applied", "failed", "rejected", "cancelled"].includes(open.status)) {
      return { enabled: false, reason: "A destroy request is already open for this stack." };
    }
    return ok;
  };
  return { update: state("update"), destroy: state("destroy") };
}
