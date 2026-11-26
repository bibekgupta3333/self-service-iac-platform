import { describe, expect, it } from "vitest";

import { createAuthProvider, DevAuthProvider } from "./provider";

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

  it("refuses modes that are not built yet instead of falling back", () => {
    expect(() => createAuthProvider({ NEXT_PUBLIC_IACP_AUTH_MODE: "github" })).toThrow(/not supported/);
  });
});
