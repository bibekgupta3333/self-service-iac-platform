// Pure rules for the same-origin API proxy (src/app/api/iacp/[...path]/route.ts).
//
// The proxy exists because the backend sends no CORS headers. It only forwards user routes
// under /v1: machine routes (/v1/internal/*, /v1/runs) use a separate authenticator and are
// never reachable through the portal.

const BLOCKED_FIRST = new Set(["internal", "runs"]);
const SAFE_SEGMENT = /^[A-Za-z0-9._~@:+-]+$/;

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
 * is not a forwardable user route. Pure.
 */
export function proxyTarget(apiBase: string, segments: readonly string[], search: string): string | null {
  const [version, first] = segments;
  if (version !== "v1" || first === undefined || BLOCKED_FIRST.has(first)) return null;
  if (!segments.every((s) => SAFE_SEGMENT.test(s) && s !== "." && s !== "..")) return null;
  const base = apiBase.replace(/\/+$/, "");
  const qs = search && search !== "?" ? (search.startsWith("?") ? search : `?${search}`) : "";
  return `${base}/${segments.map(encodeURIComponent).join("/")}${qs}`;
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
