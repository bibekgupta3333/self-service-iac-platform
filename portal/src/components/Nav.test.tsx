import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { isActive, Nav } from "./Nav";

vi.mock("next/navigation", () => ({ usePathname: () => "/catalog/sqs" }));

describe("isActive", () => {
  it.each([
    ["/catalog", "/catalog", true],
    ["/catalog/sqs", "/catalog", true],
    ["/catalogue", "/catalog", false],
    ["/requests", "/catalog", false],
  ])("isActive(%s, %s) = %s", (pathname, href, want) => {
    expect(isActive(pathname, href)).toBe(want);
  });
});

describe("Nav", () => {
  it("marks the current section with aria-current", () => {
    render(<Nav />);
    expect(screen.getByRole("link", { name: "Catalog" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Requests" })).not.toHaveAttribute("aria-current");
  });
});
