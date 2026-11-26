// Pure Server-Sent Events framing (WHATWG HTML, "Server-sent events"). The status stream is
// read with fetch rather than EventSource because EventSource hides the HTTP status (a 429,
// 401 or 404 looks like any other failure) and cannot be given headers.

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

export interface ParsedSse {
  frames: SseFrame[];
  /** Text after the last complete frame; prepend it to the next chunk. */
  rest: string;
}

/** normalise turns CRLF and lone CR into LF, holding back a trailing CR for the next chunk. Pure. */
function normalise(text: string): { text: string; held: string } {
  const held = text.endsWith("\r") ? "\r" : "";
  const body = held ? text.slice(0, -1) : text;
  return { text: body.replace(/\r\n?/g, "\n"), held };
}

function toFrame(block: string): SseFrame | undefined {
  const data: string[] = [];
  let frame: SseFrame = { data: "" };
  let hasField = false;
  for (const line of block.split("\n")) {
    if (line === "" || line.startsWith(":")) continue; // blank or comment (heartbeat)
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "id":
        if (!value.includes("\u0000")) frame = { ...frame, id: value };
        hasField = true;
        break;
      case "event":
        frame = { ...frame, event: value };
        hasField = true;
        break;
      case "data":
        data.push(value);
        hasField = true;
        break;
      case "retry":
        if (/^\d+$/.test(value)) frame = { ...frame, retry: Number(value) };
        hasField = true;
        break;
      default:
        break; // unknown fields are ignored
    }
  }
  // A block of comments only (a heartbeat) dispatches nothing.
  return hasField ? { ...frame, data: data.join("\n") } : undefined;
}

/**
 * parseSse splits buffered text into complete frames. A frame ends at a blank line; the
 * unfinished tail is returned as `rest`. Comment lines (": heartbeat") produce no frame.
 * Pure: feed it `rest + chunk` each time.
 */
export function parseSse(buffer: string): ParsedSse {
  const { text, held } = normalise(buffer);
  const end = text.lastIndexOf("\n\n");
  if (end === -1) return { frames: [], rest: text + held };
  const frames = text
    .slice(0, end)
    .split("\n\n")
    .map(toFrame)
    .filter((f): f is SseFrame => f !== undefined);
  return { frames, rest: text.slice(end + 2) + held };
}
