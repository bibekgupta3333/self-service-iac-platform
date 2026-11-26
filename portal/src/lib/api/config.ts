// Where the API lives, per side. Pure: callers pass the environment in.
//
// - Server components call the API directly: IACP_API_URL (server-only), falling back to
//   NEXT_PUBLIC_IACP_API_URL, then the backend's default listen address.
// - The browser calls NEXT_PUBLIC_IACP_API_URL when set (the API must then allow CORS, which
//   it does not do yet), otherwise the same-origin proxy route at /api/iacp, which forwards
//   to IACP_API_URL. The proxy is the default because the backend sends no CORS headers.

export const DEFAULT_API_URL = "http://127.0.0.1:8080";
export const PROXY_PREFIX = "/api/iacp";

export interface ApiEnv {
  IACP_API_URL?: string;
  NEXT_PUBLIC_IACP_API_URL?: string;
}

const clean = (v: string | undefined): string | undefined => {
  const t = v?.trim();
  return t ? t.replace(/\/+$/, "") : undefined;
};

export function serverApiBaseUrl(env: ApiEnv): string {
  return clean(env.IACP_API_URL) ?? clean(env.NEXT_PUBLIC_IACP_API_URL) ?? DEFAULT_API_URL;
}

export function browserApiBaseUrl(env: ApiEnv): string {
  return clean(env.NEXT_PUBLIC_IACP_API_URL) ?? PROXY_PREFIX;
}
