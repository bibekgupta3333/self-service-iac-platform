import { isApiError } from "./errors";

export type Result<T> = { ok: true; data: T } | { ok: false; error: unknown };

/** attempt runs an API call and captures its failure as a value for server components. */
export async function attempt<T>(fn: () => Promise<T>): Promise<Result<T>> {
  try {
    return { ok: true, data: await fn() };
  } catch (error) {
    return { ok: false, error };
  }
}

/** isNotFound reports whether a captured failure is a 404 from the API. Pure. */
export const isNotFound = (error: unknown): boolean => isApiError(error) && error.status === 404;
