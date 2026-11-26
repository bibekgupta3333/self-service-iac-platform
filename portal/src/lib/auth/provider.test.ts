import { describe, expect, it } from "vitest";

import { createAuthProvider, DevAuthProvider, GithubAuthProvider, isSafeMethod, loginUrl, type Me } from "./provider";

describe("DevAuthProvider", () => {
  it("sends no credentials, like the backend's DevAuthenticator expects", async () => {
    const p = new DevAuthProvider("alice");
    expect(await p.requestHeaders()).toEqual({});
    expect(p.credentials).toBe("same-origin");
  });

  it("labels the viewer with the configured login", async () => {
    expect(await new DevAuthProvider("alice").viewer()).toEqual({
      login: "alice",
      displayName: "alice",
      mode: "dev",
    });
  });

  it("defaults to the backend's default dev user", async () => {
    expect((await new DevAuthProvider().viewer()).login).toBe("local-dev");
    expect((await new DevAuthProvider("  ").viewer()).login).toBe("local-dev");
  });
});

describe("createAuthProvider", () => {
  it("returns the dev provider by default", () => {
    expect(createAuthProvider({})).toBeInstanceOf(DevAuthProvider);
    expect(createAuthProvider({ NEXT_PUBLIC_IACP_AUTH_MODE: "dev" }).mode).toBe("dev");
  });

  it("refuses unknown modes instead of falling back", () => {
    expect(() => createAuthProvider({ NEXT_PUBLIC_IACP_AUTH_MODE: "saml" })).toThrow(/not supported/);
  });

  it("builds the github provider only with a transport", () => {
    expect(() => createAuthProvider({ NEXT_PUBLIC_IACP_AUTH_MODE: "github" })).toThrow(/needs the API transport/);
    const p = createAuthProvider({ NEXT_PUBLIC_IACP_AUTH_MODE: "github" }, { baseUrl: "/api/iacp", fetch: async () => new Response() });
    expect(p).toBeInstanceOf(GithubAuthProvider);
  });
});

const me = (over: Partial<Me> = {}): Me => ({
  id: "u1",
  github_login: "priya",
  display_name: "Priya S.",
  is_platform_admin: false,
  teams: [{ slug: "payments", role: "developer" }],
  auth_mode: "github",
  csrf_token: "tok-123",
  ...over,
});

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("GithubAuthProvider", () => {
  const make = (handler: (url: string, init?: RequestInit) => Response | Promise<Response>, cookie?: () => Promise<string | undefined>) => {
    const calls: { url: string; init?: RequestInit }[] = [];
    const fetch = async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return handler(url, init);
    };
    return { provider: new GithubAuthProvider({ baseUrl: "/api/iacp/", fetch, cookie }), calls };
  };

  it("uses credentials include", () => {
    expect(make(() => json(me())).provider.credentials).toBe("include");
  });

  it("reads the viewer from /v1/me with credentials", async () => {
    const { provider, calls } = make(() => json(me()));
    expect(await provider.viewer()).toEqual({ login: "priya", displayName: "Priya S.", mode: "github" });
    expect(calls[0]!.url).toBe("/api/iacp/v1/me");
    expect(calls[0]!.init?.credentials).toBe("include");
  });

  it("falls back to the login when there is no display name", async () => {
    const { provider } = make(() => json(me({ display_name: "" })));
    expect((await provider.viewer()).displayName).toBe("priya");
  });

  it("reports an anonymous viewer on 401", async () => {
    const { provider } = make(() => json({ code: "unauthenticated" }, 401));
    expect(await provider.viewer()).toEqual({ login: "", displayName: "", mode: "github", anonymous: true });
    expect(await provider.requestHeaders("POST")).toEqual({});
  });

  it("sends the CSRF token on unsafe methods only", async () => {
    const { provider, calls } = make(() => json(me()));
    expect(await provider.requestHeaders("GET")).toEqual({});
    expect(calls).toHaveLength(0);
    for (const m of ["POST", "post", "PUT", "PATCH", "DELETE"]) {
      expect(await provider.requestHeaders(m)).toEqual({ "X-CSRF-Token": "tok-123" });
    }
    expect(calls).toHaveLength(1); // /v1/me is read once per provider
  });

  it("forwards the server-side cookie and never sets Cookie without one", async () => {
    const withCookie = make(() => json(me()), async () => "iacp_session=abc");
    expect(await withCookie.provider.requestHeaders("GET")).toEqual({ Cookie: "iacp_session=abc" });
    expect(await withCookie.provider.requestHeaders("POST")).toEqual({ Cookie: "iacp_session=abc", "X-CSRF-Token": "tok-123" });
    expect((withCookie.calls[0]!.init?.headers as Record<string, string>).Cookie).toBe("iacp_session=abc");
    const without = make(() => json(me()));
    expect(await without.provider.requestHeaders("GET")).toEqual({});
  });

  it("does not cache a failed /v1/me", async () => {
    let n = 0;
    const { provider } = make(() => (++n === 1 ? json({}, 503) : json(me())));
    await expect(provider.viewer()).rejects.toThrow(/503/);
    expect((await provider.viewer()).login).toBe("priya");
  });

  it("logs out through the proxy with the CSRF token", async () => {
    const { provider, calls } = make((url) => (url.endsWith("/logout") ? new Response(null, { status: 204 }) : json(me())));
    await provider.logout();
    const out = calls.find((c) => c.url === "/api/iacp/v1/auth/logout");
    expect(out?.init?.method).toBe("POST");
    expect((out?.init?.headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok-123");
  });

  it("surfaces a failed logout", async () => {
    const { provider } = make((url) => (url.endsWith("/logout") ? json({}, 500) : json(me())));
    await expect(provider.logout()).rejects.toThrow(/logout failed/);
  });
});

describe("loginUrl", () => {
  it.each([
    ["/requests", "/api/iacp/v1/auth/login?return_to=%2Frequests"],
    ["/", "/api/iacp/v1/auth/login?return_to=%2F"],
    ["//evil.test", "/api/iacp/v1/auth/login?return_to=%2F"],
    ["https://evil.test/", "/api/iacp/v1/auth/login?return_to=%2F"],
    ["/a\\b", "/api/iacp/v1/auth/login?return_to=%2F"],
    ["/a\nb", "/api/iacp/v1/auth/login?return_to=%2F"],
  ])("%s", (to, want) => expect(loginUrl(to)).toBe(want));
  it("defaults to the home page", () => expect(loginUrl()).toContain("return_to=%2F"));
});

describe("isSafeMethod", () => {
  it.each([["GET", true], ["HEAD", true], ["OPTIONS", true], ["POST", false], ["DELETE", false]])("%s", (m, want) =>
    expect(isSafeMethod(m)).toBe(want),
  );
});
