// Pure rules for the same-origin API proxy (src/app/api/iacp/[...path]/route.ts).
//
// The proxy exists because the backend sends no CORS headers. It forwards only the user
// routes the portal actually calls, by an allow-list of the first segment after /v1.
// Everything else, including machine routes (/v1/internal/*, /v1/runs) and any route the
// backend adds later, is refused until it is added here on purpose.
//
// Segments arrive already percent-decoded once by Next.js and are never decoded again.
// Matching is exact and case-sensitive, and a segment must consist of unreserved URL
// characters only, so "Internal", "%69nternal" (still containing "%"), an encoded slash
// ("a/b" after decoding), "." and ".." are all rejected rather than normalised.

/** First path segments after /v1 that the portal may call. */
export const ALLOWED_FIRST_SEGMENTS: ReadonlySet<string> = new Set(["catalog", "requests"]);

const SAFE_SEGMENT = /^[A-Za-z0-9._~-]+$/;

/** isSafeSegment accepts one already-decoded path segment. Pure. */
export const isSafeSegment = (s: string): boolean => SAFE_SEGMENT.test(s) && s !== "." && s !== "..";

/** Request headers forwarded to the API. Cookie and Authorization are for future OAuth sessions. */
export const FORWARD_REQUEST_HEADERS = [
  "accept",
  "content-type",
  "idempotency-key",
  "x-request-id",
  "authorization",
  "cookie",
  "x-csrf-token",
] as const;

/** Response headers passed back to the browser. */
export const FORWARD_RESPONSE_HEADERS = [
  "content-type",
  "x-request-id",
  "location",
  "idempotent-replayed",
  "retry-after",
  "allow",
] as const;

/**
 * proxyTarget returns the upstream URL for the proxied path segments, or null when the path
 * is not an allow-listed user route. Pure.
 */
export function proxyTarget(apiBase: string, segments: readonly string[], search: string): string | null {
  const [version, first] = segments;
  if (version !== "v1" || first === undefined || !ALLOWED_FIRST_SEGMENTS.has(first)) return null;
  if (!segments.every(isSafeSegment)) return null;
  const base = apiBase.replace(/\/+$/, "");
  const qs = search && search !== "?" ? (search.startsWith("?") ? search : `?${search}`) : "";
  return `${base}/${segments.join("/")}${qs}`;
}

/** pickHeaders copies the allow-listed headers from src. Pure. */
export function pickHeaders(src: Headers, names: readonly string[]): Headers {
  const out = new Headers();
  for (const name of names) {
    const v = src.get(name);
    if (v !== null) out.set(name, v);
  }
  return out;
}

/** The backend caps request bodies at 1 MiB (backend/internal/httpapi/respond.go). */
export const MAX_BODY_BYTES = 1 << 20;

/** Methods the proxy serves. HEAD is answered from an upstream GET without a body. */
export const PROXY_METHODS = ["GET", "HEAD", "POST"] as const;

/** Paths (segments joined by "/") that accept POST; everything else is read-only. */
const POST_PATHS: ReadonlySet<string> = new Set(["v1/requests"]);

export interface ProxyRequestInfo {
  method: string;
  segments: readonly string[];
  search: string;
  contentLength: string | null;
}

export type ProxyDecision =
  | { kind: "forward"; url: string; upstreamMethod: "GET" | "POST"; head: boolean }
  | { kind: "reject"; status: number; code: string; detail: string; allow?: string };

const reject = (status: number, code: string, detail: string, allow?: string): ProxyDecision => ({
  kind: "reject",
  status,
  code,
  detail,
  ...(allow ? { allow } : {}),
});

/** decideProxy decides whether and how to forward one request. Pure. */
export function decideProxy(apiBase: string, req: ProxyRequestInfo): ProxyDecision {
  const method = req.method.toUpperCase();
  if (!(PROXY_METHODS as readonly string[]).includes(method)) {
    return reject(405, "method_not_allowed", `${method} is not supported by the portal proxy`, PROXY_METHODS.join(", "));
  }
  const url = proxyTarget(apiBase, req.segments, req.search);
  if (url === null) return reject(404, "not_found", "this path is not available through the portal proxy");

  if (method !== "POST") return { kind: "forward", url, upstreamMethod: "GET", head: method === "HEAD" };

  if (!POST_PATHS.has(req.segments.join("/"))) {
    return reject(405, "method_not_allowed", "POST is not supported on this path", "GET, HEAD");
  }
  if (req.contentLength !== null) {
    const n = Number(req.contentLength);
    if (!/^\d+$/.test(req.contentLength) || !Number.isSafeInteger(n)) {
      return reject(400, "bad_request", "invalid Content-Length");
    }
    if (n > MAX_BODY_BYTES) return reject(413, "payload_too_large", `request body exceeds ${MAX_BODY_BYTES} bytes`);
  }
  return { kind: "forward", url, upstreamMethod: "POST", head: false };
}
