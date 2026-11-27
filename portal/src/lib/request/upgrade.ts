// Pure logic for module version upgrades (docs/06 section 1; backend internal/catalog
// upgrade.go and internal/requests upgrade.go): what the stack page offers per resource, the
// request body, the confirmation rules and plain-language explanations of refusals.

import type { ApiError } from "@/lib/api/errors";
import type { ResourceInput, UpgradeChange, UpgradePath, UpgradeRequestInput, UpgradeStep } from "@/lib/api/types";
import type { Tone } from "@/lib/format";

import { explainLifecycleError, type Explained } from "./lifecycle";
import { MAX_JUSTIFICATION } from "./form";

/** One resource of the stack with its pinned version and, when it could be loaded, its upgrade path. */
export interface ResourceUpgrade {
  resource: string;
  module: string;
  pinned: string;
  /** undefined when the path could not be loaded (the pinned version is no longer in the catalog, say). */
  path?: UpgradePath;
  /** The version the next upgrade request pins (one major at a time); undefined when up to date. */
  target?: string;
  /** The steps from the pinned version up to and including target, oldest first. */
  steps: UpgradeStep[];
  /** The class of the move to target. */
  change: UpgradeChange;
  /** The steps up to target that cross a major; their migration notes must be read. */
  majorSteps: UpgradeStep[];
  /** True when versions exist beyond target (another major), reached by a later upgrade. */
  moreAfter: boolean;
}

const RANK: Record<UpgradeChange, number> = { none: 0, patch: 1, minor: 2, major: 3 };

/** largestChange is the biggest SemVer class among steps. Pure. */
export const largestChange = (steps: readonly Pick<UpgradeStep, "change">[]): UpgradeChange =>
  steps.reduce<UpgradeChange>((acc, s) => (RANK[s.change] > RANK[acc] ? s.change : acc), "none");

/**
 * planResourceUpgrade decides what the stack page offers for one resource: the target of the
 * next upgrade request (the path's `next`, which never crosses more than one major), the steps
 * up to it and which of them are majors. Pure.
 */
export function planResourceUpgrade(resource: ResourceInput, path: UpgradePath | undefined): ResourceUpgrade {
  const base = { resource: resource.name, module: resource.module, pinned: resource.version, path };
  if (!path || path.up_to_date || !path.next) {
    return { ...base, steps: [], change: "none", majorSteps: [], moreAfter: false };
  }
  const end = path.steps.findIndex((s) => s.version === path.next);
  const steps = end >= 0 ? path.steps.slice(0, end + 1) : [];
  return {
    ...base,
    target: path.next,
    steps,
    change: largestChange(steps),
    majorSteps: steps.filter((s) => s.breaking),
    moreAfter: end >= 0 && end < path.steps.length - 1,
  };
}

/** upgradeLabel is the badge text for a resource. Pure. */
export function upgradeLabel(u: Pick<ResourceUpgrade, "target" | "change" | "path">): { text: string; tone: Tone } {
  if (!u.path) return { text: "Upgrade status unknown", tone: "neutral" };
  if (!u.target) return { text: "Up to date", tone: "success" };
  switch (u.change) {
    case "major":
      return { text: "Major upgrade available", tone: "warning" };
    case "minor":
      return { text: "Upgrade available", tone: "info" };
    default:
      return { text: "Patch available", tone: "info" };
  }
}

/** upgradesAvailable counts the resources that have a target. Pure. */
export const upgradesAvailable = (us: readonly Pick<ResourceUpgrade, "target">[]): number =>
  us.filter((u) => u.target !== undefined).length;

/** buildUpgradePayload assembles the upgrade body for one resource; the justification is trimmed. Pure. */
export function buildUpgradePayload(args: {
  stackId: string;
  resource: string;
  toVersion: string;
  justification: string;
  acknowledgeBreaking: boolean;
}): UpgradeRequestInput {
  const justification = args.justification.trim();
  return {
    kind: "upgrade",
    stack_id: args.stackId,
    ...(justification ? { justification } : {}),
    ...(args.acknowledgeBreaking ? { acknowledge_breaking: true } : {}),
    upgrades: [{ resource: args.resource, to_version: args.toVersion }],
  };
}

export interface UpgradeFields {
  acknowledged: boolean;
  justification: string;
}

export type UpgradeErrors = Partial<Record<keyof UpgradeFields, string>>;

/** validateUpgrade checks the confirmation: a major needs the acknowledgement. Pure. */
export function validateUpgrade(breaking: boolean, f: UpgradeFields): UpgradeErrors {
  const errors: UpgradeErrors = {};
  if (breaking && !f.acknowledged) errors.acknowledged = "Confirm that you read the migration notes.";
  if (f.justification.length > MAX_JUSTIFICATION) errors.justification = `Must be at most ${MAX_JUSTIFICATION} characters.`;
  return errors;
}

/**
 * explainUpgradeError turns the API's refusal of an upgrade into a heading and a next step;
 * the stack-state refusals are the update ones. Pure.
 */
export function explainUpgradeError(err: ApiError): Explained {
  const detail = err.problem.detail;
  switch (err.code) {
    case "acknowledge_breaking_required":
      return { title: "Confirm the major upgrade", detail, hint: "Read the migration notes, tick the confirmation and submit again." };
    case "major_jump_too_large":
      return {
        title: "One major version at a time",
        detail,
        hint: "Upgrade to the newest version of the next major first; the following major is a separate request.",
      };
    case "upgrade_requires_input_changes":
      return {
        title: "The current inputs do not fit the new version",
        detail,
        hint: "Update the stack's inputs as the migration notes describe, then upgrade.",
      };
    case "invalid_ref":
      return {
        title: "Another resource uses an output the new version removed",
        detail,
        hint: "Update the resource that references it first, then upgrade.",
      };
    case "module_version_unavailable":
      return { title: "That version is not available", detail, hint: "It may have been yanked. Reload the page to see the current path." };
    case "not_an_upgrade":
      return { title: "Already on this version or newer", detail, hint: "Reload the page; another request may have upgraded the stack." };
    case "spec_changed":
      return { title: "The stack changed meanwhile", detail, hint: "Another request changed the stack. Reload the page and try again." };
    case "no_current_spec":
      return { title: "Nothing to upgrade from", detail, hint: "The stack has no current settings. Use Update to set them." };
    default:
      return explainLifecycleError(err, "update");
  }
}
