# ADR-0012: Apply reports drive request and stack status, and the API releases the stack lock

- Status: Proposed
- Date: 2026-11-26
- Deciders: Platform team
- Related: [ADR-0002](0002-terraform-executes-in-github-actions.md), [ADR-0004](0004-s3-native-locking-plus-platform-lock.md), [03-data-model-and-api.md](../03-data-model-and-api.md) §4, §6.9, [06-lifecycle-management.md](../06-lifecycle-management.md)

## Context

`apply.yml` runs Terraform and reports its stages to `POST /v1/runs` (`plan`, `apply` or `destroy`, `smoke`, `summary`, and `release` when releasing the lock fails). Until now the API stored those reports and moved nothing: no stack left `provisioning`, and update and destroy requests, which need a stack that is not mid-flight, could not complete. The design (docs/03 §4, docs/05 step 12) also says a failed post-apply smoke test leaves the stack `active` with `health=degraded`, but the run status finished a run on any failed stage, and the workflow sends `summary` from `job.status`, which a failed smoke step turns red.

## Decision

1. **The reports are the source of truth for the apply outcome.** Inside the report's own transaction the API fires `apply_started` on the first accepted report of an `approved` request, and `apply_succeeded` or `apply_failed` when the run finishes, through the pure state machine (`Request.Fire`) and a pure decision (`requests.DecideApply`). The stack follows through `StackStatusWhileApplying` and `StackStatusAfterApply`, and records outputs, the applied commit and its health. Every move is audited. A `validate` report never moves anything: a green validation is not an approval. A request that was never approved gets `409 request_not_applying` and nothing is recorded.
2. **A failed smoke stage does not fail the run.** The run status ignores a failed `smoke` and `release` stage. A failed `summary` is settled as `succeeded` when the mutating stage passed and the smoke test is the only failure, so the stack ends `active` with `health=degraded` (new column `stacks.health`).
3. **The API releases the lock, not the workflow.** The terminal transition releases the lease and promotes the next waiter in the same transaction, using the path every other terminal transition uses. The workflow's own release step then meets a lease that is already gone. An explicit release by the request's own token is therefore idempotent once the request is terminal, and a later report carrying that token is accepted only for a run the API already recorded for that request, audited and ignored (it is not merged into the finished run, except the advisory `release` stage). A failed `release` report is audited with the lock's state and never flips an applied request. Any other stale token is still `409`.
4. **Fencing and provenance are unchanged.** Token against the current lease, `head_sha` equal to the token's `sha`, per-pipeline workflow and event allow-lists, run and attempt equality.

## Consequences

Positive:
- The create, update and destroy loops close end to end, the next queued request is published as soon as the previous one ends, and the audit chain shows every request and stack transition.
- One decision function, tested as a table, replaces ad-hoc status writes; replays and late reports are no-ops.

Negative:
- A transient failure is not retried: the state machine has `apply_retry`, but nothing re-dispatches `apply.yml`, so it fails the request like a permanent one until the recovery work (WBS 8.7).
- The backend infers "smoke only" from stage names, which couples it to `apply.yml`'s stage vocabulary. A stage that fails the job for another reason after a passed apply, with no smoke result, fails the request.
- A workflow that dies before sending `summary` leaves the request `applying` and holding the lock. Nothing acts on lease expiry yet (an expired lease stays current until a waiter takes it over, and even then the request stays `applying`), so an operator must intervene. A sweeper that fails such requests is a follow-up (WBS 8.7).
- The `drift` and `ttl_destroy` pipelines are still stored only.

## Alternatives considered

- **Let the workflow send an explicit `outcome` field.** Cleaner than inference, but it changes the workflow, the tools and the API together and cannot be relied on for runs already in flight. The stage-based rule can be replaced by it later.
- **Keep releasing the lock from the workflow.** The terminal transition must release it in its own transaction so the next request is published without a gap, and two releasers would race.
