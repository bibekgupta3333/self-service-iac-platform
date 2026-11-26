// Pure rules for the Idempotency-Key of the request form (docs/03 §5).
//
// The backend remembers a key with the hash of the body it was first used with:
// - same key + same body  -> the original request is replayed (safe retry);
// - same key + other body -> 422 idempotency_key_reuse;
// - a create that fails validation stores nothing, so the key can be retried.
//
// So a key is bound to one payload. Retrying the same payload (after a timeout, a 5xx or a
// validation error) reuses the key, which can never create a second request. Changing the
// payload mints a new key, which can never hit idempotency_key_reuse.

import { isApiError } from "@/lib/api/errors";

export interface KeyedPayload {
  key: string;
  /** Serialised request body the key was minted for. */
  payload: string;
}

/** keyFor returns the key to send for payload: the previous one if the body is unchanged. */
export function keyFor(prev: KeyedPayload | null, payload: string, mint: () => string): KeyedPayload {
  return prev !== null && prev.payload === payload ? prev : { key: mint(), payload };
}

/**
 * - rejected: the backend answered definitively and stored nothing (4xx).
 * - ambiguous: no definitive answer (network error, 5xx); the request may exist.
 * - key_reused: the key was already used with another body.
 */
export type FailureKind = "rejected" | "ambiguous" | "key_reused";

export const IDEMPOTENCY_KEY_REUSE = "idempotency_key_reuse";

/** classifyFailure sorts a failed submit. Pure. */
export function classifyFailure(err: unknown): FailureKind {
  if (!isApiError(err)) return "ambiguous";
  if (err.code === IDEMPOTENCY_KEY_REUSE) return "key_reused";
  return err.status === 0 || err.status >= 500 ? "ambiguous" : "rejected";
}
