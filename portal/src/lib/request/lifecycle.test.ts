import { describe, expect, it } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";

import {
  buildDestroyPayload,
  buildUpdatePayload,
  carriesCurrentSpec,
  currentSpecRequest,
  explainLifecycleError,
  nameMatches,
  removedResourceNames,
  validateDestroy,
} from "./lifecycle";

const problem = (status: number, code: string, detail?: string) =>
  new ApiError(parseProblem(status, { title: `title-${code}`, status, code, detail }));

describe("buildUpdatePayload", () => {
  const resources = [
    { name: "queue", module: "sqs", version: "1.0.0", inputs: { name: "orders" } },
    { name: "fn", module: "lambda", version: "2.1.0", inputs: { runtime: "python3.12" } },
  ];

  it("replaces only the edited resource's inputs and keeps the rest", () => {
    const body = buildUpdatePayload({
      stackId: "sid",
      resources,
      editedIndex: 0,
      inputs: { name: "orders", fifo: true },
      justification: "  tune it ",
      allowRemovals: false,
    });
    expect(body).toEqual({
      kind: "update",
      stack_id: "sid",
      justification: "tune it",
      resources: [{ ...resources[0], inputs: { name: "orders", fifo: true } }, resources[1]],
    });
    expect(resources[0]?.inputs).toEqual({ name: "orders" }); // the input was not mutated
  });

  it("sends allow_removals only when acknowledged and omits an empty justification", () => {
    const base = { stackId: "sid", resources, editedIndex: 1, inputs: {}, justification: " " };
    expect(buildUpdatePayload({ ...base, allowRemovals: false })).not.toHaveProperty("allow_removals");
    expect(buildUpdatePayload({ ...base, allowRemovals: false })).not.toHaveProperty("justification");
    expect(buildUpdatePayload({ ...base, allowRemovals: true }).allow_removals).toBe(true);
  });
});

describe("destroy confirmation", () => {
  it("builds the body without resources", () => {
    expect(buildDestroyPayload("sid", "orders", "  retired  ")).toEqual({
      kind: "destroy",
      stack_id: "sid",
      confirm_stack_name: "orders",
      justification: "retired",
    });
  });

  it("requires the exact name, case and whitespace included", () => {
    expect(nameMatches("orders", "orders")).toBe(true);
    expect(nameMatches("orders", "Orders")).toBe(false);
    expect(nameMatches("orders", "orders ")).toBe(false);
    expect(nameMatches("orders", "")).toBe(false);
  });

  it("requires a justification", () => {
    expect(validateDestroy("orders", { confirmName: "orders", justification: "retired" })).toEqual({});
    expect(validateDestroy("orders", { confirmName: "orders", justification: "   " }).justification).toBe("A justification is required.");
    expect(validateDestroy("orders", { confirmName: "order", justification: "x" }).confirmName).toBe("Type orders exactly to confirm.");
    expect(validateDestroy("orders", { confirmName: "orders", justification: "x".repeat(2001) }).justification).toContain("at most 2000");
  });
});

describe("currentSpecRequest", () => {
  const stack = (latest: { id: string; kind: string; status: string } | null) =>
    ({ id: "s1", latest_request: latest && { ...latest, short_id: "R", created_at: "t" } }) as never;
  const req = (id: string, kind: string, status: string, stackId = "s1") =>
    ({ id, kind, status, stack: { id: stackId, path: "", status: "" } }) as never;

  it.each([
    ["create", "applied", true],
    ["update", "pr_open", true],
    ["upgrade", "applying", true],
    ["destroy", "applied", false],
    ["update", "failed", false],
    ["create", "rejected", false],
    ["update", "cancelled", false],
    ["reconcile", "applied", false],
  ])("%s %s carries the spec: %s", (kind, status, want) => {
    expect(carriesCurrentSpec({ kind, status })).toBe(want);
  });

  it("uses the latest request when it carries a spec", () => {
    expect(currentSpecRequest(stack({ id: "r9", kind: "update", status: "applied" }), [req("r1", "create", "applied")])).toBe("r9");
  });

  it("falls back to the newest earlier request of this stack that carries one", () => {
    const recent = [
      req("r8", "update", "failed"),
      req("x1", "update", "applied", "other-stack"),
      req("r7", "update", "applied"),
      req("r1", "create", "applied"),
    ];
    expect(currentSpecRequest(stack({ id: "r9", kind: "destroy", status: "cancelled" }), recent)).toBe("r7");
  });

  it("returns undefined when nothing carries a spec", () => {
    expect(currentSpecRequest(stack(null), [req("r1", "destroy", "applied")])).toBeUndefined();
  });
});

describe("explainLifecycleError", () => {
  it.each([
    ["stack_busy", "The stack is busy", /wait for its latest request/],
    ["destroy_pending", "A destroy is already pending", /no further updates or destroys/],
    ["stack_destroyed", "The stack was destroyed", /create a new stack/],
    ["resources_removed", "This update would remove resources", /acknowledgement/],
  ])("explains %s", (code, title, hint) => {
    const e = explainLifecycleError(problem(409, code, "api detail"), "update");
    expect(e.title).toBe(title);
    expect(e.hint).toMatch(hint);
    expect(e.detail).toBe("api detail");
  });

  it("explains 404 and 403 by status, with role advice per action", () => {
    expect(explainLifecycleError(problem(404, "not_found"), "update").title).toBe("Stack not found");
    expect(explainLifecycleError(problem(403, "forbidden"), "destroy").hint).toContain("maintainer");
    expect(explainLifecycleError(problem(403, "forbidden"), "update").hint).toContain("developer");
  });

  it("keeps the API's words for anything else", () => {
    expect(explainLifecycleError(problem(422, "validation_failed", "bad"), "update")).toEqual({
      title: "title-validation_failed",
      detail: "bad",
      hint: "",
    });
  });
});

describe("removedResourceNames", () => {
  it("reads the names out of the API's message", () => {
    expect(removedResourceNames("this update would destroy queue, fn, which the stack's current spec has; if that is intended, send allow_removals: true")).toEqual([
      "queue",
      "fn",
    ]);
    expect(removedResourceNames("something else")).toEqual([]);
    expect(removedResourceNames(undefined)).toEqual([]);
  });
});
