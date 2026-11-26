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
export const ALLOWED_FIRST_SEGMENTS: ReadonlySet<string> = new Set(["catalog", "requests", "auth", "me"]);

/**
 * Exact paths allowed under "auth" and "me" (the OAuth login flow and the caller's profile).
 * Anything else below /v1/auth is refused, whatever the backend adds later.
 */
const AUTH_PATHS: ReadonlySet<string> = new Set(["v1/auth/login", "v1/auth/callback", "v1/auth/logout"]);
const ME_PATH = "v1/me";

/** Paths that run the login flow: their responses may redirect and set the session cookies. */
export const isAuthFlowPath = (segments: readonly string[]): boolean => AUTH_PATHS.has(segments.join("/"));

const SAFE_SEGMENT = /^[A-Za-z0-9._~-]+$/;

/** isSafeSegment accepts one already-decoded path segment. Pure. */
export const isSafeSegment = (s: string): boolean => SAFE_SEGMENT.test(s) && s !== "." && s !== "..";

/** Request headers forwarded to the API. Cookie and X-CSRF-Token carry the OAuth session. */
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
  "cache-control",
] as const;

/** The only cookies the proxy lets the API set in the browser: the session and the login state. */
export const SETTABLE_COOKIES: ReadonlySet<string> = new Set(["iacp_session", "iacp_oauth_state"]);

/**
 * filterSetCookies keeps the Set-Cookie values whose cookie name is allow-listed and that
 * carry no Domain attribute (a host-only cookie cannot be widened to sibling subdomains).
 * Pure.
 */
export function filterSetCookies(values: readonly string[]): string[] {
  return values.filter((v) => {
    const [pair, ...attrs] = v.split(";").map((x) => x.trim());
    const name = pair?.split("=")[0] ?? "";
    return SETTABLE_COOKIES.has(name) && !attrs.some((a) => /^domain\s*=/i.test(a));
  });
}

const GITHUB_AUTHORIZE = "https://github.com/login/oauth/authorize?";

/**
 * rewriteAuthLocation validates a Location header from a login-flow response. The only
 * redirects the flow produces go to GitHub's authorize endpoint (login) or to a path on the
 * portal (callback), so anything else is dropped. Pure.
 */
export function rewriteAuthLocation(location: string | null): string | null {
  if (location === null) return null;
  if (location.startsWith(GITHUB_AUTHORIZE)) return location;
  const safePath = location.startsWith("/") && !location.startsWith("//") && !/[\\\u0000-\u001f\u007f]/.test(location);
  return safePath ? location : null;
}

/**
 * proxyTarget returns the upstream URL for the proxied path segments, or null when the path
 * is not an allow-listed user route. Pure.
 */
