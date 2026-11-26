import { render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { RequestSummary } from "@/lib/api/types";

import { connectionText, RequestLive } from "./RequestLive";

const refresh = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const SSE = { "content-type": "text/event-stream" };
const enc = new TextEncoder();

function sse(chunks: string[], opts: { hold?: boolean } = {}): Response {
  let i = 0;
  return new Response(
    new ReadableStream<Uint8Array>({
      pull(c) {
        if (i < chunks.length) c.enqueue(enc.encode(chunks[i++]));
        else if (!opts.hold) c.close();
      },
    }),
    { status: 200, headers: SSE },
  );
}

const status = (seq: number, to: string, event: string, from?: string) =>
  `id: ${seq}\nevent: status\ndata: ${JSON.stringify({ seq, occurred_at: "2026-03-01T10:0" + seq + ":00Z", status: to, event, from })}\n\n`;
const end = (to: string) => `event: end\ndata: {"status":"${to}"}\n\n`;

const base = {
  id: "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90",
  short_id: "REQ-1",
  kind: "create",
  status: "draft",
  stack: { id: "s", path: "environments/development/payments/orders", status: "provisioning" },
  queued_behind: null,
  created_by: "u",
  created_at: "2026-03-01T10:00:00Z",
  updated_at: "2026-03-01T10:00:00Z",
  completed_at: null,
  github: null,
  links: { self: "", events: "" },
  allowed_events: [],
} as unknown as RequestSummary;

const fast = { baseDelayMs: 1, maxDelayMs: 2, sleep: async () => undefined };

describe("RequestLive", () => {
  it("shows the history from the stream, announces the new status and refreshes server data", async () => {
    refresh.mockClear();
    const openRequestEvents = vi.fn(async () =>
      sse([status(1, "pr_open", "pr_opened", "draft"), status(2, "validating", "validation_started", "pr_open")], { hold: true }),
    );
    render(<RequestLive request={base} client={{ openRequestEvents, getRequest: vi.fn() }} streamOptions={fast} />);

    await waitFor(() => expect(screen.getByText("Validation started")).toBeInTheDocument());
    expect(screen.getByText("Pull request opened")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Request status: Validating.");
    expect(screen.getByTestId("connection-note")).toHaveTextContent("Live: this page updates");
    expect(refresh).toHaveBeenCalled();
    expect(openRequestEvents).toHaveBeenCalledWith(base.id, expect.objectContaining({ lastEventId: "0" }));
  });

  it("finishes on end and says so politely", async () => {
    const openRequestEvents = vi.fn(async () => sse([status(3, "applied", "apply_succeeded", "applying"), end("applied")]));
    render(<RequestLive request={{ ...base, status: "applying" }} client={{ openRequestEvents, getRequest: vi.fn() }} streamOptions={fast} />);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Request applied. No further changes."));
    expect(screen.getByTestId("connection-note")).toHaveTextContent(/^Finished /);
    expect(openRequestEvents).toHaveBeenCalledTimes(1);
  });

  it("links the pull request, https only", () => {
    const client = { openRequestEvents: vi.fn(async () => sse([], { hold: true })), getRequest: vi.fn() };
    const { rerender } = render(
      <RequestLive
        request={{ ...base, github: { branch: "iacp/req-1", pr_number: 12, pr_url: "https://github.com/o/r/pull/12" } }}
        client={client}
      />,
    );
    const link = screen.getByRole("link", { name: /#12/ });
    expect(link).toHaveAttribute("href", "https://github.com/o/r/pull/12");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    rerender(
      <RequestLive request={{ ...base, github: { branch: "b", pr_number: 1, pr_url: "javascript:alert(1)" } }} client={client} />,
    );
    expect(screen.queryByRole("link", { name: /#1/ })).not.toBeInTheDocument();
  });

  it("explains a 429 and does not reconnect", async () => {
    const openRequestEvents = vi.fn(async () => {
      throw new ApiError(parseProblem(429, { title: "Too many open streams", status: 429, code: "too_many_streams" }));
    });
    render(<RequestLive request={base} client={{ openRequestEvents, getRequest: vi.fn() }} streamOptions={fast} />);
    await waitFor(() => expect(screen.getByTestId("connection-note")).toHaveTextContent(/too many live request pages/));
    expect(openRequestEvents).toHaveBeenCalledTimes(1);
  });

  it("explains a 404 and a 401", async () => {
    const make = (code: number) =>
      vi.fn(async () => {
        throw new ApiError(parseProblem(code, { title: "x", status: code, code: "x" }));
      });
    const first = render(<RequestLive request={base} client={{ openRequestEvents: make(404), getRequest: vi.fn() }} />);
    await waitFor(() => expect(screen.getByTestId("connection-note")).toHaveTextContent(/not found/));
    first.unmount();
    render(<RequestLive request={base} client={{ openRequestEvents: make(401), getRequest: vi.fn() }} />);
    await waitFor(() => expect(screen.getByTestId("connection-note")).toHaveTextContent(/Sign in again/));
  });

  it("falls back to polling when the stream is not an event stream", async () => {
    const getRequest = vi
      .fn<(id: string) => Promise<RequestSummary>>()
      .mockResolvedValueOnce({ ...base, status: "validating" })
      .mockResolvedValue({ ...base, status: "applied" });
    const openRequestEvents = vi.fn(async () => new Response("<html></html>", { status: 200, headers: { "content-type": "text/html" } }));
    render(<RequestLive request={base} client={{ openRequestEvents, getRequest }} streamOptions={fast} pollMs={5} />);
    await waitFor(() => expect(screen.getByTestId("connection-note")).toHaveTextContent(/Finished/));
    expect(getRequest).toHaveBeenCalledWith(base.id);
    expect(screen.getByRole("status")).toHaveTextContent("Request applied. No further changes.");
    const calls = getRequest.mock.calls.length;
    await new Promise((r) => setTimeout(r, 30));
    expect(getRequest.mock.calls.length).toBe(calls); // polling stopped at the terminal status
  });

  it("closes the stream when unmounted", async () => {
    let signal: AbortSignal | undefined;
    const openRequestEvents = vi.fn(async (_id: string, opts: { signal?: AbortSignal }) => {
      signal = opts.signal;
      return sse([], { hold: true });
    });
    const { unmount } = render(<RequestLive request={base} client={{ openRequestEvents, getRequest: vi.fn() }} />);
    await waitFor(() => expect(signal).toBeDefined());
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("shows the queue and the failure reason", () => {
    const client = { openRequestEvents: vi.fn(async () => sse([], { hold: true })), getRequest: vi.fn() };
    const { unmount } = render(<RequestLive request={{ ...base, queued_behind: "abcdef12-0000-0000-0000-000000000000" }} client={client} />);
    expect(screen.getByRole("link", { name: "abcdef12" })).toHaveAttribute("href", "/requests/abcdef12-0000-0000-0000-000000000000");
    unmount();
    render(<RequestLive request={{ ...base, status: "failed", failure_reason: "plan_mismatch" }} client={client} />);
    const note = screen.getByText(/Failure reason/);
    expect(within(note).getByText("plan_mismatch")).toBeInTheDocument();
  });
});

describe("connectionText", () => {
  it("is silent once the request is final", () => {
    expect(connectionText({ state: "open" }, true)).toBe("");
    expect(connectionText({ state: "polling" }, false)).toMatch(/checking every 5 seconds/);
    expect(connectionText({ state: "stopped" }, false)).toBe("Live updates stopped.");
  });
});
