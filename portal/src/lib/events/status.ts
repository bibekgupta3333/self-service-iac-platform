// Pure model of a request's live status: decoding the stream's frames (backend
// internal/httpapi/events.go) and folding them into the timeline state the page renders.
//
// Frames: `snapshot` (first connection: the current status), `status` (one audit-derived
// change; its id is the audit seq, the resume point) and `end` (the request is terminal).

import type { SseFrame } from "./sse";

export type StreamEvent =
  | { type: "snapshot"; seq: number; status: string; at: string }
  | { type: "status"; seq: number; at: string; status: string; from?: string; event?: string }
  | { type: "end"; status: string }
  /** A status read by the polling fallback: no seq, no transition. */
  | { type: "polled"; status: string; at: string };

export interface TimelineEntry {
  seq: number;
  at: string;
  status: string;
  from?: string;
  /** The state machine event that caused the change (pr_opened, checks_passed, ...). */
  event?: string;
}

export interface LiveState {
  status: string;
  entries: readonly TimelineEntry[];
  /** Highest seq seen; the Last-Event-ID to resume from. 0 means nothing seen yet. */
  lastSeq: number;
  /** The stream reported the request terminal, or a poll saw a terminal status. */
  ended: boolean;
  updatedAt: string | null;
}

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown): string | undefined => (typeof v === "string" && v !== "" ? v : undefined);
const seqOf = (v: unknown): number | undefined =>
  typeof v === "number" && Number.isSafeInteger(v) && v >= 0 ? v : undefined;

/** decodeFrame turns an SSE frame into a StreamEvent; frames it does not understand give null. Pure. */
export function decodeFrame(frame: SseFrame): StreamEvent | null {
  let body: unknown;
  try {
    body = JSON.parse(frame.data);
  } catch {
    return null;
  }
  if (!isRecord(body)) return null;
  const status = str(body.status);
  if (status === undefined) return null;
  switch (frame.event) {
    case "snapshot": {
      const seq = seqOf(body.seq);
      return seq === undefined ? null : { type: "snapshot", seq, status, at: str(body.updated_at) ?? "" };
    }
    case "status": {
      const seq = seqOf(body.seq);
      if (seq === undefined) return null;
      const from = str(body.from);
      const event = str(body.event);
      return {
        type: "status",
        seq,
        at: str(body.occurred_at) ?? "",
        status,
        ...(from ? { from } : {}),
        ...(event ? { event } : {}),
      };
    }
    case "end":
      return { type: "end", status };
    default:
      return null;
  }
}

const TERMINAL = ["applied", "failed", "rejected", "cancelled"];

/** initialLive seeds the state from the page's server-rendered request. Pure. */
export function initialLive(status: string, updatedAt: string | null = null): LiveState {
  return { status, entries: [], lastSeq: 0, ended: TERMINAL.includes(status), updatedAt };
}

/**
 * reduceLive folds one event into the state. Events at or below lastSeq are duplicates (a
 * resumed stream may repeat the frame it was cut on) and change nothing. Pure.
 */
export function reduceLive(state: LiveState, ev: StreamEvent): LiveState {
  switch (ev.type) {
    case "snapshot":
      if (ev.seq < state.lastSeq) return state;
      return { ...state, status: ev.status, lastSeq: ev.seq, updatedAt: ev.at || state.updatedAt };
    case "status": {
      if (ev.seq <= state.lastSeq) return state;
      const entry: TimelineEntry = {
        seq: ev.seq,
        at: ev.at,
        status: ev.status,
        ...(ev.from ? { from: ev.from } : {}),
        ...(ev.event ? { event: ev.event } : {}),
      };
      return {
        ...state,
        status: ev.status,
        entries: [...state.entries, entry],
        lastSeq: ev.seq,
        updatedAt: ev.at || state.updatedAt,
      };
    }
    case "end":
      return { ...state, status: ev.status, ended: true };
    case "polled":
      return {
        ...state,
        status: ev.status,
        ended: state.ended || TERMINAL.includes(ev.status),
        updatedAt: state.status === ev.status ? state.updatedAt : ev.at || state.updatedAt,
      };
  }
}

const EVENT_LABELS: Record<string, string> = {
  pr_opened: "Pull request opened",
  validation_started: "Validation started",
  checks_passed: "Checks passed",
  checks_failed: "Checks failed",
  approved: "Approved",
  new_commit: "New commit pushed; the plan is being redone",
  pr_closed: "Pull request closed",
  cancel: "Cancelled",
  apply_started: "Apply started",
  apply_succeeded: "Apply succeeded",
  apply_failed: "Apply failed",
  apply_retry: "Apply is retrying after a transient error",
  review_dismissed: "Review dismissed",
  pr_merged: "Pull request merged",
  publish_failed: "The pull request could not be opened",
};

/** describeEntry is the sentence shown for a timeline entry. Unknown events fall back to the status. Pure. */
export function describeEntry(e: Pick<TimelineEntry, "event" | "status" | "from">): string {
  const label = e.event ? EVENT_LABELS[e.event] : undefined;
  if (label) return label;
  return e.from ? `${e.from} to ${e.status}` : e.status;
}

/** liveAnnouncement is the polite live-region text for a status change. Pure. */
export function liveAnnouncement(label: string, ended: boolean): string {
  return ended ? `Request ${label.toLowerCase()}. No further changes.` : `Request status: ${label}.`;
}
