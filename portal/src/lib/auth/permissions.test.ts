import { describe, expect, it } from "vitest";

import { atLeast, type Capabilities, decide, requiredRole, type Role, type StackAction } from "./permissions";

const teams = (roles: Record<string, string>, admin = false): Capabilities => ({ kind: "teams", admin, roles });

describe("requiredRole", () => {
  it.each<[StackAction, string, Role]>([
    ["update", "development", "developer"],
    ["update", "production", "developer"],
    ["destroy", "development", "developer"],
    ["destroy", "staging", "developer"],
    ["destroy", "production", "maintainer"],
    ["destroy", "prod", "maintainer"], // an unknown environment counts as production
    ["destroy", "", "maintainer"],
  ])("%s in %s needs %s", (action, env, want) => {
    expect(requiredRole(action, env)).toBe(want);
  });
});

describe("atLeast", () => {
  it("orders the roles and grants nothing for unknown ones", () => {
    expect(atLeast("admin", "maintainer")).toBe(true);
    expect(atLeast("developer", "developer")).toBe(true);
    expect(atLeast("viewer", "developer")).toBe(false);
    expect(atLeast("owner", "viewer")).toBe(false);
    expect(atLeast(undefined, "viewer")).toBe(false);
  });
});

describe("decide", () => {
  const prod = { team: "payments", environment: "production" };
  const dev = { team: "payments", environment: "development" };

  it("allows everything when unrestricted (dev auth)", () => {
    expect(decide({ kind: "unrestricted" }, "destroy", prod).allowed).toBe(true);
  });

  it("allows a platform admin everywhere", () => {
    expect(decide(teams({}, true), "destroy", prod).allowed).toBe(true);
  });

  it("matches the backend matrix for developers", () => {
    const caps = teams({ payments: "developer" });
    expect(decide(caps, "update", prod).allowed).toBe(true);
    expect(decide(caps, "destroy", dev).allowed).toBe(true);
    const denied = decide(caps, "destroy", prod);
    expect(denied.allowed).toBe(false);
    expect(denied.need).toBe("maintainer");
    expect(denied.reason).toContain("maintainer role or higher");
  });

  it("denies viewers and non-members with a reason", () => {
    expect(decide(teams({ payments: "viewer" }), "update", dev)).toMatchObject({ allowed: false, need: "developer" });
    const other = decide(teams({ billing: "admin" }), "update", dev);
    expect(other.allowed).toBe(false);
    expect(other.reason).toContain("not a member of payments");
  });

  it("allows a maintainer to destroy in production", () => {
    expect(decide(teams({ payments: "maintainer" }), "destroy", prod).allowed).toBe(true);
  });
});
