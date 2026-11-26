// Maps the backend's RFC 9457 problem+json bodies (backend/internal/httpapi/respond.go)
// to a typed error. parseProblem is pure; ApiError is the value the client throws.

/** One entry of problem.errors: an RFC 6901 pointer into the request body. */
export interface FieldError {
  pointer: string;
  code: string;
  message: string;
}

export interface Problem {
  type: string;
  title: string;
  status: number;
  code: string;
  detail?: string;
  instance?: string;
  request_id?: string;
  errors: FieldError[];
}

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

const str = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);

function toFieldError(v: unknown): FieldError | undefined {
  if (!isRecord(v)) return undefined;
  const pointer = str(v.pointer);
  const message = str(v.message);
  if (pointer === undefined || message === undefined) return undefined;
  return { pointer, message, code: str(v.code) ?? "invalid" };
}

/**
 * parseProblem turns a response status and decoded body into a Problem. Bodies that are not
 * problem documents (a proxy's HTML error page, an empty body) still produce a usable value.
 * Pure.
 */
export function parseProblem(status: number, body: unknown, requestId?: string): Problem {
  if (!isRecord(body)) {
    return {
      type: "about:blank",
      title: `HTTP ${status}`,
      status,
      code: status >= 500 ? "internal" : `http_${status}`,
      request_id: requestId,
      errors: [],
    };
  }
  const errors = Array.isArray(body.errors)
    ? body.errors.map(toFieldError).filter((e): e is FieldError => e !== undefined)
    : [];
  return {
    type: str(body.type) ?? "about:blank",
    title: str(body.title) ?? `HTTP ${status}`,
    status: typeof body.status === "number" ? body.status : status,
    code: str(body.code) ?? `http_${status}`,
    detail: str(body.detail),
    instance: str(body.instance),
    request_id: str(body.request_id) ?? requestId,
    errors,
  };
}

/** ApiError is thrown by ApiClient for any non-2xx response or transport failure. */
export class ApiError extends Error {
  readonly problem: Problem;

  constructor(problem: Problem) {
    super(problem.detail ? `${problem.title}: ${problem.detail}` : problem.title);
    this.name = "ApiError";
    this.problem = problem;
  }

  get status(): number {
    return this.problem.status;
  }

  get code(): string {
    return this.problem.code;
  }

  get fieldErrors(): readonly FieldError[] {
    return this.problem.errors;
  }

  get requestId(): string | undefined {
    return this.problem.request_id;
  }

  /** network builds the error used when fetch itself rejects (API down, DNS, CORS). */
  static network(cause: unknown): ApiError {
    const detail = cause instanceof Error ? cause.message : String(cause);
    return new ApiError({
      type: "about:blank",
      title: "Could not reach the platform API",
      status: 0,
      code: "network_error",
      detail,
      errors: [],
    });
  }
}

export const isApiError = (e: unknown): e is ApiError => e instanceof ApiError;
