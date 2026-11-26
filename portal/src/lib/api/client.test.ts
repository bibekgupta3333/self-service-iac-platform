import { describe, expect, it } from "vitest";

import { DevAuthProvider } from "@/lib/auth/provider";

import { ApiClient, buildUrl } from "./client";
import { ApiError } from "./errors";
import type { CreateRequestInput } from "./types";

interface Call {
  url: string;
  init: RequestInit;
}

/** fakeFetch records calls and answers with the queued responses in order. */
function fakeFetch(...responses: Array<Response | Error>) {
  const calls: Call[] = [];
  const fetch = async (url: string, init: RequestInit = {}): Promise<Response> => {
    calls.push({ url, init });
    const next = responses.shift();
    if (next === undefined) throw new Error("unexpected call");
    if (next instanceof Error) throw next;
    return next;
  };
  return { fetch, calls };
}

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });

const client = (fetch: ReturnType<typeof fakeFetch>["fetch"], baseUrl = "http://api.test") =>
  new ApiClient({ baseUrl, fetch, auth: new DevAuthProvider(), init: { cache: "no-store" } });

const headersOf = (c: Call | undefined) => new Headers(c?.init.headers);

describe("buildUrl", () => {
  it.each([
    ["http://api.test", "/v1/x", {}, "http://api.test/v1/x"],
    ["http://api.test/", "/v1/x", {}, "http://api.test/v1/x"],
    ["/api/iacp", "v1/x", {}, "/api/iacp/v1/x"],
    ["http://api.test", "/v1/x", { a: "1", b: undefined, c: "", d: null, e: false }, "http://api.test/v1/x?a=1"],
    ["http://api.test", "/v1/x", { team: "a b", limit: 5, mine: true }, "http://api.test/v1/x?team=a+b&limit=5&mine=true"],
  ])("buildUrl(%s, %s, %o) = %s", (base, path, query, want) => {
    expect(buildUrl(base, path, query)).toBe(want);
  });
});

