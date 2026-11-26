import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StatusTimeline } from "./StatusTimeline";

describe("StatusTimeline", () => {
  it("marks the current step for assistive technology", () => {
    render(<StatusTimeline status="plan_ready" />);
    const list = screen.getByRole("list", { name: "Request progress" });
    const items = within(list).getAllByRole("listitem");
    expect(items).toHaveLength(7);
    const current = items.find((li) => li.getAttribute("aria-current") === "step");
    expect(current).toHaveTextContent("Plan ready (current step)");
    expect(items[0]).toHaveTextContent("Draft (completed)");
    expect(items[6]).toHaveTextContent("Applied (not started)");
  });

  it("shows the outcome of a request that left the happy path", () => {
    render(<StatusTimeline status="cancelled" />);
    expect(screen.getByText("Cancelled")).toHaveTextContent("Cancelled (outcome)");
    expect(screen.getByText("Draft")).toHaveTextContent("Draft (not recorded)");
  });
});
