import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { CreateRequestResult, RequestInput, RequestSummary, UpgradePath, UpgradeStep } from "@/lib/api/types";
import { planResourceUpgrade } from "@/lib/request/upgrade";

import { UpgradeResource } from "./UpgradeResource";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));

const SID = "7b1c0a52-6c11-4f0e-b9b6-5f2f3c8d1a90";
const stack = { id: SID, name: "orders", environment: "development" };
const enabled = { enabled: true, reason: "" };
const created = { id: "r1", status: "draft" } as RequestSummary;
const ok = async () => ({ request: created, replayed: false });

const step = (version: string, change: UpgradeStep["change"], notes = ""): UpgradeStep => ({
  version,
  git_tag: `modules/sqs/v${version}`,
  status: "active",
  released_at: "2026-11-01T00:00:00Z",
  change,
  breaking: change === "major",
  changelog_md: `### Changed\n- ${version}`,
  upgrade_notes: notes,
});

const path = (steps: UpgradeStep[], next: string | null, to = next ?? "1.2.0"): UpgradePath => ({
  module: "sqs",
  from: "1.2.0",
  to,
  up_to_date: steps.length === 0,
  change: "minor",
  majors_crossed: 0,
  next,
  targets: { patch: null, minor: null, major: null },
  steps,
});

const resource = { name: "orders_queue", module: "sqs", version: "1.2.0", inputs: {} };
const minor = planResourceUpgrade(resource, path([step("1.2.1", "patch"), step("1.4.0", "minor")], "1.4.0"));
const major = planResourceUpgrade(
  resource,
  path([step("1.4.0", "minor"), step("2.0.0", "major", "Set `dlq_name` before upgrading."), step("3.0.0", "major", "later")], "2.0.0", "3.0.0"),
);

function setup(upgrade = minor, createRequest: (i: RequestInput, k: string) => Promise<CreateRequestResult> = ok, state = enabled) {
  const client = { createRequest: vi.fn(createRequest) };
  const onCreated = vi.fn();
  const user = userEvent.setup();
  let n = 0;
  render(
    <UpgradeResource stack={stack} upgrade={upgrade} state={state} client={client} newIdempotencyKey={() => `key-${++n}`} onCreated={onCreated} />,
  );
  return { client, onCreated, user };
}

describe("UpgradeResource", () => {
  it("shows the path with the class of each step", () => {
    setup();
    expect(screen.getByText("Upgrade available")).toBeInTheDocument();
    const list = screen.getByRole("list", { name: "Upgrade path of orders_queue" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["1.2.0", "→1.2.1patch", "→1.4.0minor"]);
    expect(screen.queryByLabelText("Migration notes")).not.toBeInTheDocument();
  });

  it("says when a resource is up to date and offers no action", () => {
    setup(planResourceUpgrade(resource, path([], null)));
    expect(screen.getByText("Up to date")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("submits a minor upgrade with an Idempotency-Key and reuses it for a retry of the same body", async () => {
    let calls = 0;
    const { client, onCreated, user } = setup(minor, async () => {
      calls++;
      if (calls === 1) throw ApiError.network(new Error("offline"));
      return { request: created, replayed: true };
    });
    await user.click(screen.getByRole("button", { name: "Upgrade to 1.4.0" }));
    const dialog = screen.getByRole("dialog", { name: "Upgrade orders_queue to sqs 1.4.0?" });
    await user.click(within(dialog).getByRole("button", { name: "Open upgrade request" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/may have received the request/);
    await user.click(within(dialog).getByRole("button", { name: "Open upgrade request" }));
    expect(client.createRequest).toHaveBeenCalledTimes(2);
    const [body, key] = client.createRequest.mock.calls[0] as [RequestInput, string];
    expect(body).toEqual({ kind: "upgrade", stack_id: SID, upgrades: [{ resource: "orders_queue", to_version: "1.4.0" }] });
    expect(key).toBe("key-1");
    expect(client.createRequest.mock.calls[1]?.[1]).toBe("key-1");
    expect(onCreated).toHaveBeenCalledWith(created);
  });

  it("puts a major's migration notes first and requires the acknowledgement", async () => {
    const { client, user } = setup(major);
    expect(screen.getByText("Major upgrade available")).toBeInTheDocument();
    expect(screen.getByText(/one more major away/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upgrade to 2.0.0" }));
    const dialog = screen.getByRole("dialog", { name: "Upgrade orders_queue to sqs 2.0.0?" });
    const notes = within(dialog).getByRole("region", { name: "Migration notes" });
    expect(notes).toHaveTextContent("Set `dlq_name` before upgrading.");
    expect(notes).not.toHaveTextContent("later");
    const submit = within(dialog).getByRole("button", { name: "Open upgrade request" });
    expect(submit).toBeDisabled();
    await user.click(within(dialog).getByRole("checkbox", { name: /I read the migration notes for sqs 2.0.0/ }));
    await user.type(within(dialog).getByLabelText(/Justification/), " new DLQ ");
    await user.click(submit);
    expect(client.createRequest).toHaveBeenCalledWith(
      {
        kind: "upgrade",
        stack_id: SID,
        justification: "new DLQ",
        acknowledge_breaking: true,
        upgrades: [{ resource: "orders_queue", to_version: "2.0.0" }],
      },
      "key-1",
    );
  });

  it("explains a refusal with the API's field errors", async () => {
    const { user } = setup(minor, async () => {
      throw new ApiError(
        parseProblem(422, {
          title: "t",
          status: 422,
          code: "upgrade_requires_input_changes",
          detail: "inputs",
          errors: [{ pointer: "/upgrades/0/inputs", code: "required", message: "missing dlq_name" }],
        }),
      );
    });
    await user.click(screen.getByRole("button", { name: "Upgrade to 1.4.0" }));
    const dialog = screen.getByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Open upgrade request" }));
    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent("The current inputs do not fit the new version");
    expect(alert).toHaveTextContent("/upgrades/0/inputs: missing dlq_name");
    expect(alert).toHaveFocus();
  });

  it("keeps the action visible but disabled, with the reason", async () => {
    const { client, user } = setup(minor, ok, { enabled: false, reason: "The stack is updating; wait." });
    const button = screen.getByRole("button", { name: "Upgrade to 1.4.0" });
    expect(button).toHaveAttribute("aria-disabled", "true");
    expect(button).toHaveAccessibleDescription("Upgrade unavailable: The stack is updating; wait.");
    await user.click(button);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(client.createRequest).not.toHaveBeenCalled();
  });
});
