import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { CreateRequestResult, RequestInput, RequestSummary, ResourceInput } from "@/lib/api/types";
import { sqsSchema } from "@/lib/schema/fixtures";

import { RequestForm, type UpdateContext } from "./RequestForm";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));

const version = { module: "sqs", version: "1.0.0", inputs_schema: sqsSchema };
const created = { id: "r1", status: "draft" } as RequestSummary;
const SID = "7b1c0a52-6c11-4f0e-b9b6-5f2f3c8d1a90";

const queue: ResourceInput = { name: "queue", module: "sqs", version: "1.0.0", inputs: { name: "orders", visibility_timeout_seconds: 60 } };
const fn: ResourceInput = { name: "fn", module: "lambda", version: "2.0.0", inputs: { runtime: "python3.12" } };

const ctx = (over: Partial<UpdateContext> = {}): UpdateContext => ({
  stack: { id: SID, name: "orders", team: "payments", environment: "development", path: "environments/development/payments/orders" },
  resources: [queue, fn],
  editedIndex: 0,
  initialInputs: queue.inputs,
  ...over,
});

const problem = (status: number, code: string, detail = "api detail", errors: unknown[] = []) =>
  new ApiError(parseProblem(status, { title: `title-${code}`, status, code, detail, errors }));

function setup(update: UpdateContext, createRequest: (i: RequestInput, k: string) => Promise<CreateRequestResult>) {
  const client = { createRequest: vi.fn(createRequest) };
  const onCreated = vi.fn();
  const user = userEvent.setup();
  let n = 0;
  render(<RequestForm version={version} update={update} client={client} newIdempotencyKey={() => `key-${++n}`} onCreated={onCreated} />);
  return { client, onCreated, user };
}

const ok = async () => ({ request: created, replayed: false });

describe("RequestForm in update mode", () => {
  it("starts from the stack's stored inputs and hides the create-only fields", () => {
    setup(ctx(), ok);
    expect(screen.getByLabelText(/^name/)).toHaveValue("orders");
    expect(screen.getByLabelText(/^visibility_timeout_seconds/)).toHaveValue(60);
    expect(screen.queryByLabelText(/^Team/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/^Stack name/)).not.toBeInTheDocument();
    expect(screen.getByText("environments/development/payments/orders")).toBeInTheDocument();
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Submit update request" })).toBeInTheDocument();
  });

  it("sends an update by stack_id with the edited resource replaced and the others kept", async () => {
    const { client, onCreated, user } = setup(ctx(), ok);
    await user.clear(screen.getByLabelText(/^visibility_timeout_seconds/));
    await user.type(screen.getByLabelText(/^visibility_timeout_seconds/), "90");
    await user.type(screen.getByLabelText(/^Justification/), "  raise it ");
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    expect(client.createRequest).toHaveBeenCalledWith(
      {
        kind: "update",
        stack_id: SID,
        justification: "raise it",
        resources: [{ ...queue, inputs: { name: "orders", visibility_timeout_seconds: 90 } }, fn],
      },
      "key-1",
    );
    expect(onCreated).toHaveBeenCalledWith(created);
  });

  it("tells the user when the API gave no spec and the form starts from defaults", () => {
    setup(ctx({ initialInputs: null, resources: [{ ...queue, inputs: {} }] }), ok);
    expect(screen.getByRole("note")).toHaveTextContent(/did not return this stack's current settings/);
    expect(screen.getByLabelText(/^visibility_timeout_seconds/)).toHaveValue(30);
  });

  it("warns about stored inputs the module version no longer has", () => {
    setup(ctx({ initialInputs: { ...queue.inputs, legacy_flag: true } }), ok);
    expect(screen.getByRole("note")).toHaveTextContent("legacy_flag");
  });

  it("asks for an explicit acknowledgement after resources_removed, then sends allow_removals with a new key", async () => {
    const responses: Array<Error | CreateRequestResult> = [
      problem(422, "resources_removed", "this update would destroy fn, which the stack's current spec has; if that is intended, send allow_removals: true"),
      { request: created, replayed: false },
    ];
    const { client, user } = setup(ctx({ resources: [queue] }), async () => {
      const next = responses.shift() as Error | CreateRequestResult;
      if (next instanceof Error) throw next;
      return next;
    });
    await user.click(screen.getByRole("button", { name: "Submit update request" }));

    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("This update would remove resources")).toBeInTheDocument();
    expect(alert).toHaveFocus();
    const ack = screen.getByRole("checkbox", { name: /I understand this update removes resources/ });
    expect(ack).not.toBeChecked();
    expect(screen.getByRole("group", { name: "Resources will be removed" })).toHaveTextContent("fn");
    expect(client.createRequest.mock.calls[0]?.[0]).not.toHaveProperty("allow_removals");

    // Without the box ticked, resubmitting is blocked client-side.
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    expect(client.createRequest).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Acknowledge the removal first")).toBeInTheDocument();

    await user.click(ack);
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    expect(client.createRequest).toHaveBeenCalledTimes(2);
    expect(client.createRequest.mock.calls[1]?.[0]).toMatchObject({ allow_removals: true });
    // The body changed, so the key changed: no idempotency_key_reuse.
    expect(client.createRequest.mock.calls[1]?.[1]).toBe("key-2");
  });

  it.each([
    ["stack_busy", 409, "The stack is busy", /wait for its latest request/],
    ["destroy_pending", 409, "A destroy is already pending", /no further updates or destroys/],
    ["stack_destroyed", 409, "The stack was destroyed", /create a new stack/],
    ["not_found", 404, "Stack not found", /may not exist/],
    ["forbidden", 403, "You are not allowed to do this", /developer role/],
  ])("explains %s and links back to the stack", async (code, status, title, hint) => {
    const { user } = setup(ctx(), async () => {
      throw problem(status, code);
    });
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(title)).toBeInTheDocument();
    expect(alert).toHaveTextContent(hint);
    expect(alert).toHaveFocus();
    expect(within(alert).getByRole("link", { name: "Open the stack" })).toHaveAttribute("href", `/stacks/${SID}`);
  });

  it("maps a field error onto the edited resource's input, not resource 0", async () => {
    const { user } = setup(ctx({ resources: [fn, queue], editedIndex: 1 }), async () => {
      throw problem(422, "validation_failed", "request is invalid", [
        { pointer: "/resources/1/inputs/visibility_timeout_seconds", code: "maximum", message: "must be at most 43200" },
      ]);
    });
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    await screen.findByRole("alert");
    expect(screen.getByLabelText(/^visibility_timeout_seconds/)).toHaveAttribute("aria-invalid", "true");
  });

  it("replays the same key when an unchanged update is retried after a 5xx", async () => {
    let calls = 0;
    const { client, user } = setup(ctx(), async () => {
      if (++calls === 1) throw problem(503, "unavailable");
      return { request: created, replayed: true };
    });
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    await screen.findByRole("alert");
    await user.click(screen.getByRole("button", { name: "Submit update request" }));
    expect(client.createRequest.mock.calls.map((c) => c[1])).toEqual(["key-1", "key-1"]);
  });
});
