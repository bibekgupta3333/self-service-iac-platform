// Server-Sent Events framing (WHATWG HTML, "Server-sent events"). The status stream is read
// with fetch rather than EventSource because EventSource hides the HTTP status (a 429, 401 or
// 404 looks like any other failure) and cannot be given headers.
//
// SseParser is incremental: each chunk is scanned once for line breaks, so a large frame that
// arrives in many small chunks costs linear time, not quadratic. A frame that grows past the
// size cap is dropped and the parser resynchronises at the next blank line. Line handling is
// built from the pure helpers below.

export interface SseFrame {
  /** The `id:` field of this frame, when it had one. */
  id?: string;
  /** The `event:` field; absent means the default "message" event. */
  event?: string;
  /** `data:` lines joined by "\n". */
  data: string;
  /** The `retry:` field in milliseconds, when it had a valid one. */
  retry?: number;
}

/** The default cap on one frame: 256 KiB of text (UTF-16 code units, which is bytes for ASCII JSON). */
export const MAX_FRAME_CHARS = 256 * 1024;

export interface SseChunkResult {
  frames: SseFrame[];
  /** Comment lines seen (": heartbeat"). They dispatch nothing but prove the connection is alive. */
  comments: number;
  /** Frames dropped because they grew past the size cap. */
  dropped: number;
}

export type SseLine = { kind: "blank" } | { kind: "comment" } | { kind: "field"; field: string; value: string };

/** parseLine classifies one line (without its terminator). Pure. */
export function parseLine(line: string): SseLine {
  if (line === "") return { kind: "blank" };
  if (line.startsWith(":")) return { kind: "comment" };
  const colon = line.indexOf(":");
  const field = colon === -1 ? line : line.slice(0, colon);
  const value = colon === -1 ? "" : line.slice(colon + 1);
  return { kind: "field", field, value: value.startsWith(" ") ? value.slice(1) : value };
}

/** A frame being assembled from its field lines. */
export interface FrameDraft {
  readonly id?: string;
  readonly event?: string;
  readonly retry?: number;
  /** Joined data lines; undefined until the first `data:` line. */
  readonly data?: string;
  readonly hasField: boolean;
  /** Characters of field lines so far, for the size cap. */
  readonly size: number;
}

export const EMPTY_DRAFT: FrameDraft = { hasField: false, size: 0 };

/** applyField folds one field line into the draft. Unknown fields are ignored but still count towards the size. Pure. */
export function applyField(d: FrameDraft, field: string, value: string, lineSize: number): FrameDraft {
  const next = { ...d, size: d.size + lineSize };
  switch (field) {
    case "id":
      return value.includes("\u0000") ? { ...next, hasField: true } : { ...next, id: value, hasField: true };
    case "event":
      return { ...next, event: value, hasField: true };
    case "data":
      return { ...next, data: d.data === undefined ? value : `${d.data}\n${value}`, hasField: true };
    case "retry":
      return /^\d+$/.test(value) ? { ...next, retry: Number(value), hasField: true } : { ...next, hasField: true };
    default:
      return next;
  }
}

/** finishFrame turns a draft into a frame at a blank line. A draft without fields (a heartbeat) gives undefined. Pure. */
export function finishFrame(d: FrameDraft): SseFrame | undefined {
  if (!d.hasField) return undefined;
  return {
    ...(d.id !== undefined ? { id: d.id } : {}),
    ...(d.event !== undefined ? { event: d.event } : {}),
    data: d.data ?? "",
    ...(d.retry !== undefined ? { retry: d.retry } : {}),
  };
}

const BREAK = /[\r\n]/g;

/**
 * SseParser turns decoded text chunks into frames. Feed it every chunk of one connection in
 * order; use a new parser for each connection. CRLF, LF and lone CR all end a line, also
 * when a CRLF is split across chunks.
 */
export class SseParser {
  private partial = "";
  /** The previous chunk ended with CR: a leading LF in the next one belongs to it. */
  private skipLf = false;
  private draft: FrameDraft = EMPTY_DRAFT;
  /** Inside an oversized frame: ignore lines until the blank line that ends it. */
  private discarding = false;
  /** While discarding: the current line had text, so its end is not a blank line. */
  private discardedText = false;

  constructor(private readonly maxFrameChars: number = MAX_FRAME_CHARS) {}

  push(chunk: string): SseChunkResult {
    const out: SseChunkResult = { frames: [], comments: 0, dropped: 0 };
    let i = this.skipLf && chunk.startsWith("\n") ? 1 : 0;
    this.skipLf = false;
    while (i < chunk.length) {
      BREAK.lastIndex = i;
      const m = BREAK.exec(chunk);
      if (!m) {
        this.append(chunk.slice(i), out);
        break;
      }
      const at = m.index;
      this.append(chunk.slice(i, at), out);
      this.endLine(out);
      if (chunk[at] === "\r") {
        if (at + 1 === chunk.length) this.skipLf = true;
        i = chunk[at + 1] === "\n" ? at + 2 : at + 1;
      } else {
        i = at + 1;
      }
    }
    return out;
  }

  private append(text: string, out: SseChunkResult): void {
    if (text === "") return;
    if (this.discarding) {
      this.discardedText = true;
      return;
    }
    this.partial += text;
    if (this.partial.length + this.draft.size > this.maxFrameChars) {
      out.dropped += 1;
      this.partial = "";
      this.draft = EMPTY_DRAFT;
      this.discarding = true;
      this.discardedText = true;
    }
  }

  private endLine(out: SseChunkResult): void {
    const line = this.partial;
    this.partial = "";
    if (this.discarding) {
      const blank = !this.discardedText;
      this.discardedText = false;
      if (blank) this.discarding = false; // the oversized frame is over; resume with the next one
      return;
    }
    const parsed = parseLine(line);
    switch (parsed.kind) {
      case "blank": {
        const frame = finishFrame(this.draft);
        this.draft = EMPTY_DRAFT;
        if (frame) out.frames.push(frame);
        return;
      }
      case "comment":
        out.comments += 1;
        return;
      case "field":
        this.draft = applyField(this.draft, parsed.field, parsed.value, line.length + 1);
        return;
    }
  }
}
