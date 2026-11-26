import { describe, expect, it } from "vitest";

import type { StackSummary } from "@/lib/api/types";
import type { Capabilities } from "@/lib/auth/permissions";

import { outputRows, parseStackQuery, stackActions, stackHealth, stacksHref } from "./stacks";

const stack = (over: Partial<StackSummary> = {}): StackSummary => ({
  id: "7b1c0a52-6c11-4f0e-b9b6-5f2f3c8d1a90",
  team: "payments",
  environment: "development",
  name: "orders",
  path: "environments/development/payments/orders",
  region: "us-east-1",
  status: "active",
  is_ephemeral: false,
  expires_at: null,
  outputs: {},
  latest_request: null,
  created_at: "2026-03-01T10:00:00Z",
  updated_at: "2026-03-01T10:00:00Z",
  links: { self: "", latest_request: null },
  ...over,
});

describe("parseStackQuery", () => {
  it("keeps valid filters and trims them", () => {
    expect(parseStackQuery({ team: " payments ", env: "production", status: "drifted", cursor: "abc" })).toEqual({
      team: "payments",
      env: "production",
      status: "drifted",
      cursor: "abc",
    });
  });

  it("drops what the API would refuse, empty values and repeats", () => {
    expect(parseStackQuery({ env: "prod", status: "weird", team: "", cursor: " " })).toEqual({});
    expect(parseStackQuery({ team: ["a", "b"] })).toEqual({ team: "a" });
    expect(parseStackQuery({})).toEqual({});
  });
});

describe("stacksHref", () => {
  it("encodes only the set filters", () => {
    expect(stacksHref({})).toBe("/stacks");
    expect(stacksHref({ team: "a b", status: "failed", cursor: "x=" })).toBe("/stacks?team=a+b&status=failed&cursor=x%3D");
  });
});

describe("stackHealth", () => {
  it.each([
    ["active", "Healthy", "success"],
    ["drifted", "Drifted", "warning"],
    ["failed", "Failed", "danger"],
    ["updating", "Change in progress", "progress"],
    ["destroyed", "Destroyed", "neutral"],
    ["mystery", "mystery", "neutral"],
  ])("%s is %s", (status, label, tone) => {
    expect(stackHealth(status)).toMatchObject({ label, tone });
  });
});

describe("outputRows", () => {
  it("sorts by name and shows values as returned", () => {
    expect(outputRows({ url: "https://q", arn: "arn:aws:sqs:x", secret: "(sensitive value redacted)", n: 3, tags: { a: "b" } })).toEqual([
      { name: "arn", text: "arn:aws:sqs:x" },
      { name: "n", text: "3" },
      { name: "secret", text: "(sensitive value redacted)" },
      { name: "tags", text: '{"a":"b"}' },
      { name: "url", text: "https://q" },
    ]);
  });

  it("copes with no outputs", () => {
    expect(outputRows(undefined)).toEqual([]);
    expect(outputRows({})).toEqual([]);
  });
});

describe("stackActions", () => {
  const unrestricted: Capabilities = { kind: "unrestricted" };

  it("offers both actions on an active stack", () => {
    expect(stackActions(stack(), unrestricted)).toEqual({ update: { enabled: true, reason: "" }, destroy: { enabled: true, reason: "" } });
  });

  it.each(["drifted", "failed"] as const)("still offers them on a %s stack", (status) => {
    const a = stackActions(stack({ status }), unrestricted);
    expect(a.update.enabled && a.destroy.enabled).toBe(true);
  });

  it.each(["provisioning", "updating", "destroying"] as const)("disables both while %s", (status) => {
    const a = stackActions(stack({ status }), unrestricted);
    expect(a.update).toMatchObject({ enabled: false });
    expect(a.update.reason).toContain("wait for the request in flight");
    expect(a.destroy.enabled).toBe(false);
  });

  it("disables both on a destroyed stack", () => {
    const a = stackActions(stack({ status: "destroyed" }), unrestricted);
    expect(a.update.reason).toContain("destroyed");
    expect(a.destroy.enabled).toBe(false);
  });

  it("disables both while a destroy request is open, but not after it finished", () => {
    const open = { id: "r", short_id: "REQ-9", kind: "destroy" as const, status: "pr_open" as const, created_at: "t" };
    expect(stackActions(stack({ latest_request: open }), unrestricted).update.reason).toContain("destroy request is already open");
    expect(stackActions(stack({ latest_request: { ...open, status: "failed" } }), unrestricted).update.enabled).toBe(true);
  });

  it("applies the role first: a developer cannot destroy in production", () => {
    const dev: Capabilities = { kind: "teams", admin: false, roles: { payments: "developer" } };
    const a = stackActions(stack({ environment: "production" }), dev);
    expect(a.update.enabled).toBe(true);
    expect(a.destroy).toMatchObject({ enabled: false });
    expect(a.destroy.reason).toContain("maintainer");
  });

  it("mirrors the role check for an unknown environment as production", () => {
    const dev: Capabilities = { kind: "teams", admin: false, roles: { payments: "developer" } };
    const a = stackActions(stack({ environment: "sandbox" }), dev);
    expect(a.update.enabled).toBe(true);
    expect(a.destroy.enabled).toBe(false);
    expect(a.destroy.reason).toContain("needs the maintainer role");
    const maintainer: Capabilities = { kind: "teams", admin: false, roles: { payments: "maintainer" } };
    expect(stackActions(stack({ environment: "sandbox" }), maintainer).destroy.enabled).toBe(true);
  });

  it("denies a viewer both", () => {
    const viewer: Capabilities = { kind: "teams", admin: false, roles: { payments: "viewer" } };
    const a = stackActions(stack(), viewer);
    expect(a.update.enabled || a.destroy.enabled).toBe(false);
  });
});
