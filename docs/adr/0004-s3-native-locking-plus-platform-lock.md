# ADR-0004: S3 native state locking plus a platform-level stack lock

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team
- Related: [06-lifecycle-management.md](../06-lifecycle-management.md#3-state-locking)

## Context

Terraform state must be protected from concurrent writes. Historically the S3 backend required a DynamoDB table for locking; since Terraform 1.10 the S3 backend supports native lockfiles using S3 conditional writes (`use_lockfile = true`), and DynamoDB locking is deprecated.

Terraform's lock only covers a single operation. The platform additionally needs to prevent two *requests* from editing the same stack concurrently: two open PRs for one stack would each be planned against stale config, and whichever merged second would apply a plan nobody approved.

## Decision

1. Use the **S3 backend with native lockfiles** (`use_lockfile = true`, Terraform >= 1.10) in the platform-account state bucket. No DynamoDB lock table.
2. Add a **platform-level lock**: one row per stack in Postgres `locks`, acquired when a request opens its PR and held until the request reaches a terminal status. Further requests for that stack queue FIFO.
3. Add a GitHub Actions `concurrency` group per stack on apply jobs as a third, cheap guard.
4. PR and drift plans run with `-lock=false` (read-only).

## Consequences

Positive:
- One fewer AWS resource and IAM surface (no DynamoDB table).
- Requests never race; the approved plan remains valid until applied.
- Users see clear queue status instead of opaque lock errors.

Negative:
- Requires Terraform >= 1.10 everywhere (pinned by the platform).
- Two lock systems to reason about; stale locks need runbooks (abandoned PR expiry, interrupted apply, force-unlock only via break-glass).
- Throughput per stack is serial by design.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| S3 + DynamoDB lock table | Deprecated path; extra resource; no benefit over native lockfile |
| Terraform lock only | Does not prevent conflicting PRs / unapproved plans |
| Platform lock only | Does not protect against an out-of-band Terraform run (break-glass, misconfigured workflow) |
| Postgres advisory locks held for request duration | Tied to a DB session; unsuitable for locks held across hours/days |
