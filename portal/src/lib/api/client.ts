// ApiClient is the portal's only HTTP adapter to the control-plane API. fetch, the base URL
// and the AuthProvider are injected, so tests use a fake fetch and pages pick server or
// browser wiring (see ./factory.ts).

import type { AuthProvider } from "@/lib/auth/provider";

import { ApiError, parseProblem } from "./errors";
import type {
  CreateRequestInput,
  CreateRequestResult,
  ModuleFilter,
  ModuleSummary,
  ModuleVersion,
  Page,
  RequestFilter,
  RequestSummary,
  VersionSummary,
} from "./types";

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export type QueryValue = string | number | boolean | undefined | null;

export interface ApiClientOptions {
  /** Absolute API origin ("http://127.0.0.1:8080") or a same-origin proxy prefix ("/api/iacp"). */
  baseUrl: string;
  fetch: FetchLike;
  auth: AuthProvider;
  /** Extra RequestInit merged into every call (e.g. { cache: "no-store" } on the server). */
  init?: RequestInit;
}

/** buildUrl joins base, path and the non-empty query values. Pure. */
export function buildUrl(baseUrl: string, path: string, query: Record<string, QueryValue> = {}): string {
  const base = baseUrl.replace(/\/+$/, "");
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === "" || v === false) continue;
    params.set(k, String(v));
  }
  const qs = params.toString();
  return `${base}${path.startsWith("/") ? path : `/${path}`}${qs ? `?${qs}` : ""}`;
}

/** seg encodes one path segment. Pure. */
const seg = (s: string): string => encodeURIComponent(s);

interface SendOptions {
  query?: Record<string, QueryValue>;
  body?: unknown;
  headers?: Record<string, string>;
}

interface Sent<T> {
  data: T;
  response: Response;
}

export class ApiClient {
  private readonly baseUrl: string;
  private readonly fetchImpl: FetchLike;
  private readonly auth: AuthProvider;
  private readonly init: RequestInit;

  constructor(opts: ApiClientOptions) {
    this.baseUrl = opts.baseUrl;
    this.fetchImpl = opts.fetch;
    this.auth = opts.auth;
    this.init = opts.init ?? {};
  }

  // ---- catalog ----

  async listModules(filter: ModuleFilter = {}): Promise<ModuleSummary[]> {
    const { data } = await this.send<Page<ModuleSummary>>("GET", "/v1/catalog/modules", {
      query: { category: filter.category, status: filter.status },
    });
    return data.data;
  }

  async getModule(name: string): Promise<ModuleSummary> {
    return (await this.send<ModuleSummary>("GET", `/v1/catalog/modules/${seg(name)}`)).data;
  }

  async listVersions(name: string): Promise<VersionSummary[]> {
    const { data } = await this.send<Page<VersionSummary>>("GET", `/v1/catalog/modules/${seg(name)}/versions`);
    return data.data;
  }

  async getVersion(name: string, version: string): Promise<ModuleVersion> {
    return (await this.send<ModuleVersion>("GET", `/v1/catalog/modules/${seg(name)}/versions/${seg(version)}`))
      .data;
  }

  // ---- requests ----

  /**
   * createRequest posts a request. idempotencyKey must stay the same for retries of the same
   * form submission; the backend replays the original request for a repeated key and body.
   */
  async createRequest(input: CreateRequestInput, idempotencyKey: string): Promise<CreateRequestResult> {
    const { data, response } = await this.send<RequestSummary>("POST", "/v1/requests", {
      body: input,
      headers: { "Idempotency-Key": idempotencyKey },
    });
    return { request: data, replayed: response.headers.get("Idempotent-Replayed") === "true" };
  }

  async listRequests(filter: RequestFilter = {}): Promise<Page<RequestSummary>> {
    return (
      await this.send<Page<RequestSummary>>("GET", "/v1/requests", {
        query: {
          team: filter.team,
          status: filter.status,
          kind: filter.kind,
          mine: filter.mine ? "true" : undefined,
          limit: filter.limit,
          cursor: filter.cursor,
        },
      })
    ).data;
  }

  async getRequest(id: string): Promise<RequestSummary> {
    return (await this.send<RequestSummary>("GET", `/v1/requests/${seg(id)}`)).data;
  }

  // ---- transport ----

  private async send<T>(method: string, path: string, opts: SendOptions = {}): Promise<Sent<T>> {
    const headers: Record<string, string> = {
      Accept: "application/json",
      ...(await this.auth.requestHeaders(method)),
      ...opts.headers,
    };
    let body: string | undefined;
    if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }

    let response: Response;
    try {
      response = await this.fetchImpl(buildUrl(this.baseUrl, path, opts.query), {
        ...this.init,
        method,
        headers,
        body,
        credentials: this.auth.credentials,
      });
    } catch (err) {
      throw ApiError.network(err);
    }

    const decoded = await readJson(response);
    if (!response.ok) {
      throw new ApiError(parseProblem(response.status, decoded, response.headers.get("X-Request-Id") ?? undefined));
    }
    return { data: decoded as T, response };
  }
}

/** readJson decodes a JSON body, returning undefined for empty or non-JSON bodies. */
async function readJson(response: Response): Promise<unknown> {
  const text = await response.text();
  if (!text) return undefined;
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}
