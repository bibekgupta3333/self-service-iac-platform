// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { GET, HEAD, POST } from "./route";

interface Upstream {
  url: string;
  init: RequestInit;
}

let calls: Upstream[];
let reply: () => Promise<Response>;

beforeEach(() => {
  calls = [];
  reply = async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } });
  vi.stubEnv("IACP_API_URL", "http://api.test");
  vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return reply();
  });
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

const ctx = (path: string[]) => ({ params: Promise.resolve({ path }) });

const req = (path: string, init: RequestInit = {}) => new Request(`http://portal.test/api/iacp/${path}`, init);

describe("proxy route", () => {
  it("forwards a GET with allow-listed headers and returns allow-listed response headers", async () => {
    reply = async () =>
      new Response('{"data":[]}', {
        status: 200,
        headers: {
          "Content-Type": "application/json",
          "X-Request-Id": "rid",
          "Set-Cookie": "a=b",
          Server: "upstream",
        },
      });
    const res = await GET(
      req("v1/catalog/modules?category=storage", { headers: { Accept: "application/json", Host: "evil", "X-Other": "1" } }),
      ctx(["v1", "catalog", "modules"]),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toBe('{"data":[]}');
    expect(res.headers.get("x-request-id")).toBe("rid");
    expect(res.headers.get("set-cookie")).toBeNull();
    expect(res.headers.get("server")).toBeNull();
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url).toBe("http://api.test/v1/catalog/modules?category=storage");
    const sent = new Headers(calls[0]?.init.headers);
    expect(sent.get("accept")).toBe("application/json");
    expect(sent.get("host")).toBeNull();
    expect(sent.get("x-other")).toBeNull();
  });

  it("answers HEAD from an upstream GET without a body", async () => {
    const res = await HEAD(req("v1/requests", { method: "HEAD" }), ctx(["v1", "requests"]));
    expect(res.status).toBe(200);
    expect(res.body).toBeNull();
    expect(calls[0]?.init.method).toBe("GET");
    expect(calls[0]?.init.body).toBeUndefined();
  });

  it("forwards a POST body and the Idempotency-Key", async () => {
    reply = async () =>
      new Response('{"id":"r1"}', {
        status: 202,
        headers: { "Content-Type": "application/json", Location: "/v1/requests/r1" },
      });
    const body = JSON.stringify({ kind: "create" });
    const res = await POST(
      req("v1/requests", {
        method: "POST",
        body,
        headers: { "Content-Type": "application/json", "Idempotency-Key": "key-12345678" },
      }),
      ctx(["v1", "requests"]),
    );
    expect(res.status).toBe(202);
    expect(res.headers.get("location")).toBe("/api/iacp/v1/requests/r1");
    expect(calls[0]?.init.method).toBe("POST");
    expect(new TextDecoder().decode(calls[0]?.init.body as Uint8Array)).toBe(body);
    expect(new Headers(calls[0]?.init.headers).get("idempotency-key")).toBe("key-12345678");
  });

  it.each([
    [["v1", "internal", "catalog", "index"]],
    [["v1", "Internal", "catalog", "index"]],
    [["v1", "RUNS"]],
    [["v1", "requests", ".."]],
  ])("refuses %o without calling upstream", async (path) => {
    const res = await POST(req(path.join("/"), { method: "POST", body: "{}" }), ctx(path));
    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toBe("application/problem+json");
    expect(calls).toHaveLength(0);
  });

  it.each([
    [{ Origin: "https://evil.test" }],
    [{ "Sec-Fetch-Site": "cross-site" }],
  ])("refuses a cross-site POST %o with 403", async (headers) => {
    const res = await POST(req("v1/requests", { method: "POST", body: "{}", headers }), ctx(["v1", "requests"]));
    expect(res.status).toBe(403);
    expect(await res.json()).toMatchObject({ code: "cross_site_request" });
    expect(calls).toHaveLength(0);
  });

  it("accepts a same-origin POST", async () => {
    const res = await POST(
      req("v1/requests", {
        method: "POST",
        body: "{}",
        headers: { Origin: "http://portal.test", "Sec-Fetch-Site": "same-origin" },
      }),
      ctx(["v1", "requests"]),
    );
    expect(res.status).toBe(200);
    expect(calls).toHaveLength(1);
  });

  it("accepts the configured public origin", async () => {
    vi.stubEnv("IACP_PORTAL_ORIGIN", "https://portal.example");
    const res = await POST(
      req("v1/requests", { method: "POST", body: "{}", headers: { Origin: "https://portal.example" } }),
      ctx(["v1", "requests"]),
    );
    expect(res.status).toBe(200);
  });

  it("refuses POST on read-only paths with 405 and Allow", async () => {
    const res = await POST(req("v1/catalog/modules", { method: "POST", body: "{}" }), ctx(["v1", "catalog", "modules"]));
    expect(res.status).toBe(405);
    expect(res.headers.get("allow")).toBe("GET, HEAD");
    expect(calls).toHaveLength(0);
  });

  it("refuses bodies over 1 MiB with 413, by header or by streamed size", async () => {
    const big = "x".repeat((1 << 20) + 1);
    const declared = await POST(req("v1/requests", { method: "POST", body: big }), ctx(["v1", "requests"]));
    expect(declared.status).toBe(413);

    const stream = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(new TextEncoder().encode(big));
        c.close();
      },
    });
    const chunked = await POST(
      req("v1/requests", { method: "POST", body: stream, duplex: "half" } as RequestInit),
      ctx(["v1", "requests"]),
    );
    expect(chunked.status).toBe(413);
    expect(calls).toHaveLength(0);
  });

  it("maps an unreachable upstream to 502 problem+json", async () => {
    reply = async () => {
      throw new TypeError("fetch failed");
    };
    const res = await GET(req("v1/requests"), ctx(["v1", "requests"]));
    expect(res.status).toBe(502);
    expect(await res.json()).toMatchObject({ status: 502, code: "api_unreachable" });
  });

  describe("the request event stream", () => {
    const ID = "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90";
    const path = ["v1", "requests", ID, "events"];
    const enc = new TextEncoder();
    const dec = new TextDecoder();

    it("passes the stream through as it arrives, forwards Last-Event-ID and aborts upstream when the client leaves", async () => {
      let upstream!: ReadableStreamDefaultController<Uint8Array>;
      reply = async () =>
        new Response(
          new ReadableStream<Uint8Array>({
            start(c) {
              upstream = c;
              c.enqueue(enc.encode('id: 7\nevent: snapshot\ndata: {"status":"pr_open"}\n\n'));
            },
          }),
          {
            status: 200,
            headers: {
              "Content-Type": "text/event-stream; charset=utf-8",
              "Cache-Control": "no-cache, no-transform",
              "X-Accel-Buffering": "no",
              "Set-Cookie": "x=y",
            },
          },
        );
      const client = new AbortController();
      const res = await GET(
        req(`v1/requests/${ID}/events`, {
          headers: { Accept: "text/event-stream", "Last-Event-ID": "7", Cookie: "iacp_session=s" },
          signal: client.signal,
        }),
        ctx(path),
      );

      // The response is returned while the upstream stream is still open, and each chunk is
      // readable as soon as it is sent: nothing waits for the end of the body.
      expect(res.status).toBe(200);
      expect(res.headers.get("content-type")).toBe("text/event-stream; charset=utf-8");
      expect(res.headers.get("cache-control")).toBe("no-cache, no-transform");
      expect(res.headers.get("x-accel-buffering")).toBe("no");
      expect(res.headers.get("set-cookie")).toBeNull();
      expect(res.headers.get("content-length")).toBeNull();
      const reader = res.body!.getReader();
      expect(dec.decode((await reader.read()).value)).toContain("event: snapshot");
      upstream.enqueue(enc.encode(": heartbeat\n\n"));
      expect(dec.decode((await reader.read()).value)).toBe(": heartbeat\n\n");

      expect(calls).toHaveLength(1);
      expect(calls[0]?.url).toBe(`http://api.test/v1/requests/${ID}/events`);
      expect(calls[0]?.init.method).toBe("GET");
      const sent = new Headers(calls[0]?.init.headers);
      expect(sent.get("last-event-id")).toBe("7");
      expect(sent.get("accept")).toBe("text/event-stream");
      expect(sent.get("cookie")).toBe("iacp_session=s");

      const signal = calls[0]?.init.signal as AbortSignal;
      expect(signal.aborted).toBe(false);
      client.abort();
      expect(signal.aborted).toBe(true);
      await reader.cancel().catch(() => undefined);
    });

    it.each(["HEAD", "POST", "PUT", "PATCH", "DELETE"])("refuses %s without calling the API", async (method) => {
      const handler = method === "HEAD" ? HEAD : POST;
      const res = await handler(
        req(`v1/requests/${ID}/events`, { method, ...(method === "HEAD" ? {} : { body: "{}" }) }),
        ctx(path),
      );
      expect(res.status).toBe(405);
      if (method === "HEAD" || method === "POST") expect(res.headers.get("allow")).toBe("GET");
      expect(calls).toHaveLength(0);
    });

    it.each([
      [["v1", "requests", ID, "Events"], 200], // not the stream path: an ordinary read the API answers itself
      [["v1", "requests", ID, "events", ".."], 404],
      [["v1", "requests", "..", "events"], 404],
      [["v1", "requests", ID, "ev%65nts"], 404],
    ] as [string[], number][])("%j is not streamed (%i)", async (p, status) => {
      const res = await GET(req(p.join("/")), ctx(p));
      expect(res.status).toBe(status);
      if (status === 200) expect(calls[0]?.init.signal).toBeUndefined();
      else expect(calls).toHaveLength(0);
    });
  });

  it("passes upstream errors through unchanged", async () => {
    reply = async () =>
      new Response('{"code":"validation_failed"}', { status: 422, headers: { "Content-Type": "application/problem+json" } });
    const res = await GET(req("v1/requests"), ctx(["v1", "requests"]));
    expect(res.status).toBe(422);
    expect(res.headers.get("content-type")).toBe("application/problem+json");
  });
});
