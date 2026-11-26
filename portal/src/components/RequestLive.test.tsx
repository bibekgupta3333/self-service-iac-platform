import { render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ApiError, parseProblem } from "@/lib/api/errors";
import type { RequestSummary } from "@/lib/api/types";

import { connectionText, historyText, RequestLive } from "./RequestLive";

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

  it("stops polling on a 401 and says why", async () => {
    const getRequest = vi.fn(async () => {
      throw new ApiError(parseProblem(401, { title: "x", status: 401, code: "unauthenticated" }));
    });
    const openRequestEvents = vi.fn(async () => new Response("<html></html>", { status: 200, headers: { "content-type": "text/html" } }));
    render(<RequestLive request={base} client={{ openRequestEvents, getRequest }} streamOptions={fast} pollMs={5} />);
    await waitFor(() => expect(screen.getByTestId("connection-note")).toHaveTextContent(/Sign in again/));
    await new Promise((r) => setTimeout(r, 30));
    expect(getRequest).toHaveBeenCalledTimes(1);
  });

  it("does not poll while the tab is hidden", async () => {
    let hidden = true;
    let onChange: () => void = () => undefined;
    const visibility = {
      hidden: () => hidden,
      onChange: (cb: () => void) => {
        onChange = cb;
        return () => undefined;
      },
    };
    const getRequest = vi.fn(async (): Promise<RequestSummary> => ({ ...base, status: "validating" }));
    const openRequestEvents = vi.fn(async () => new Response("<html></html>", { status: 200, headers: { "content-type": "text/html" } }));
    render(
      <RequestLive request={base} client={{ openRequestEvents, getRequest }} streamOptions={fast} pollMs={5} visibility={visibility} />,
    );
    await waitFor(() => expect(screen.getByTestId("connection-note")).toHaveTextContent(/checking every/));
    await new Promise((r) => setTimeout(r, 30));
    expect(getRequest).not.toHaveBeenCalled();
    hidden = false;
    onChange();
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Request status: Validating."));
  });

  it("opens no stream for a request that was already terminal", async () => {
    const openRequestEvents = vi.fn(async () => sse([], { hold: true }));
    const getRequest = vi.fn();
    render(
      <RequestLive
        request={{ ...base, status: "applied", completed_at: "2026-03-01T11:00:00Z" }}
        client={{ openRequestEvents, getRequest }}
      />,
    );
    expect(screen.getByTestId("connection-note")).toHaveTextContent("Finished 2026-03-01 11:00 UTC.");
    expect(screen.getByText(/already finished when the page loaded/)).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 20));
    expect(openRequestEvents).not.toHaveBeenCalled();
    expect(getRequest).not.toHaveBeenCalled();
  });

  it("shows each change once when a resumed stream replays the frame it was cut on", async () => {
    const responses = [
      sse([status(1, "pr_open", "pr_opened", "draft"), status(2, "validating", "validation_started", "pr_open")]),
      sse([status(2, "validating", "validation_started", "pr_open"), status(3, "plan_ready", "checks_passed", "validating")], {
        hold: true,
      }),
    ];
    const seen: Array<string | undefined> = [];
    const openRequestEvents = vi.fn(async (_id: string, opts: { lastEventId?: string }) => {
      seen.push(opts.lastEventId);
      return responses.shift() as Response;
    });
    render(<RequestLive request={base} client={{ openRequestEvents, getRequest: vi.fn() }} streamOptions={fast} />);
    await waitFor(() => expect(screen.getByText("Checks passed")).toBeInTheDocument());
    expect(seen).toEqual(["0", "2"]);
    expect(screen.getAllByText("Validation started")).toHaveLength(1);
    expect(within(screen.getByRole("region", { name: "History" })).getAllByRole("listitem")).toHaveLength(3);
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

describe("historyText", () => {
  it("explains why no history is shown", () => {
    expect(historyText({ state: "open" }, true)).toMatch(/already finished when the page loaded/);
    expect(historyText({ state: "polling" }, false)).toMatch(/only available over the live connection/);
    expect(historyText({ state: "stopped" }, false)).toMatch(/only available over the live connection/);
    expect(historyText({ state: "open" }, false)).toBe("No status changes recorded yet.");
  });
});
