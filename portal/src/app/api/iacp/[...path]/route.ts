// Same-origin proxy to the control-plane API for client components. The rules are pure
// functions in src/lib/api/proxy.ts; this file only does the I/O.

import { PROXY_PREFIX, serverApiBaseUrl } from "@/lib/api/config";
import {
  decideProxy,
  FORWARD_REQUEST_HEADERS,
  FORWARD_RESPONSE_HEADERS,
  MAX_BODY_BYTES,
  pickHeaders,
  portalOrigins,
  rewriteLocation,
} from "@/lib/api/proxy";

export const dynamic = "force-dynamic";

type Context = { params: Promise<{ path: string[] }> };

const TITLES: Record<number, string> = {
  400: "Malformed request",
  403: "Not allowed",
  404: "Resource not found",
  405: "Method not allowed",
  413: "Request body too large",
  502: "Bad gateway",
};

function problem(status: number, code: string, detail: string, extra: Record<string, string> = {}): Response {
  return new Response(
    JSON.stringify({ type: "about:blank", title: TITLES[status] ?? "Error", status, code, detail }),
    { status, headers: { "Content-Type": "application/problem+json", ...extra } },
  );
}

/** readCapped reads the body, or returns null once it exceeds max bytes (chunked bodies too). */
async function readCapped(req: Request, max: number): Promise<Uint8Array | null> {
  if (!req.body) return new Uint8Array();
  const reader = req.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > max) {
      await reader.cancel();
      return null;
    }
    chunks.push(value);
  }
  const out = new Uint8Array(size);
  let offset = 0;
  for (const c of chunks) {
    out.set(c, offset);
    offset += c.byteLength;
  }
  return out;
}

async function forward(req: Request, ctx: Context): Promise<Response> {
  const { path } = await ctx.params;
  const url = new URL(req.url);
  const allowedOrigins = portalOrigins(req.url, req.headers.get("host"), process.env.IACP_PORTAL_ORIGIN);
  const decision = decideProxy(
    serverApiBaseUrl({
      IACP_API_URL: process.env.IACP_API_URL,
      NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL,
    }),
    {
      method: req.method,
      segments: path,
      search: url.search,
      contentLength: req.headers.get("content-length"),
      origin: req.headers.get("origin"),
      secFetchSite: req.headers.get("sec-fetch-site"),
      allowedOrigins,
    },
  );
  if (decision.kind === "reject") {
    return problem(decision.status, decision.code, decision.detail, decision.allow ? { Allow: decision.allow } : {});
  }

  let body: Uint8Array | undefined;
  if (decision.upstreamMethod === "POST") {
    const read = await readCapped(req, MAX_BODY_BYTES);
    if (read === null) return problem(413, "payload_too_large", `request body exceeds ${MAX_BODY_BYTES} bytes`);
    body = read;
  }

  let upstream: Response;
  try {
    upstream = await fetch(decision.url, {
      method: decision.upstreamMethod,
      headers: pickHeaders(req.headers, FORWARD_REQUEST_HEADERS),
      body: body as BodyInit | undefined,
      cache: "no-store",
      redirect: "manual",
    });
  } catch (err) {
    console.error("api proxy: upstream unreachable", err);
    return problem(502, "api_unreachable", "the platform API is not reachable");
  }

  const headers = pickHeaders(upstream.headers, FORWARD_RESPONSE_HEADERS);
  const location = rewriteLocation(headers.get("location"), PROXY_PREFIX);
  headers.delete("location");
  if (location !== null) headers.set("location", location);
  if (decision.head) {
    // Drain (API responses are small) rather than cancel: cancelling the upstream stream
    // stalled the HEAD response under next start.
    await upstream.arrayBuffer().catch(() => undefined);
    return new Response(null, { status: upstream.status, headers });
  }
  return new Response(upstream.body, { status: upstream.status, headers });
}

export const GET = forward;
export const HEAD = forward;
export const POST = forward;