export function proxyTarget(apiBase: string, segments: readonly string[], search: string): string | null {
  const [version, first] = segments;
  if (version !== "v1" || first === undefined || !ALLOWED_FIRST_SEGMENTS.has(first)) return null;
  if (!segments.every(isSafeSegment)) return null;
  const joined = segments.join("/");
  if (first === "auth" && !AUTH_PATHS.has(joined)) return null;
  if (first === "me" && joined !== ME_PATH) return null;
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

/**
 * rewriteLocation maps an upstream Location header into the proxy's URL space. The backend
 * answers with API-relative paths ("/v1/requests/{id}"), which a browser would resolve against
 * the portal origin. Anything that is not such a path (absolute URLs, other paths) is dropped
 * rather than passed on. Pure.
 */
export function rewriteLocation(location: string | null, prefix: string): string | null {
  if (location === null || !location.startsWith("/v1/") || location.startsWith("//")) return null;
  return `${prefix.replace(/\/+$/, "")}${location}`;
}

/** The backend caps request bodies at 1 MiB (backend/internal/httpapi/respond.go). */
export const MAX_BODY_BYTES = 1 << 20;

/** Methods the proxy serves. HEAD is answered from an upstream GET without a body. */
export const PROXY_METHODS = ["GET", "HEAD", "POST"] as const;

/** Paths (segments joined by "/") that accept POST; everything else is read-only. */
const POST_PATHS: ReadonlySet<string> = new Set(["v1/requests", "v1/auth/logout"]);

export interface ProxyRequestInfo {
  method: string;
  segments: readonly string[];
  search: string;
  contentLength: string | null;
  /** Origin request header. */
  origin: string | null;
  /** Sec-Fetch-Site request header. */
  secFetchSite: string | null;
  /** Origins the portal is served from (its own request origin, plus IACP_PORTAL_ORIGIN). */
  allowedOrigins: readonly string[];
  /** Whether the request carries a Cookie header (ambient browser credentials). */
  hasCookie?: boolean;
  /** X-CSRF-Token request header. */
  csrfToken?: string | null;
}

/**
 * portalOrigins lists the origins the portal is being served from: the scheme and Host the
 * browser used (req.url can carry the server's own hostname instead, e.g. localhost under
 * next start), the request URL's origin, and IACP_PORTAL_ORIGIN for deployments behind a
 * proxy that rewrites Host. A cross-site page cannot choose the Host header of the victim's
 * request, so trusting it here is safe. Pure.
 */
export function portalOrigins(requestUrl: string, host: string | null, configured: string | undefined): string[] {
  const url = new URL(requestUrl);
  const out = new Set<string>([url.origin]);
  if (host) out.add(`${url.protocol}//${host}`);
  const extra = configured?.trim().replace(/\/+$/, "");
  if (extra) out.add(extra);
  return [...out];
}

/**
 * isSameOriginRequest is the CSRF check for unsafe methods. Browsers send Sec-Fetch-Site on
 * every request, and Origin on every cross-origin POST, so a cross-site form or fetch is
 * always identified. A request with neither header comes from a non-browser client (curl, a
 * script), which carries no ambient browser credentials, and is allowed. Pure.
 */
export function isSameOriginRequest(
  req: Pick<ProxyRequestInfo, "origin" | "secFetchSite" | "allowedOrigins" | "hasCookie">,
): boolean {
  if (req.secFetchSite !== null) return req.secFetchSite === "same-origin";
  if (req.origin !== null) return req.allowedOrigins.includes(req.origin);
  // Neither header: a non-browser client, unless it presents a cookie. Browsers always send
  // one of the two on a cross-origin POST, so a cookie without them is refused (fail closed).
  return !req.hasCookie;
}

export type ProxyDecision =
  | { kind: "forward"; url: string; upstreamMethod: "GET" | "POST"; head: boolean; authFlow: boolean }
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

  const authFlow = isAuthFlowPath(req.segments);
  if (method !== "POST") return { kind: "forward", url, upstreamMethod: "GET", head: method === "HEAD", authFlow };

  if (!POST_PATHS.has(req.segments.join("/"))) {
    return reject(405, "method_not_allowed", "POST is not supported on this path", "GET, HEAD");
  }
  if (!isSameOriginRequest(req)) {
    return reject(403, "cross_site_request", "cross-site requests are not accepted by the portal proxy");
  }
  // With a session cookie the backend also demands a valid per-session token; requiring its
  // presence here rejects cookie-bearing forged posts before they reach the API.
  // Logout is the exception: a browser whose session expired has no token to send and must
  // still clear its cookie. The backend demands the token whenever the session is live.
  if (req.hasCookie && !req.csrfToken && req.segments.join("/") !== "v1/auth/logout") {
    return reject(403, "csrf_token_missing", "the X-CSRF-Token header is required on requests with a session");
  }
  if (req.contentLength !== null) {
    const n = Number(req.contentLength);
    if (!/^\d+$/.test(req.contentLength) || !Number.isSafeInteger(n)) {
      return reject(400, "bad_request", "invalid Content-Length");
    }
    if (n > MAX_BODY_BYTES) return reject(413, "payload_too_large", `request body exceeds ${MAX_BODY_BYTES} bytes`);
  }
  return { kind: "forward", url, upstreamMethod: "POST", head: false, authFlow };
}
