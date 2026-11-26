import { describe, expect, it } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { ResourceInput, UpgradePath, UpgradeStep } from "@/lib/api/types";

import {
  buildUpgradePayload,
  explainUpgradeError,
  largestChange,
  planResourceUpgrade,
  upgradeLabel,
  upgradesAvailable,
  validateUpgrade,
} from "./upgrade";

const resource: ResourceInput = { name: "orders_queue", module: "sqs", version: "1.2.0", inputs: { name: "q" } };

const step = (version: string, change: UpgradeStep["change"], notes = ""): UpgradeStep => ({
  version,
  git_tag: `modules/sqs/v${version}`,
  status: "active",
  released_at: "2026-11-01T00:00:00Z",
  change,
  breaking: change === "major",
  changelog_md: `notes for ${version}`,
  upgrade_notes: notes,
});

const path = (over: Partial<UpgradePath> = {}): UpgradePath => ({
  module: "sqs",
  from: "1.2.0",
  to: "3.0.0",
  up_to_date: false,
  change: "major",
  majors_crossed: 2,
  next: "2.1.0",
  targets: { patch: "1.2.1", minor: "1.4.2", major: "3.0.0" },
  steps: [
    step("1.2.1", "patch"),
    step("1.4.2", "minor"),
    step("2.0.0", "major", "Rename dlq_name."),
    step("2.1.0", "minor"),
    step("3.0.0", "major", "Drop fifo."),
  ],
  ...over,
});

describe("planResourceUpgrade", () => {
  it("stops at the path's next version, one major at a time", () => {
    const u = planResourceUpgrade(resource, path());
    expect(u.target).toBe("2.1.0");
    expect(u.steps.map((s) => s.version)).toEqual(["1.2.1", "1.4.2", "2.0.0", "2.1.0"]);
    expect(u.change).toBe("major");
    expect(u.majorSteps.map((s) => s.upgrade_notes)).toEqual(["Rename dlq_name."]);
    expect(u.moreAfter).toBe(true);
  });

  it("offers a minor without major steps", () => {
    const p = path({ to: "1.4.2", next: "1.4.2", steps: [step("1.2.1", "patch"), step("1.4.2", "minor")] });
    const u = planResourceUpgrade(resource, p);
    expect(u).toMatchObject({ target: "1.4.2", change: "minor", majorSteps: [], moreAfter: false });
  });

  it("offers nothing when up to date or when the path is unknown", () => {
    const upToDate = planResourceUpgrade(resource, path({ up_to_date: true, next: null, steps: [] }));
    expect(upToDate).toMatchObject({ steps: [], change: "none" });
    expect(upToDate.target).toBeUndefined();
    const unknown = planResourceUpgrade(resource, undefined);
    expect(unknown).toMatchObject({ pinned: "1.2.0" });
    expect(unknown.target).toBeUndefined();
    expect(unknown.path).toBeUndefined();
  });
});

describe("labels", () => {
  it("names the class of the upgrade", () => {
    expect(upgradeLabel({ path: undefined, change: "none" })).toEqual({ text: "Upgrade status unknown", tone: "neutral" });
    expect(upgradeLabel({ path: path(), target: undefined, change: "none" }).text).toBe("Up to date");
    expect(upgradeLabel({ path: path(), target: "2.1.0", change: "major" })).toEqual({ text: "Major upgrade available", tone: "warning" });
    expect(upgradeLabel({ path: path(), target: "1.4.2", change: "minor" }).text).toBe("Upgrade available");
    expect(upgradeLabel({ path: path(), target: "1.2.1", change: "patch" }).text).toBe("Patch available");
  });

  it("counts and ranks", () => {
    expect(upgradesAvailable([{ target: "1.0.1" }, { target: undefined }, { target: "2.0.0" }])).toBe(2);
    expect(largestChange([{ change: "patch" }, { change: "minor" }])).toBe("minor");
    expect(largestChange([])).toBe("none");
  });
});

describe("buildUpgradePayload", () => {
  it("sends one upgrade, the acknowledgement only when given, and a trimmed justification", () => {
    expect(
      buildUpgradePayload({ stackId: "s1", resource: "orders_queue", toVersion: "2.1.0", justification: "  fixes ", acknowledgeBreaking: true }),
    ).toEqual({
      kind: "upgrade",
      stack_id: "s1",
      justification: "fixes",
      acknowledge_breaking: true,
      upgrades: [{ resource: "orders_queue", to_version: "2.1.0" }],
    });
    expect(
      buildUpgradePayload({ stackId: "s1", resource: "orders_queue", toVersion: "1.4.2", justification: " ", acknowledgeBreaking: false }),
    ).toEqual({ kind: "upgrade", stack_id: "s1", upgrades: [{ resource: "orders_queue", to_version: "1.4.2" }] });
  });
});

describe("validateUpgrade", () => {
  it("requires the acknowledgement for a major only", () => {
    expect(validateUpgrade(true, { acknowledged: false, justification: "" })).toEqual({
      acknowledged: "Confirm that you read the migration notes.",
    });
    expect(validateUpgrade(true, { acknowledged: true, justification: "" })).toEqual({});
    expect(validateUpgrade(false, { acknowledged: false, justification: "" })).toEqual({});
    expect(validateUpgrade(false, { acknowledged: false, justification: "x".repeat(2001) }).justification).toMatch(/at most/);
  });
});

describe("explainUpgradeError", () => {
  const err = (status: number, code: string) => new ApiError(parseProblem(status, { title: "t", status, code, detail: "d" }));

  it.each([
    [422, "acknowledge_breaking_required", "Confirm the major upgrade"],
    [422, "major_jump_too_large", "One major version at a time"],
    [422, "upgrade_requires_input_changes", "The current inputs do not fit the new version"],
    [422, "invalid_ref", "Another resource uses an output the new version removed"],
    [422, "module_version_unavailable", "That version is not available"],
    [422, "not_an_upgrade", "Already on this version or newer"],
    [409, "spec_changed", "The stack changed meanwhile"],
    [409, "no_current_spec", "Nothing to upgrade from"],
    [409, "stack_busy", "The stack is busy"],
    [403, "forbidden", "You are not allowed to do this"],
  ])("%i %s", (status, code, title) => {
    const e = explainUpgradeError(err(status, code));
    expect(e.title).toBe(title);
    expect(e.detail).toBe("d");
  });
});
