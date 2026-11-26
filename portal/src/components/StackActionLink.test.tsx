import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StackActionLink } from "./StackActionLink";

describe("StackActionLink", () => {
  it("is a link when the action is available", () => {
    render(<StackActionLink href="/stacks/s1/update" label="Update" state={{ enabled: true, reason: "" }} />);
    expect(screen.getByRole("link", { name: "Update" })).toHaveAttribute("href", "/stacks/s1/update");
  });

  it("is a focusable, aria-disabled button with its reason when it is not", () => {
    render(
      <>
        <StackActionLink href="/x" label="Update" state={{ enabled: false, reason: "no" }} describedBy="why" />
        <p id="why">Update unavailable: no</p>
      </>,
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Update" });
    expect(button).toHaveAttribute("aria-disabled", "true");
    expect(button).not.toBeDisabled(); // stays reachable by keyboard
    expect(button).toHaveAccessibleDescription("Update unavailable: no");
  });
});
