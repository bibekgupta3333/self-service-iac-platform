// Wiring: one place that reads the environment and builds an ApiClient per side.

import { authEnv, createAuthProvider } from "@/lib/auth/provider";

import { ApiClient } from "./client";
import { browserApiBaseUrl, serverApiBaseUrl } from "./config";

/** serverApi is for server components and route handlers (never cached: data is live). */
export function serverApi(): ApiClient {
  return new ApiClient({
    baseUrl: serverApiBaseUrl({
      IACP_API_URL: process.env.IACP_API_URL,
      NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL,
    }),
    fetch: (input, init) => fetch(input, init),
    auth: createAuthProvider(authEnv()),
    init: { cache: "no-store" },
  });
}

/** browserApi is for client components; it goes through the same-origin proxy by default. */
export function browserApi(): ApiClient {
  return new ApiClient({
    baseUrl: browserApiBaseUrl({ NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL }),
    fetch: (input, init) => fetch(input, init),
    auth: createAuthProvider(authEnv()),
  });
}
