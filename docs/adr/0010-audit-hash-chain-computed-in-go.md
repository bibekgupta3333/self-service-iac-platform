# ADR-0010: Audit hash chain computed in Go, enforced by PostgreSQL

- Status: Proposed
- Date: 2026-11-25
- Deciders: Platform team
- Related: [03-data-model-and-api.md §3](../03-data-model-and-api.md#3-hash-chained-audit-log), [ADR-0007](0007-go-backend-postgresql.md)

## Context

The first design of `audit_events` computed each row's hash inside a `BEFORE INSERT` trigger, over a `|`-joined string that included `payload::text`. Two problems showed up when building the backend:

- The verifier has to reproduce PostgreSQL's `jsonb` text output (key order by length then bytes, number formatting) exactly. That is hard to do outside the database and easy to break.
- The chain logic lived in plpgsql, so it could only be tested against a live database.

`AGENTS.md` asks for pure, side-effect-free logic with I/O only in adapters.

## Decision

1. The hash is computed by the Go audit writer with a pure function: `hash = sha256(prev_hash || CanonicalJSON(event))`. The canonical form is a JSON object with alphabetically ordered keys, a fixed microsecond timestamp format, and a payload that is decoded and re-encoded with sorted keys.
2. The writer takes the advisory lock `727274` inside the caller's transaction, reads the chain head, seals the event, and inserts it. The audit row commits or rolls back with the state change it describes.
3. PostgreSQL still guarantees the invariants: a `BEFORE INSERT` trigger takes the same lock and rejects a row whose `prev_hash` is not the current head. Separate triggers reject `UPDATE`, `DELETE` and `TRUNCATE`. The application role gets `INSERT` and `SELECT` only.
4. `VerifyChain` (pure) and `GET /v1/audit/verify` recompute any `seq` range from stored rows.

## Consequences

Positive:
- Chain logic is unit-tested without a database, with a pinned known-value test.
- Verification works on exported NDJSON with the same Go code, independent of PostgreSQL's `jsonb` formatting.
- Linkage and immutability are still enforced in the database, not trusted to the application.

Negative:
- A writer that bypasses the Go code must reproduce the canonical form. The only supported write path is `audit.Writer`.
- Changing the canonical form invalidates existing chains, so it needs a versioned migration of the format.

## Alternatives considered

- **Keep hashing in the trigger:** single source of truth in SQL, but the verifier must mirror `jsonb` text output and tests need a database.
- **Hash in Go with no database check:** simpler, but a buggy or compromised writer could fork the chain without detection until the nightly verification.
