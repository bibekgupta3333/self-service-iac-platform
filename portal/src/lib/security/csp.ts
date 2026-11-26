// Content-Security-Policy for every portal response (applied in next.config.ts). Pure.
//
// The portal loads nothing from other origins, so everything is 'self'. Next.js injects
// inline bootstrap scripts and style attributes, so script-src and style-src allow
// 'unsafe-inline'; replacing that with per-request nonces (middleware) is a follow-up. The
// policy still blocks external scripts, plugins, framing, foreign form targets and calls to
// any API other than the portal itself (or NEXT_PUBLIC_IACP_API_URL when set).

export interface CspOptions {
  /** next dev needs eval for React Refresh and a websocket for HMR. */
  dev: boolean;
  /** NEXT_PUBLIC_IACP_API_URL, when the browser calls the API directly. */
  publicApiUrl?: string;
}

/** originOf returns the origin of an absolute http(s) URL, or undefined. Pure. */
export function originOf(url: string | undefined): string | undefined {
  if (!url) return undefined;
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.origin : undefined;
  } catch {
    return undefined;
  }
}

/** contentSecurityPolicy builds the header value. Pure. */
export function contentSecurityPolicy({ dev, publicApiUrl }: CspOptions): string {
  const api = originOf(publicApiUrl);
  const directives: Array<[string, string[]]> = [
    ["default-src", ["'self'"]],
    ["script-src", ["'self'", "'unsafe-inline'", ...(dev ? ["'unsafe-eval'"] : [])]],
    ["style-src", ["'self'", "'unsafe-inline'"]],
    ["img-src", ["'self'", "data:"]],
    ["font-src", ["'self'"]],
    ["connect-src", ["'self'", ...(api ? [api] : []), ...(dev ? ["ws:"] : [])]],
    ["object-src", ["'none'"]],
    ["base-uri", ["'self'"]],
    ["form-action", ["'self'"]],
    ["frame-ancestors", ["'none'"]],
  ];
  return directives.map(([name, values]) => `${name} ${values.join(" ")}`).join("; ");
}
