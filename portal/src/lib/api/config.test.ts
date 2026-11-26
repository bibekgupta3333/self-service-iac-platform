import { describe, expect, it } from "vitest";

import { browserApiBaseUrl, DEFAULT_API_URL, PROXY_PREFIX, serverApiBaseUrl } from "./config";

describe("serverApiBaseUrl", () => {
  it.each([
    [{}, DEFAULT_API_URL],
    [{ IACP_API_URL: "http://api:8080/" }, "http://api:8080"],
    [{ NEXT_PUBLIC_IACP_API_URL: "https://api.example" }, "https://api.example"],
    [{ IACP_API_URL: "http://a", NEXT_PUBLIC_IACP_API_URL: "http://b" }, "http://a"],
    [{ IACP_API_URL: "  " }, DEFAULT_API_URL],
  ])("%o -> %s", (env, want) => {
    expect(serverApiBaseUrl(env)).toBe(want);
  });
});

describe("browserApiBaseUrl", () => {
  it("uses the proxy unless a public API URL is set", () => {
    expect(browserApiBaseUrl({})).toBe(PROXY_PREFIX);
    expect(browserApiBaseUrl({ IACP_API_URL: "http://internal" })).toBe(PROXY_PREFIX);
    expect(browserApiBaseUrl({ NEXT_PUBLIC_IACP_API_URL: "https://api.example/" })).toBe("https://api.example");
  });
});
