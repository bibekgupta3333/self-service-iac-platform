"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useReducer, useRef, useState } from "react";

import type { ApiClient } from "@/lib/api/client";
import { browserApi } from "@/lib/api/factory";
import type { RequestSummary } from "@/lib/api/types";
import {
  describeEntry,
  initialLive,
  liveAnnouncement,
  reduceLive,
  type StreamEvent,
} from "@/lib/events/status";
import { documentVisibility, RequestPoller, type Visibility } from "@/lib/events/poller";
import { StatusStream, type StatusStreamOptions, type StreamNotice, type StreamState } from "@/lib/events/stream";
import { formatDateTime, httpsUrl, isTerminal, requestStatusLabel } from "@/lib/format";

import { StatusBadge, StatusTimeline } from "./StatusTimeline";

/** How often the polling fallback reads the request. */
export const POLL_MS = 5000;

export interface RequestLiveProps {
  request: RequestSummary;
  /** Injected for tests; defaults to the browser ApiClient. */
  client?: Pick<ApiClient, "openRequestEvents" | "getRequest">;
  /** Injected for tests: timing and randomness of the stream's reconnects. */
  streamOptions?: Partial<
    Pick<
      StatusStreamOptions,
      | "baseDelayMs"
      | "maxDelayMs"
      | "maxFailures"
      | "minHealthyMs"
      | "idleTimeoutMs"
      | "minReconnectIntervalMs"
      | "sleep"
      | "random"
      | "now"
      | "schedule"
    >
  >;
  pollMs?: number;
  /** Injected for tests; defaults to the document's visibility. */
  visibility?: Visibility;
}

type Connection = { state: StreamState | "polling"; notice?: StreamNotice };

/** connectionText is the short note about how the page is kept current. Pure. */
export function connectionText(c: Connection, ended: boolean): string {
  if (ended) return "";
  switch (c.state) {
    case "open":
      return "Live: this page updates as the request changes.";
    case "connecting":
    case "reconnecting":
      return "Connecting to live updates…";
    case "polling":
      return `Live updates are unavailable here; checking every ${POLL_MS / 1000} seconds instead.`;
    case "stopped":
      return c.notice?.message ?? "Live updates stopped.";
    default:
      return "";
  }
}

/** historyText is shown when no history entries are loaded. Pure. */
export function historyText(c: Connection, finishedOnLoad: boolean): string {
  if (finishedOnLoad) {
    return "This request had already finished when the page loaded, so the live history is not loaded. The audit log keeps every change.";
  }
  if (c.state === "polling" || c.state === "stopped") return "The change history is only available over the live connection.";
  return "No status changes recorded yet.";
}

/**
 * RequestLive shows a request's status, progress and history, kept current by the SSE stream
 * (GET /v1/requests/{id}/events). The server-rendered request seeds it; every status change
 * also refreshes the server components, so links and queue state follow. If the stream cannot
 * be used it polls the request instead.
 */
