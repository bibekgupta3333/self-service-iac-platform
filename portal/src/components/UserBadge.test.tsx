import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { UserBadge } from "./UserBadge";

describe("UserBadge", () => {
  it("keeps the dev badge and shows no sign-in or sign-out controls in dev mode", () => {
    render(<UserBadge viewer={{ login: "local-dev", displayName: "local-dev", mode: "dev" }} />);
    expect(screen.getByText("dev auth")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /sign out/i })).toBeNull();
    expect(screen.queryByRole("link", { name: /sign in/i })).toBeNull();
  });

  it("offers GitHub sign-in through the same-origin login route when anonymous", () => {
    render(<UserBadge viewer={{ login: "", displayName: "", mode: "github", anonymous: true }} />);
    const link = screen.getByRole("link", { name: /sign in with github/i });
    expect(link).toHaveAttribute("href", "/api/iacp/v1/auth/login?return_to=%2F");
  });

  it("shows the signed-in user and a sign-out button", () => {
    render(<UserBadge viewer={{ login: "priya", displayName: "Priya S.", mode: "github" }} />);
    expect(screen.getByText("Priya S.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /sign out/i })).toBeInTheDocument();
    expect(screen.queryByText("dev auth")).toBeNull();
  });
});
