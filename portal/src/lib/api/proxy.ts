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