export function RequestLive({ request, client, streamOptions, pollMs = POLL_MS, visibility }: RequestLiveProps) {
  const router = useRouter();
  const [live, dispatch] = useReducer(reduceLive, undefined, () => initialLive(request.status, request.updated_at));
  const [connection, setConnection] = useState<Connection>({ state: "connecting" });
  const previous = useRef(live.status);
  const id = request.id;
  // A request that was already terminal never changes again: no stream, no polling.
  // Captured once: router.refresh() later passes a new request prop that may be terminal.
  const [finishedOnLoad] = useState(() => isTerminal(request.status));

  useEffect(() => {
    if (finishedOnLoad) return;
    const api = client ?? browserApi();
    let closed = false;

    const poller = new RequestPoller({
      read: () => api.getRequest(id),
      onStatus: (status, at) => {
        if (!closed) dispatch({ type: "polled", status, at });
      },
      onStop: (notice) => {
        if (!closed) setConnection({ state: "stopped", notice });
      },
      isFinal: isTerminal,
      intervalMs: pollMs,
      visibility: visibility ?? documentVisibility(),
    });

    const stream = new StatusStream({
      ...streamOptions,
      initialLastEventId: "0", // replay the whole history, then follow
      open: (lastEventId, signal) => api.openRequestEvents(id, { lastEventId, signal }),
      onEvent: (e: StreamEvent) => dispatch(e),
      onState: (state, notice) => {
        if (closed) return;
        if (state === "closed") return;
        if (state === "unavailable") {
          setConnection({ state: "polling", notice });
          poller.start();
        } else {
          setConnection({ state, notice });
        }
      },
    });
    void stream.start();

    return () => {
      closed = true;
      stream.close();
      poller.stop();
    };
    // The stream belongs to the request id; the other props are fixed for the page's life.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, finishedOnLoad]);

  // Pull fresh server data (PR link, stack status, queue) whenever the status changes.
  useEffect(() => {
    if (previous.current !== live.status) {
      previous.current = live.status;
      router.refresh();
    }
  }, [live.status, router]);

  const label = requestStatusLabel(live.status);
  const pr = request.github && httpsUrl(request.github.pr_url) ? request.github : null;
  const note = connectionText(connection, live.ended);

  return (
    <>
      <section aria-labelledby="progress-title" className="space-y-3 rounded-lg border border-slate-200 bg-white p-5">
        <div className="flex flex-wrap items-center gap-3">
          <h2 id="progress-title" className="text-base font-semibold">
            Progress
          </h2>
          <StatusBadge status={live.status} label={label} />
        </div>
        {/* Polite: announced when the status changes, never for connection noise. */}
        <p role="status" aria-live="polite" aria-atomic="true" className="sr-only">
          {liveAnnouncement(label, live.ended)}
        </p>
        <p className="text-sm text-slate-600" data-testid="connection-note">
          {live.ended
            ? `Finished ${formatDateTime(request.completed_at ?? live.updatedAt ?? request.updated_at)}.`
            : note}
        </p>
        <StatusTimeline status={live.status} />
        {request.queued_behind && !live.ended && (
          <p className="text-sm text-amber-900">
            Waiting for the stack lock held by{" "}
            <Link href={`/requests/${encodeURIComponent(request.queued_behind)}`} className="font-mono underline">
              {request.queued_behind.slice(0, 8)}
            </Link>
            . The pull request opens when that request finishes.
          </p>
        )}
        {request.failure_reason && live.status === "failed" && (
          <p className="text-sm text-red-900">
            Failure reason: <code className="font-mono">{request.failure_reason}</code>
          </p>
        )}
      </section>

      <section aria-labelledby="links-title" className="space-y-2">
        <h2 id="links-title" className="text-base font-semibold">
          Links
        </h2>
        {pr ? (
          <p className="text-sm">
            Pull request{" "}
            <a href={pr.pr_url} target="_blank" rel="noopener noreferrer" className="text-indigo-700 underline">
              #{pr.pr_number}
              <span className="sr-only"> (opens in a new tab)</span>
            </a>{" "}
            on branch <code className="font-mono text-xs">{pr.branch}</code>
          </p>
        ) : (
          <p className="text-sm text-slate-600">The pull request link appears here once it is opened.</p>
        )}
        <p className="text-xs text-slate-500">
          The API does not return plan, cost or workflow run links for a request yet; they appear here when it does.
        </p>
      </section>

      <section aria-labelledby="history-title" className="space-y-3">
        <h2 id="history-title" className="text-base font-semibold">
          History
        </h2>
        {live.entries.length === 0 ? (
          <p className="text-sm text-slate-600">
            {historyText(connection, finishedOnLoad)}
          </p>
        ) : (
          <ol className="space-y-2 rounded-lg border border-slate-200 bg-white p-4 text-sm">
            {live.entries.map((e) => (
              <li key={e.seq} className="flex flex-wrap items-baseline gap-x-3">
                <time dateTime={e.at} className="whitespace-nowrap text-xs text-slate-600">
                  {formatDateTime(e.at)}
                </time>
                <span>{describeEntry(e)}</span>
                <span className="text-xs text-slate-500">({requestStatusLabel(e.status)})</span>
              </li>
            ))}
          </ol>
        )}
      </section>
    </>
  );
}
