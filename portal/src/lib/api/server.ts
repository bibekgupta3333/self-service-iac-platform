// Server-side wiring for server components and route handlers (never cached: data is live).
// In github auth mode the caller's Cookie header is forwarded to the API, so pages render
// with the signed-in user's permissions.

import { cookies } from "next/headers";

import { authEnv, type AuthProvider, createAuthProvider } from "@/lib/auth/provider";

import { ApiClient } from "./client";
import { serverApiBaseUrl } from "./config";

const baseUrl = (): string =>
  serverApiBaseUrl({
    IACP_API_URL: process.env.IACP_API_URL,
    NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL,
  });

/** serverAuth returns the auth provider for the current request. */
export function serverAuth(): AuthProvider {
  return createAuthProvider(authEnv(), {
    baseUrl: baseUrl(),
    fetch: (input, init) => fetch(input, { ...init, cache: "no-store" }),
    cookie: async () => (await cookies()).toString(),
  });
}

/** serverApi is the ApiClient for server components. */
export function serverApi(): ApiClient {
  return new ApiClient({
    baseUrl: baseUrl(),
    fetch: (input, init) => fetch(input, init),
    auth: serverAuth(),
    init: { cache: "no-store" },
  });
}
