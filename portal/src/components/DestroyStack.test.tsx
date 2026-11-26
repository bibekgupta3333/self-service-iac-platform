import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { CreateRequestResult, RequestInput, RequestSummary } from "@/lib/api/types";

import { DestroyStack } from "./DestroyStack";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));

const SID = "7b1c0a52-6c11-4f0e-b9b6-5f2f3c8d1a90";
const stack = { id: SID, name: "orders", path: "environments/development/payments/orders", team: "payments", environment: "development" };
const enabled = { enabled: true, reason: "" };
const created = { id: "r1", status: "draft" } as RequestSummary;

const problem = (status: number, code: string, detail = "api detail", errors: unknown[] = []) =>
  new ApiError(parseProblem(status, { title: `title-${code}`, status, code, detail, errors }));

function setup(createRequest: (i: RequestInput, k: string) => Promise<CreateRequestResult>, props: Partial<Parameters<typeof DestroyStack>[0]> = {}) {
  const client = { createRequest: vi.fn(createRequest) };
  const onCreated = vi.fn();
  const user = userEvent.setup();
  let n = 0;
  render(<DestroyStack stack={stack} state={enabled} client={client} newIdempotencyKey={() => `key-${++n}`} onCreated={onCreated} {...props} />);
  return { client, onCreated, user };
}

const ok = async () => ({ request: created, replayed: false });

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "Destroy" }));
  return screen.getByRole("dialog", { name: "Destroy orders?" });
}

describe("DestroyStack", () => {
  it("keeps the dialog closed until Destroy is pressed, and warns plainly when it opens", async () => {
    const { user } = setup(ok);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    const dialog = await openDialog(user);
    expect(dialog).toHaveAccessibleDescription(/Merging the destroy pull request removes the infrastructure/);
    expect(within(dialog).getByText(/environments\/development\/payments\/orders/)).toBeInTheDocument();
    expect(within(dialog).queryByText("This is a production stack.")).not.toBeInTheDocument();
  });

  it("puts focus on the name field when it opens", async () => {
    const { user } = setup(ok);
    const dialog = await openDialog(user);
    expect(within(dialog).getByLabelText(/Type orders to confirm/)).toHaveFocus();
  });

  it("flags a production stack", async () => {
    const { user } = setup(ok, { stack: { ...stack, environment: "production" } });
    const dialog = await openDialog(user);
    expect(within(dialog).getByText("This is a production stack.")).toBeInTheDocument();
  });

  it("enables the submit button only for the exact name", async () => {
    const { user } = setup(ok);
    const dialog = await openDialog(user);
    const submit = within(dialog).getByRole("button", { name: "Open destroy request" });
    const name = within(dialog).getByLabelText(/Type orders to confirm/);
    expect(submit).toBeDisabled();
    await user.type(name, "order");
    expect(submit).toBeDisabled();
    await user.type(name, "s ");
    expect(submit).toBeDisabled(); // trailing space is not the name
    await user.clear(name);
    await user.type(name, "Orders");
    expect(submit).toBeDisabled(); // case matters
    await user.clear(name);
    await user.type(name, "orders");
    expect(submit).toBeEnabled();
  });

  it("requires a justification and focuses the error summary", async () => {
    const { client, user } = setup(ok);
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText(/Type orders to confirm/), "orders");
    await user.click(within(dialog).getByRole("button", { name: "Open destroy request" }));
    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveFocus();
    expect(within(alert).getByRole("link", { name: "Justification" })).toHaveAttribute("href", "#destroy-justification");
    expect(within(dialog).getByLabelText(/^Justification/)).toHaveAttribute("aria-invalid", "true");
    expect(client.createRequest).not.toHaveBeenCalled();
  });

  it("sends the destroy body with an Idempotency-Key and reports the created request", async () => {
    const { client, onCreated, user } = setup(ok);
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText(/Type orders to confirm/), "orders");
    await user.type(within(dialog).getByLabelText(/^Justification/), "  service retired ");
    await user.click(within(dialog).getByRole("button", { name: "Open destroy request" }));
    expect(client.createRequest).toHaveBeenCalledWith(
      { kind: "destroy", stack_id: SID, confirm_stack_name: "orders", justification: "service retired" },
      "key-1",
    );
    expect(onCreated).toHaveBeenCalledWith(created);
  });

  it("reuses the key when the same confirmation is retried after a 5xx, and mints a new one after an edit", async () => {
    let calls = 0;
    const { client, user } = setup(async () => {
      if (++calls <= 2) throw problem(503, "unavailable");
      return { request: created, replayed: false };
    });
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText(/Type orders to confirm/), "orders");
    await user.type(within(dialog).getByLabelText(/^Justification/), "retired");
    const submit = () => user.click(within(dialog).getByRole("button", { name: "Open destroy request" }));
    await submit();
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/may have received the request/);
    await submit();
    await user.type(within(dialog).getByLabelText(/^Justification/), " now");
    await submit();
    expect(client.createRequest.mock.calls.map((c) => c[1])).toEqual(["key-1", "key-1", "key-2"]);
  });

  it.each([
    ["stack_busy", 409, "The stack is busy"],
    ["destroy_pending", 409, "A destroy is already pending"],
    ["stack_destroyed", 409, "The stack was destroyed"],
    ["not_found", 404, "Stack not found"],
    ["forbidden", 403, "You are not allowed to do this"],
  ])("explains %s clearly and focuses the message", async (code, status, title) => {
    const { user } = setup(async () => {
      throw problem(status, code);
    });
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText(/Type orders to confirm/), "orders");
    await user.type(within(dialog).getByLabelText(/^Justification/), "retired");
    await user.click(within(dialog).getByRole("button", { name: "Open destroy request" }));
    const alert = await within(dialog).findByRole("alert");
    expect(within(alert).getByText(title)).toBeInTheDocument();
    expect(alert).toHaveFocus();
  });

  it("maps the API's confirmation error onto the name field", async () => {
    const { user } = setup(async () => {
      throw problem(422, "validation_failed", "request is invalid", [
        { pointer: "/confirm_stack_name", code: "mismatch", message: 'must be "orders"' },
      ]);
    });
    const dialog = await openDialog(user);
    await user.type(within(dialog).getByLabelText(/Type orders to confirm/), "orders");
    await user.type(within(dialog).getByLabelText(/^Justification/), "retired");
    await user.click(within(dialog).getByRole("button", { name: "Open destroy request" }));
    await within(dialog).findByRole("alert");
    expect(within(dialog).getByLabelText(/Type orders to confirm/)).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByText('must be "orders"', { selector: "p" })).toBeInTheDocument();
  });

  it("Cancel closes the dialog and returns focus to the trigger", async () => {
    const { user } = setup(ok);
    const dialog = await openDialog(user);
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Destroy" })).toHaveFocus();
  });

  it("is aria-disabled with the reason when the action is not available, and never opens", async () => {
    const { client, user } = setup(ok, { state: { enabled: false, reason: "A destroy request is already open for this stack." } });
    const trigger = screen.getByRole("button", { name: "Destroy" });
    expect(trigger).toHaveAttribute("aria-disabled", "true");
    expect(trigger).toHaveAccessibleDescription(/A destroy request is already open/);
    await user.click(trigger);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(client.createRequest).not.toHaveBeenCalled();
  });
});
