// Pure logic for the update and destroy flows (docs/06 sections 1 and 5; backend
// internal/requests spec.go and stackstate.go): request bodies, the destroy confirmation
// checks, picking the stack's current spec, and plain-language explanations of the API's
// conflict errors.

import type { ApiError } from "@/lib/api/errors";
import type {
  DestroyRequestInput,
  RequestStatus,
  RequestSummary,
  ResourceInput,
  StackSummary,
  UpdateRequestInput,
} from "@/lib/api/types";

import { MAX_JUSTIFICATION } from "./form";

/**
 * buildUpdatePayload assembles the update body: the stack's resources with the edited one's
 * inputs replaced. The module and version of every resource are kept as they are. Pure.
 */
export function buildUpdatePayload(args: {
  stackId: string;
  resources: readonly ResourceInput[];
  editedIndex: number;
  inputs: Record<string, unknown>;
  justification: string;
  allowRemovals: boolean;
}): UpdateRequestInput {
  const justification = args.justification.trim();
  return {
    kind: "update",
    stack_id: args.stackId,
    ...(justification ? { justification } : {}),
    ...(args.allowRemovals ? { allow_removals: true } : {}),
    resources: args.resources.map((r, i) => (i === args.editedIndex ? { ...r, inputs: args.inputs } : r)),
  };
}

/** buildDestroyPayload assembles the destroy body; the justification is trimmed. Pure. */
export function buildDestroyPayload(stackId: string, confirmStackName: string, justification: string): DestroyRequestInput {
  return { kind: "destroy", stack_id: stackId, confirm_stack_name: confirmStackName, justification: justification.trim() };
}

export interface DestroyFields {
  confirmName: string;
  justification: string;
}

export type DestroyErrors = Partial<Record<keyof DestroyFields, string>>;

/**
 * validateDestroy checks the confirmation. The typed name must equal the stack's name exactly
 * (case and whitespace included, as the API compares it), and a justification is required.
 * Pure.
 */
export function validateDestroy(stackName: string, f: DestroyFields): DestroyErrors {
  const errors: DestroyErrors = {};
  if (f.confirmName !== stackName) errors.confirmName = `Type ${stackName} exactly to confirm.`;
  const why = f.justification.trim();
  if (why === "") errors.justification = "A justification is required.";
  else if (f.justification.length > MAX_JUSTIFICATION) errors.justification = `Must be at most ${MAX_JUSTIFICATION} characters.`;
  return errors;
}

/** nameMatches reports whether the typed text is exactly the stack's name; it enables the button. Pure. */
export const nameMatches = (stackName: string, typed: string): boolean => typed === stackName;

const SPEC_KINDS = ["create", "update", "upgrade"];
const DEAD = ["failed", "rejected", "cancelled"];

/**
 * carriesCurrentSpec reports whether a request's spec is part of the stack's current spec: a
 * create, update or upgrade that did not fail, get rejected or get cancelled. This is the
 * rule the backend uses to decide what an update would remove. Pure.
 */
export const carriesCurrentSpec = (r: { kind: string; status: RequestStatus | string }): boolean =>
  SPEC_KINDS.includes(r.kind) && !DEAD.includes(r.status);

/**
 * currentSpecRequest picks the request whose spec is the stack's current one: the stack's
 * latest request when it carries a spec, otherwise the newest earlier one that does.
 * `recent` is a newest-first list of requests (any stack); only this stack's are considered.
 * Returns undefined when none is found. Pure.
 */
export function currentSpecRequest(
  stack: Pick<StackSummary, "id" | "latest_request">,
  recent: readonly Pick<RequestSummary, "id" | "kind" | "status" | "stack">[],
): string | undefined {
  const latest = stack.latest_request;
  if (latest && carriesCurrentSpec(latest)) return latest.id;
  return recent.find((r) => r.stack.id === stack.id && carriesCurrentSpec(r))?.id;
}

export interface Explained {
  /** Short heading. */
  title: string;
  /** The API's own detail, when it adds something. */
  detail?: string;
  /** What to do next. */
  hint: string;
}

/**
 * explainLifecycleError turns the API's refusal of an update or destroy into a heading and a
 * next step. Anything it does not know keeps the API's title and detail. Pure.
 */
export function explainLifecycleError(err: ApiError, action: "update" | "destroy"): Explained {
  const detail = err.problem.detail;
  switch (err.code) {
    case "stack_busy":
      return {
        title: "The stack is busy",
        detail,
        hint: "A change is still in flight for this stack. Open the stack, wait for its latest request to finish, then try again.",
      };
    case "destroy_pending":
      return {
        title: "A destroy is already pending",
        detail,
        hint: "A destroy request is open for this stack, so it accepts no further updates or destroys. Finish or cancel that request first.",
      };
    case "stack_destroyed":
      return {
        title: "The stack was destroyed",
        detail,
        hint: "A destroyed stack accepts no requests. To run this infrastructure again, create a new stack from the catalog.",
      };
    case "resources_removed":
      return {
        title: "This update would remove resources",
        detail,
        hint: "Resources the stack has now are missing from this update, so merging it destroys them. If that is intended, tick the acknowledgement and submit again.",
      };
    default:
  }
  if (err.status === 404) {
    return {
      title: "Stack not found",
      detail,
      hint: "It may not exist, or you may not be a member of its team. Go back to the stacks list.",
    };
  }
  if (err.status === 403) {
    return {
      title: "You are not allowed to do this",
      detail,
      hint:
        action === "destroy"
          ? "Destroying a production stack needs the maintainer role in its team; other environments need developer."
          : "Updating a stack needs the developer role in its team.",
    };
  }
  return { title: err.problem.title, detail, hint: "" };
}

/**
 * removedResourceNames extracts the resource names from a resources_removed detail
 * ("this update would destroy a, b, which the stack's current spec has; ..."). Returns []
 * when the text is not in that shape. Pure.
 */
export function removedResourceNames(detail: string | undefined): string[] {
  const m = detail ? /would destroy (.+?), which the stack's current spec has/.exec(detail) : null;
  return m?.[1] ? m[1].split(",").map((s) => s.trim()).filter(Boolean) : [];
}
