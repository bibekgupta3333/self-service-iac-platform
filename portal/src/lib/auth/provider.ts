// Caller identity for the portal.
//
// The backend selects its user authenticator with IACP_AUTH_MODE:
//
// - `dev` (backend/internal/httpapi/auth.go DevAuthenticator): the API ignores the request and
//   treats every call to /v1 as one fixed local user. There is no header, cookie or token to
//   send, so DevAuthProvider only *labels* the user.
// - `github` (WBS 4.4): GitHub OAuth. The browser holds an HttpOnly `iacp_session` cookie for the
//   portal's origin (the login flow runs through the same-origin proxy, which passes the
//   session Set-Cookie through on the login routes only). GithubAuthProvider sends it with
//   credentials "include", adds the per-session X-CSRF-Token on unsafe methods, and reads the
//   user and the token from GET /v1/me.
//
// Pages and ApiClient only see the AuthProvider interface.

export type AuthMode = "dev" | "github";

export interface Viewer {
  login: string;
  displayName: string;
  mode: AuthMode;
  /** True when the GitHub provider has no valid session; the UI offers "Sign in". */
  anonymous?: boolean;
}

export interface AuthProvider {
  readonly mode: AuthMode;
  /** Fetch credentials mode for calls to the API. */
  readonly credentials: RequestCredentials;
  /** Headers that identify the caller on every API call. */
  requestHeaders(method: string): Promise<Record<string, string>>;
  /** The signed-in user, for display. */
  viewer(): Promise<Viewer>;
}

export const DEFAULT_DEV_USER = "local-dev";

/** DevAuthProvider mirrors the backend's dev auth mode: no credentials, a fixed login. */
export class DevAuthProvider implements AuthProvider {
  readonly mode = "dev" as const;
  readonly credentials: RequestCredentials = "same-origin";
  private readonly login: string;

  constructor(login: string = DEFAULT_DEV_USER) {
    this.login = login.trim() || DEFAULT_DEV_USER;
  }

  async requestHeaders(): Promise<Record<string, string>> {
    // DevAuthenticator.Authenticate ignores the request, so nothing is sent.
    return {};
  }

  async viewer(): Promise<Viewer> {
    return { login: this.login, displayName: this.login, mode: this.mode };
  }
}

/** The subset of GET /v1/me the portal uses (backend/internal/httpapi/auth_handler.go meDTO). */
export interface Me {
  id: string;
  github_login: string;
  display_name: string;
  is_platform_admin: boolean;
  teams: { slug: string; role: string }[];
  auth_mode: string;
  csrf_token?: string;
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface GithubAuthOptions {
  /** Where /v1/me is read: the proxy prefix in the browser, the API origin on the server. */
  baseUrl: string;
  fetch: FetchLike;
  /** Server side only: the incoming request's Cookie header, to forward to the API. */
  cookie?: () => Promise<string | undefined>;
}

/** Methods that never need a CSRF token (the backend's session.SafeMethod). Pure. */
export const isSafeMethod = (method: string): boolean => ["GET", "HEAD", "OPTIONS"].includes(method);

/** The path of the proxy route that starts the login. */
export const LOGIN_PATH = "/api/iacp/v1/auth/login";
export const LOGOUT_PATH = "/api/iacp/v1/auth/logout";

/**
 * loginUrl is the same-origin URL that starts the GitHub login and returns to returnTo. Only
 * a path on this site is accepted; anything else falls back to "/", mirroring the backend's
 * redirect policy. Pure.
 */
export function loginUrl(returnTo: string = "/"): string {
  const ok = returnTo.startsWith("/") && !returnTo.startsWith("//") && !/[\\\u0000-\u001f\u007f]/.test(returnTo);
  return `${LOGIN_PATH}?return_to=${encodeURIComponent(ok ? returnTo : "/")}`;
}

/** GithubAuthProvider authenticates with the iacp_session cookie and a per-session CSRF token. */
export class GithubAuthProvider implements AuthProvider {
  readonly mode = "github" as const;
  readonly credentials: RequestCredentials = "include";
  private readonly opts: GithubAuthOptions;
  private pending: Promise<Me | null> | undefined;

  constructor(opts: GithubAuthOptions) {
    this.opts = opts;
  }

  /** me reads GET /v1/me once per provider. It resolves null when there is no valid session. */
  me(): Promise<Me | null> {
    this.pending ??= this.fetchMe().catch((err: unknown) => {
      this.pending = undefined; // do not cache failures
      throw err;
    });
    return this.pending;
  }

  private async fetchMe(): Promise<Me | null> {
    const headers: Record<string, string> = { Accept: "application/json" };
    const cookie = await this.opts.cookie?.();
    if (cookie) headers.Cookie = cookie;
    const base = this.opts.baseUrl.replace(/\/+$/, "");
    const res = await this.opts.fetch(`${base}/v1/me`, { headers, credentials: this.credentials, cache: "no-store" });
    if (res.status === 401) return null;
    if (!res.ok) throw new Error(`GET /v1/me failed with HTTP ${res.status}`);
    return (await res.json()) as Me;
  }

  async requestHeaders(method: string): Promise<Record<string, string>> {
    const headers: Record<string, string> = {};
    const cookie = await this.opts.cookie?.();
    if (cookie) headers.Cookie = cookie;
    if (!isSafeMethod(method.toUpperCase())) {
      const token = (await this.me())?.csrf_token;
      if (token) headers["X-CSRF-Token"] = token;
    }
    return headers;
  }

  async viewer(): Promise<Viewer> {
    const me = await this.me();
    if (me === null) return { login: "", displayName: "", mode: this.mode, anonymous: true };
    return { login: me.github_login, displayName: me.display_name || me.github_login, mode: this.mode };
  }

  /** logout ends the session. Browser only: it needs the proxy and the cookie jar. */
  async logout(): Promise<void> {
    const headers = await this.requestHeaders("POST");
    const res = await this.opts.fetch(LOGOUT_PATH, { method: "POST", headers, credentials: this.credentials });
    if (!res.ok && res.status !== 401) throw new Error(`logout failed with HTTP ${res.status}`);
    this.pending = undefined;
  }
}

export interface AuthEnv {
  NEXT_PUBLIC_IACP_AUTH_MODE?: string;
  NEXT_PUBLIC_IACP_DEV_USER?: string;
}

/**
 * createAuthProvider picks the provider for env. It fails loudly for any mode the portal
 * cannot honour rather than silently falling back to dev. The github provider needs the
 * transport (fetch, base URL and, on the server, the cookie source).
 */
export function createAuthProvider(env: AuthEnv, github?: GithubAuthOptions): AuthProvider {
  const mode = env.NEXT_PUBLIC_IACP_AUTH_MODE?.trim() || "dev";
  switch (mode) {
    case "dev":
      return new DevAuthProvider(env.NEXT_PUBLIC_IACP_DEV_USER);
    case "github":
      if (!github) throw new Error("NEXT_PUBLIC_IACP_AUTH_MODE=github needs the API transport (fetch and base URL)");
      return new GithubAuthProvider(github);
    default:
      throw new Error(`NEXT_PUBLIC_IACP_AUTH_MODE=${mode} is not supported (supported: dev, github)`);
  }
}

/** authEnv reads the public auth variables. Literal property access lets Next inline them. */
export function authEnv(): AuthEnv {
  return {
    NEXT_PUBLIC_IACP_AUTH_MODE: process.env.NEXT_PUBLIC_IACP_AUTH_MODE,
    NEXT_PUBLIC_IACP_DEV_USER: process.env.NEXT_PUBLIC_IACP_DEV_USER,
  };
}
