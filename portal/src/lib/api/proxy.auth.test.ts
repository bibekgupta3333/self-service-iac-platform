import { describe, expect, it } from "vitest";

import {
  decideProxy,
  filterSetCookies,
  isAuthFlowPath,
  type ProxyRequestInfo,
  rewriteAuthLocation,
} from "./proxy";

const base = "http://api.test";

const info = (over: Partial<ProxyRequestInfo>): ProxyRequestInfo => ({
  method: "GET",
  segments: ["v1", "auth", "login"],
  search: "",
  contentLength: null,
  origin: null,
  secFetchSite: null,
  allowedOrigins: ["https://portal.test"],
  ...over,
});

describe("auth flow routes", () => {
  it("flags only the three login-flow paths", () => {
    expect(isAuthFlowPath(["v1", "auth", "login"])).toBe(true);
    expect(isAuthFlowPath(["v1", "auth", "callback"])).toBe(true);
    expect(isAuthFlowPath(["v1", "auth", "logout"])).toBe(true);
    expect(isAuthFlowPath(["v1", "me"])).toBe(false);
    expect(isAuthFlowPath(["v1", "requests"])).toBe(false);
  });

  it("forwards login and callback as GET navigations, marked as auth flow", () => {
    expect(decideProxy(base, info({}))).toMatchObject({ kind: "forward", authFlow: true, upstreamMethod: "GET" });
    // GitHub's redirect back is a cross-site GET; the state cookie protects it, not Origin.
    expect(decideProxy(base, info({ segments: ["v1", "auth", "callback"], secFetchSite: "cross-site" }))).toMatchObject({
      kind: "forward",
      authFlow: true,
    });
    expect(decideProxy(base, info({ segments: ["v1", "me"] }))).toMatchObject({ kind: "forward", authFlow: false });
  });

  it("allows POST only on logout, not on login, callback or me", () => {
    for (const segments of [
      ["v1", "auth", "login"],
      ["v1", "auth", "callback"],
      ["v1", "me"],
    ]) {
      expect(decideProxy(base, info({ method: "POST", segments }))).toMatchObject({ kind: "reject", status: 405 });
    }
    expect(
      decideProxy(base, info({ method: "POST", segments: ["v1", "auth", "logout"], secFetchSite: "same-origin" })),
    ).toMatchObject({ kind: "forward", authFlow: true });
  });
});

describe("CSRF with a session cookie", () => {
  const post = (over: Partial<ProxyRequestInfo>): ProxyRequestInfo =>
    info({ method: "POST", segments: ["v1", "requests"], contentLength: "10", ...over });

  it("refuses a cookie-bearing POST with neither Sec-Fetch-Site nor Origin", () => {
    expect(decideProxy(base, post({ hasCookie: true, csrfToken: "t" }))).toMatchObject({
      kind: "reject",
      status: 403,
      code: "cross_site_request",
    });
  });

  it("still allows a cookie-less non-browser client (dev mode) with neither header", () => {
    expect(decideProxy(base, post({ hasCookie: false })).kind).toBe("forward");
    expect(decideProxy(base, post({})).kind).toBe("forward");
  });

  it("requires the CSRF token header when a cookie is present", () => {
    for (const csrfToken of [null, undefined, ""]) {
      expect(decideProxy(base, post({ hasCookie: true, secFetchSite: "same-origin", csrfToken }))).toMatchObject({
        kind: "reject",
        status: 403,
        code: "csrf_token_missing",
      });
    }
    expect(decideProxy(base, post({ hasCookie: true, secFetchSite: "same-origin", csrfToken: "t" })).kind).toBe("forward");
    expect(decideProxy(base, post({ hasCookie: true, origin: "https://portal.test", csrfToken: "t" })).kind).toBe("forward");
  });

  it("lets logout through without a token so an expired session can clear its cookie", () => {
    const logout = post({ segments: ["v1", "auth", "logout"], hasCookie: true, secFetchSite: "same-origin", csrfToken: null });
    expect(decideProxy(base, logout)).toMatchObject({ kind: "forward", authFlow: true });
    // ...but never cross-site.
    expect(decideProxy(base, { ...logout, secFetchSite: "cross-site" })).toMatchObject({ code: "cross_site_request" });
  });

  it("keeps rejecting cross-site posts first, token or not", () => {
    expect(decideProxy(base, post({ hasCookie: true, csrfToken: "t", secFetchSite: "cross-site" }))).toMatchObject({
      code: "cross_site_request",
    });
    expect(decideProxy(base, post({ hasCookie: true, csrfToken: "t", origin: "https://evil.test" }))).toMatchObject({
      code: "cross_site_request",
    });
  });
});

describe("filterSetCookies", () => {
  it("passes only the session and login-state cookies", () => {
    const got = filterSetCookies([
      "iacp_session=abc; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=28800",
      "iacp_oauth_state=xyz; Path=/; HttpOnly; SameSite=Lax; Max-Age=600",
      "tracking=1; Path=/",
      "iacp_sessionx=1; Path=/",
      "other=iacp_session=1",
    ]);
    expect(got).toEqual([
      "iacp_session=abc; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=28800",
      "iacp_oauth_state=xyz; Path=/; HttpOnly; SameSite=Lax; Max-Age=600",
    ]);
  });

  it("passes the __Host- prefixed names the backend uses over https", () => {
    const host = [
      "__Host-iacp_session=abc; Path=/; HttpOnly; Secure; SameSite=Lax",
      "__Host-iacp_oauth_state=x; Path=/; HttpOnly; Secure; SameSite=Lax",
    ];
    expect(filterSetCookies(host)).toEqual(host);
    expect(filterSetCookies(["__Host-other=1; Path=/; Secure"])).toEqual([]);
  });

  it("drops cookies that try to widen their scope with Domain", () => {
    expect(filterSetCookies(["iacp_session=abc; Domain=example.test; Path=/", "iacp_session=a; domain = x.test"])).toEqual([]);
  });

  it("passes clearing cookies", () => {
    expect(filterSetCookies(["iacp_session=; Path=/; Max-Age=0"])).toHaveLength(1);
  });
});

describe("rewriteAuthLocation", () => {
  it.each([
    ["https://github.com/login/oauth/authorize?client_id=x&state=y", "https://github.com/login/oauth/authorize?client_id=x&state=y"],
    ["/requests", "/requests"],
    ["/requests?login_error=not_org_member", "/requests?login_error=not_org_member"],
    [null, null],
    ["//evil.test", null],
    ["https://evil.test/", null],
    ["https://github.com.evil.test/login/oauth/authorize?x=1", null],
    ["http://github.com/login/oauth/authorize?x=1", null],
    ["https://github.com/other?x=1", null],
    ["/a\\b", null],
    ["/a\r\nSet-Cookie: x=1", null],
    ["javascript:alert(1)", null],
    ["requests", null],
  ])("%s -> %s", (loc, want) => expect(rewriteAuthLocation(loc)).toBe(want));
});