describe("ApiClient catalog", () => {
  it("lists modules from the data envelope and passes filters", async () => {
    const f = fakeFetch(json(200, { data: [{ name: "sqs" }], next_cursor: null }));
    const mods = await client(f.fetch).listModules({ category: "messaging" });
    expect(mods).toEqual([{ name: "sqs" }]);
    expect(f.calls[0]?.url).toBe("http://api.test/v1/catalog/modules?category=messaging");
    expect(f.calls[0]?.init.method).toBe("GET");
    expect(f.calls[0]?.init.cache).toBe("no-store");
    expect(f.calls[0]?.init.credentials).toBe("same-origin");
    expect(headersOf(f.calls[0]).get("Accept")).toBe("application/json");
  });

  it("encodes path segments", async () => {
    const f = fakeFetch(json(200, { module: "sqs", version: "1.0.0" }));
    await client(f.fetch).getVersion("sqs", "1.0.0/../x");
    expect(f.calls[0]?.url).toBe("http://api.test/v1/catalog/modules/sqs/versions/1.0.0%2F..%2Fx");
  });

  it.each([[".."], ["."], [""]])("refuses the path segment %j without calling fetch", async (bad) => {
    const f = fakeFetch();
    const err = (await client(f.fetch).getVersion("sqs", bad).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(404);
    expect(f.calls).toHaveLength(0);
    await expect(client(f.fetch).getRequest("..")).rejects.toBeInstanceOf(ApiError);
  });

  it("encodes a percent sign in a name", async () => {
    const f = fakeFetch(json(200, { name: "%" }));
    await client(f.fetch).getModule("%");
    expect(f.calls[0]?.url).toBe("http://api.test/v1/catalog/modules/%25");
  });

  it("unwraps versions", async () => {
    const f = fakeFetch(json(200, { data: [{ version: "1.0.0" }], next_cursor: null }));
    expect(await client(f.fetch).listVersions("sqs")).toEqual([{ version: "1.0.0" }]);
    expect(f.calls[0]?.url).toBe("http://api.test/v1/catalog/modules/sqs/versions");
  });
});

describe("ApiClient requests", () => {
  const input: CreateRequestInput = {
    kind: "create",
    team: "payments",
    environment: "development",
    stack: "orders",
    resources: [{ name: "queue", module: "sqs", version: "1.0.0", inputs: { name: "orders" } }],
  };

  it("posts JSON with the Idempotency-Key header", async () => {
    const f = fakeFetch(json(202, { id: "r1", status: "draft" }));
    const res = await client(f.fetch).createRequest(input, "key-12345678");
    expect(res).toEqual({ request: { id: "r1", status: "draft" }, replayed: false });
    const call = f.calls[0];
    expect(call?.url).toBe("http://api.test/v1/requests");
    expect(call?.init.method).toBe("POST");
    expect(headersOf(call).get("Idempotency-Key")).toBe("key-12345678");
    expect(headersOf(call).get("Content-Type")).toBe("application/json");
    expect(JSON.parse(String(call?.init.body))).toEqual(input);
  });

  it("reports replays", async () => {
    const f = fakeFetch(json(202, { id: "r1" }, { "Idempotent-Replayed": "true" }));
    expect((await client(f.fetch).createRequest(input, "key-12345678")).replayed).toBe(true);
  });

  it("lists requests with mine=true and a cursor", async () => {
    const f = fakeFetch(json(200, { data: [], next_cursor: "abc" }));
    const page = await client(f.fetch).listRequests({ mine: true, cursor: "c1", limit: 20 });
    expect(page.next_cursor).toBe("abc");
    expect(f.calls[0]?.url).toBe("http://api.test/v1/requests?mine=true&limit=20&cursor=c1");
  });

  it("omits mine when false", async () => {
    const f = fakeFetch(json(200, { data: [], next_cursor: null }));
    await client(f.fetch).listRequests({ mine: false, team: "payments" });
    expect(f.calls[0]?.url).toBe("http://api.test/v1/requests?team=payments");
  });

  it("gets one request", async () => {
    const f = fakeFetch(json(200, { id: "r1" }));
    expect(await client(f.fetch, "/api/iacp").getRequest("r1")).toEqual({ id: "r1" });
    expect(f.calls[0]?.url).toBe("/api/iacp/v1/requests/r1");
  });
});

describe("ApiClient errors", () => {
  it("throws ApiError with the problem's field errors", async () => {
    const f = fakeFetch(
      new Response(
        JSON.stringify({
          type: "https://iacp.acme.internal/errors/validation_failed",
          title: "Request failed validation",
          status: 422,
          code: "validation_failed",
          detail: "request is invalid",
          request_id: "rid-1",
          errors: [{ pointer: "/team", code: "pattern", message: "must be a team slug" }],
        }),
        { status: 422, headers: { "Content-Type": "application/problem+json" } },
      ),
    );
    const err = await client(f.fetch).listModules().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(422);
    expect(apiErr.code).toBe("validation_failed");
    expect(apiErr.requestId).toBe("rid-1");
    expect(apiErr.fieldErrors).toEqual([{ pointer: "/team", code: "pattern", message: "must be a team slug" }]);
    expect(apiErr.message).toBe("Request failed validation: request is invalid");
  });

  it("uses the X-Request-Id header when the body is not a problem", async () => {
    const f = fakeFetch(new Response("<html>bad gateway</html>", { status: 502, headers: { "X-Request-Id": "rid-2" } }));
    const err = (await client(f.fetch).listModules().catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(502);
    expect(err.code).toBe("internal");
    expect(err.requestId).toBe("rid-2");
  });

  it("wraps transport failures as network_error", async () => {
    const f = fakeFetch(new TypeError("fetch failed"));
    const err = (await client(f.fetch).listModules().catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe("network_error");
    expect(err.status).toBe(0);
    expect(err.problem.detail).toBe("fetch failed");
  });
});

describe("ApiClient.openRequestEvents", () => {
  const id = "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90";

  it("GETs the stream with Accept, Last-Event-ID and the abort signal, leaving the body unread", async () => {
    const stream = new Response("event: end\ndata: {}\n\n", { status: 200, headers: { "content-type": "text/event-stream" } });
    const f = fakeFetch(stream);
    const ctl = new AbortController();
    const res = await client(f.fetch, "/api/iacp").openRequestEvents(id, { lastEventId: "12", signal: ctl.signal });
    expect(res).toBe(stream);
    expect(res.bodyUsed).toBe(false);
    expect(f.calls[0]?.url).toBe(`/api/iacp/v1/requests/${id}/events`);
    expect(f.calls[0]?.init.method).toBe("GET");
    expect(f.calls[0]?.init.signal).toBe(ctl.signal);
    const h = headersOf(f.calls[0]);
    expect(h.get("accept")).toBe("text/event-stream");
    expect(h.get("last-event-id")).toBe("12");
  });

  it("omits Last-Event-ID on a first connection and sends 0 as given", async () => {
    const f = fakeFetch(new Response("", { status: 200 }), new Response("", { status: 200 }));
    await client(f.fetch).openRequestEvents(id);
    await client(f.fetch).openRequestEvents(id, { lastEventId: "0" });
    expect(headersOf(f.calls[0]).has("last-event-id")).toBe(false);
    expect(headersOf(f.calls[1]).get("last-event-id")).toBe("0");
  });

  it("turns a 429 problem into an ApiError", async () => {
    const f = fakeFetch(json(429, { title: "Too many open streams", status: 429, code: "too_many_streams" }, { "Retry-After": "5" }));
    const err = (await client(f.fetch).openRequestEvents(id).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(429);
    expect(err.code).toBe("too_many_streams");
  });

  it("wraps a transport failure and refuses a dot path segment", async () => {
    const f = fakeFetch(new TypeError("fetch failed"));
    const err = (await client(f.fetch).openRequestEvents(id).catch((e: unknown) => e)) as ApiError;
    expect(err.code).toBe("network_error");
    await expect(client(f.fetch).openRequestEvents("..")).rejects.toMatchObject({ status: 404 });
  });
});
