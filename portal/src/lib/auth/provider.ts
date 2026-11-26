// Caller identity for the portal.
//
// The backend selects its user authenticator with IACP_AUTH_MODE. The only mode built so far
// is `dev` (backend/internal/httpapi/auth.go DevAuthenticator): the API ignores the request
// entirely and treats every call to /v1 as one fixed local user, upserted at startup from
// IACP_DEV_USER (default "local-dev"). There is no header, cookie or token to send, and no
// /v1/me endpoint yet, so the portal can only *label* the user with the same login.
//
// GitHub OAuth (WBS 4.4) will replace DevAuthProvider with a provider that relies on the
// iacp_session cookie (credentials: "include"), adds X-CSRF-Token on mutations and reads the
// user from GET /v1/me. Pages and ApiClient only see the AuthProvider interface.

export type AuthMode = "dev";

export interface Viewer {
  login: string;
  displayName: string;
  mode: AuthMode;
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

export interface AuthEnv {
  NEXT_PUBLIC_IACP_AUTH_MODE?: string;
  NEXT_PUBLIC_IACP_DEV_USER?: string;
}

/**
 * createAuthProvider picks the provider for env. It fails loudly for any mode the portal
 * cannot honour rather than silently falling back to dev.
 */
export function createAuthProvider(env: AuthEnv): AuthProvider {
  const mode = env.NEXT_PUBLIC_IACP_AUTH_MODE?.trim() || "dev";
  switch (mode) {
    case "dev":
      return new DevAuthProvider(env.NEXT_PUBLIC_IACP_DEV_USER);
    default:
      throw new Error(
        `NEXT_PUBLIC_IACP_AUTH_MODE=${mode} is not supported yet (supported: dev; GitHub OAuth arrives with WBS 4.4)`,
      );
  }
}

/** authEnv reads the public auth variables. Literal property access lets Next inline them. */
export function authEnv(): AuthEnv {
  return {
    NEXT_PUBLIC_IACP_AUTH_MODE: process.env.NEXT_PUBLIC_IACP_AUTH_MODE,
    NEXT_PUBLIC_IACP_DEV_USER: process.env.NEXT_PUBLIC_IACP_DEV_USER,
  };
}
