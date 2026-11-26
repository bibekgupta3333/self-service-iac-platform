import { describe, expect, it } from "vitest";

import {
  catalogStatusTone,
  categoriesOf,
  formatDateTime,
  httpsUrl,
  isTerminal,
  requestStatusLabel,
  requestStatusTone,
  shortSha,
  timeline,
} from "./format";

describe("formatDateTime", () => {
  it.each([
    ["2026-10-02T09:14:22Z", "2026-10-02 09:14 UTC"],
    ["2026-10-02T23:59:59-02:00", "2026-10-03 01:59 UTC"],
    [null, "—"],
    [undefined, "—"],
    ["not a date", "not a date"],
  ])("%s -> %s", (input, want) => expect(formatDateTime(input)).toBe(want));
});

describe("categoriesOf", () => {
  it("lists distinct categories in order", () => {
    expect(categoriesOf([{ category: "storage" }, { category: "compute" }, { category: "storage" }, { category: "" }])).toEqual([
      "compute",
      "storage",
    ]);
  });
});

describe("labels and tones", () => {
  it("shortens SHAs", () => expect(shortSha("9c1d4e5f6a7b8c9d")).toBe("9c1d4e5"));

  it("maps catalog statuses", () => {
    expect(catalogStatusTone("active")).toBe("success");
    expect(catalogStatusTone("deprecated")).toBe("warning");
    expect(catalogStatusTone("yanked")).toBe("danger");
  });

  it("maps request statuses and passes unknown ones through", () => {
    expect(requestStatusLabel("pr_open")).toBe("PR open");
    expect(requestStatusLabel("mystery")).toBe("mystery");
    expect(requestStatusTone("applied")).toBe("success");
    expect(requestStatusTone("applying")).toBe("progress");
    expect(requestStatusTone("failed")).toBe("danger");
    expect(requestStatusTone("cancelled")).toBe("warning");
    expect(requestStatusTone("plan_ready")).toBe("info");
    expect(requestStatusTone("draft")).toBe("neutral");
  });

  it("knows terminal statuses", () => {
    expect(isTerminal("applied")).toBe(true);
    expect(isTerminal("cancelled")).toBe(true);
    expect(isTerminal("validating")).toBe(false);
  });
});

describe("timeline", () => {
  const states = (s: string) => timeline(s).map((t) => `${t.status}:${t.state}`);

  it("marks earlier steps done and the current one current", () => {
    expect(states("validating")).toEqual([
      "draft:done",
      "pr_open:done",
      "validating:current",
      "plan_ready:upcoming",
      "approved:upcoming",
      "applying:upcoming",
      "applied:upcoming",
    ]);
  });

  it("marks every step done once applied", () => {
    expect(timeline("applied").every((t) => t.state === "done")).toBe(true);
  });

  it("appends the outcome when the request left the happy path", () => {
    const steps = timeline("failed");
    expect(steps).toHaveLength(7);
    expect(steps.slice(0, -1).every((t) => t.state === "unknown")).toBe(true);
    expect(steps.at(-1)).toEqual({ status: "failed", label: "Failed", state: "stopped" });
  });
});

describe("httpsUrl", () => {
  it("accepts only https://github.com/ pages", () => {
    expect(httpsUrl("https://github.com/o/r/pull/1")).toBe("https://github.com/o/r/pull/1");
    expect(httpsUrl("https://github.com/o/r/actions/runs/42")).toBe("https://github.com/o/r/actions/runs/42");
    expect(httpsUrl("https://example.com/o/r/pull/1")).toBeNull();
    expect(httpsUrl("https://github.com.evil.example/o/r/pull/1")).toBeNull();
    expect(httpsUrl("https://evil.example/github.com/o/r")).toBeNull();
    expect(httpsUrl("https://api.github.com/repos/o/r")).toBeNull();
    expect(httpsUrl("https://github.com:8443/o/r/pull/1")).toBeNull();
    expect(httpsUrl("https://user:pw@github.com/o/r/pull/1")).toBeNull();
    expect(httpsUrl("https://github.com/")).toBeNull();
    expect(httpsUrl("http://github.com/o/r/pull/1")).toBeNull();
    expect(httpsUrl("http://example.com/x")).toBeNull();
    expect(httpsUrl("javascript:alert(1)")).toBeNull();
    expect(httpsUrl("/relative")).toBeNull();
    expect(httpsUrl("")).toBeNull();
    expect(httpsUrl(undefined)).toBeNull();
  });
});
