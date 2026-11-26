// What the signed-in user may do, for hiding or disabling actions in the UI. This mirrors the
// backend's matrix (backend/internal/rbac, docs/07 section 7) for the two stack actions the
// portal offers. It is a convenience, never a control: the API decides, and the portal still
// shows its 403 when the two disagree.

export type Role = "viewer" | "developer" | "maintainer" | "admin";

const ORDER: readonly Role[] = ["viewer", "developer", "maintainer", "admin"];

/**
 * Capabilities is what the portal knows about the caller.
 * - unrestricted: the dev auth mode, where every call acts as one local user the portal cannot
 *   inspect; actions are offered and the API answers.
 * - teams: the GitHub mode, from GET /v1/me.
 */
export type Capabilities =
  | { kind: "unrestricted" }
  | { kind: "teams"; admin: boolean; roles: Readonly<Record<string, string>> };

export type StackAction = "update" | "destroy";

export interface ActionDecision {
  allowed: boolean;
  /** The role the action needs in the stack's team and environment. */
  need: Role;
  /** Why it is not allowed; empty when it is. */
  reason: string;
}

const NON_PROD = new Set(["development", "staging"]);

/** requiredRole is the least role that may perform action in environment. Unknown environments count as production. Pure. */
export function requiredRole(action: StackAction, environment: string): Role {
  if (action === "update") return "developer";
  return NON_PROD.has(environment) ? "developer" : "maintainer";
}

const isRole = (r: string | undefined): r is Role => ORDER.includes(r as Role);

/** atLeast reports whether role is at or above need; unknown roles grant nothing. Pure. */
export const atLeast = (role: string | undefined, need: Role): boolean =>
  isRole(role) && ORDER.indexOf(role) >= ORDER.indexOf(need);

/** decide answers whether the caller may perform action on a stack of team in environment. Pure. */
export function decide(
  caps: Capabilities,
  action: StackAction,
  stack: { team: string; environment: string },
): ActionDecision {
  const need = requiredRole(action, stack.environment);
  if (caps.kind === "unrestricted" || caps.admin) return { allowed: true, need, reason: "" };
  const role = caps.roles[stack.team];
  if (atLeast(role, need)) return { allowed: true, need, reason: "" };
  const verb = action === "update" ? "update" : "destroy";
  return {
    allowed: false,
    need,
    reason: role
      ? `Your ${role} role in ${stack.team} cannot ${verb} ${stack.environment} stacks; it needs the ${need} role or higher.`
      : `You are not a member of ${stack.team}; ${verb} needs the ${need} role or higher.`,
  };
}
