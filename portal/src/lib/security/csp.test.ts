import { describe, expect, it } from "vitest";

import { contentSecurityPolicy, originOf } from "./csp";

describe("originOf", () => {
  it.each([
    ["https://api.example/v1", "https://api.example"],
    ["http://127.0.0.1:8080", "http://127.0.0.1:8080"],
    ["/api/iacp", undefined],
    ["javascript:alert(1)", undefined],
    [undefined, undefined],
  ])("%s -> %s", (url, want) => expect(originOf(url)).toBe(want));
});

describe("contentSecurityPolicy", () => {
  it("locks production down to the portal's own origin", () => {
    expect(contentSecurityPolicy({ dev: false })).toBe(
      "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
        "img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; " +
        "form-action 'self'; frame-ancestors 'none'",
    );
  });

  it("allows a directly called API origin", () => {
    expect(contentSecurityPolicy({ dev: false, publicApiUrl: "https://api.example/" })).toContain(
      "connect-src 'self' https://api.example;",
    );
  });

  it("allows eval and websockets only in dev", () => {
    const dev = contentSecurityPolicy({ dev: true });
    expect(dev).toContain("script-src 'self' 'unsafe-inline' 'unsafe-eval'");
    expect(dev).toContain("connect-src 'self' ws:");
  });
});
