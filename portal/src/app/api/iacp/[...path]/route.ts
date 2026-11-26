// Same-origin proxy to the control-plane API for client components. See src/lib/api/proxy.ts.

import { serverApiBaseUrl } from "@/lib/api/config";
import {
  FORWARD_REQUEST_HEADERS,
  FORWARD_RESPONSE_HEADERS,
  pickHeaders,
  proxyTarget,
} from "@/lib/api/proxy";

export const dynamic = "force-dynamic";

type Context = { params: Promise<{ path: string[] }> };

function problem(status: number, code: string, detail: string): Response {
  return Response.json(
    { type: `about:blank`, title: status === 404 ? "Resource not found" : "Bad gateway", status, code, detail },
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}

async function forward(req: Request, ctx: Context): Promise<Response> {
  const { path } = await ctx.params;
  const target = proxyTarget(
    serverApiBaseUrl({
      IACP_API_URL: process.env.IACP_API_URL,
      NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL,
    }),
    path,
    new URL(req.url).search,
  );
  if (target === null) {
    return problem(404, "not_found", "this path is not available through the portal proxy");
  }

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: req.method,
      headers: pickHeaders(req.headers, FORWARD_REQUEST_HEADERS),
      body: req.method === "GET" ? undefined : await req.text(),
      cache: "no-store",
      redirect: "manual",
    });
  } catch (err) {
    console.error("api proxy: upstream unreachable", err);
    return problem(502, "api_unreachable", "the platform API is not reachable");
  }

  return new Response(upstream.body, {
    status: upstream.status,
    headers: pickHeaders(upstream.headers, FORWARD_RESPONSE_HEADERS),
  });
}

export const GET = forward;
export const POST = forward;
