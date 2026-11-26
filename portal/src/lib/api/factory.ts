// Browser wiring: builds the ApiClient used by client components. The server-side
// counterpart lives in ./server.ts, which reads the incoming cookies (next/headers) and so
// must not be imported from client code.

import { authEnv, createAuthProvider } from "@/lib/auth/provider";

import { ApiClient } from "./client";
import { browserApiBaseUrl } from "./config";

/** browserApi is for client components; it goes through the same-origin proxy by default. */
export function browserApi(): ApiClient {
  const baseUrl = browserApiBaseUrl({ NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL });
  const fetchImpl = (input: string, init?: RequestInit) => fetch(input, init);
  return new ApiClient({
    baseUrl,
    fetch: fetchImpl,
    auth: createAuthProvider(authEnv(), { baseUrl, fetch: fetchImpl }),
  });
}
