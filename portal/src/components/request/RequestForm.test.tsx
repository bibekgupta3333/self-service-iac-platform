import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { CreateRequestInput, CreateRequestResult, RequestSummary } from "@/lib/api/types";
import { sqsSchema } from "@/lib/schema/fixtures";

import { RequestForm } from "./RequestForm";

const push = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));

const version = { module: "sqs", version: "1.0.0", inputs_schema: sqsSchema };
const created = { id: "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90", status: "draft" } as RequestSummary;

function setup(createRequest: (input: CreateRequestInput, key: string) => Promise<CreateRequestResult>) {
  const client = { createRequest: vi.fn(createRequest) };
  const onCreated = vi.fn();
  const user = userEvent.setup();
  render(<RequestForm version={version} client={client} newIdempotencyKey={() => "key-0001-fixed"} onCreated={onCreated} />);
  return { client, onCreated, user };
}

async function fillValid(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/^Team/), "payments");
  await user.type(screen.getByLabelText(/^Stack name/), "order-events");
  await user.type(screen.getByLabelText(/^name/), "orders");
}

describe("RequestForm", () => {
  it("renders controls generated from the schema", () => {
    setup(async () => ({ request: created, replayed: false }));
    expect(screen.getByRole("group", { name: "sqs inputs" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Encryption" })).toBeInTheDocument();
    expect(screen.getByLabelText(/^fifo/)).toHaveAttribute("type", "checkbox");
    expect(screen.getByLabelText(/^visibility_timeout_seconds/)).toHaveValue(30);
    expect(screen.getByLabelText(/^Resource name/)).toHaveValue("sqs");
  });

  it("blocks submission and lists client-side problems", async () => {
    const { client, user } = setup(async () => ({ request: created, replayed: false }));
    await user.click(screen.getByRole("button", { name: "Submit request" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("Please fix the highlighted fields")).toBeInTheDocument();
    expect(within(alert).getByRole("link", { name: "Team" })).toHaveAttribute("href", "#field-team");
    expect(within(alert).getByRole("link", { name: "name" })).toHaveAttribute("href", "#field-input-name");
    expect(screen.getByLabelText(/^Team/)).toHaveAttribute("aria-invalid", "true");
    expect(client.createRequest).not.toHaveBeenCalled();
  });

  it("submits the payload with the session's idempotency key", async () => {
    const { client, onCreated, user } = setup(async () => ({ request: created, replayed: false }));
    await fillValid(user);
    await user.clear(screen.getByLabelText(/^visibility_timeout_seconds/));
    await user.type(screen.getByLabelText(/^visibility_timeout_seconds/), "60");
    await user.click(screen.getByRole("button", { name: "Submit request" }));
    expect(client.createRequest).toHaveBeenCalledWith(
      {
        kind: "create",
        team: "payments",
        environment: "development",
        stack: "order-events",
        resources: [{ name: "sqs", module: "sqs", version: "1.0.0", inputs: { name: "orders", visibility_timeout_seconds: 60 } }],
      },
      "key-0001-fixed",
    );
    expect(onCreated).toHaveBeenCalledWith(created);
  });

  it("maps backend validation errors onto fields and reuses the key on retry", async () => {
    const problem = parseProblem(422, {
      title: "Request failed validation",
      status: 422,
      code: "validation_failed",
      detail: "request is invalid",
      request_id: "rid-9",
      errors: [
        { pointer: "/team", code: "forbidden", message: "you are not a developer in payments" },
        { pointer: "/resources/0/inputs/name", code: "pattern", message: "name is taken" },
        { pointer: "/resources/0/version", code: "module_version_unavailable", message: "sqs@1.0.0 is yanked" },
      ],
    });
    const { client, user } = setup(async () => {
      throw new ApiError(problem);
    });
    await fillValid(user);
    await user.click(screen.getByRole("button", { name: "Submit request" }));

    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("Request failed validation")).toBeInTheDocument();
    expect(within(alert).getByText("/resources/0/version: sqs@1.0.0 is yanked")).toBeInTheDocument();
    expect(within(alert).getByText("rid-9")).toBeInTheDocument();
    expect(screen.getByText("name is taken")).toBeInTheDocument();
    expect(screen.getByLabelText(/^name/)).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText(/^Team/)).toHaveAttribute("aria-invalid", "true");

    await user.click(screen.getByRole("button", { name: "Submit request" }));
    expect(client.createRequest).toHaveBeenCalledTimes(2);
    expect(client.createRequest.mock.calls.map((c) => c[1])).toEqual(["key-0001-fixed", "key-0001-fixed"]);
  });

  it("previews the exact request", async () => {
    const { user } = setup(async () => ({ request: created, replayed: false }));
    await fillValid(user);
    await user.click(screen.getByText("Preview request"));
    const pre = screen.getByText(/POST \/v1\/requests/, { selector: "pre code" });
    expect(pre.textContent).toContain("Idempotency-Key: key-0001-fixed");
    expect(pre.textContent).toContain('"stack": "order-events"');
  });
});
