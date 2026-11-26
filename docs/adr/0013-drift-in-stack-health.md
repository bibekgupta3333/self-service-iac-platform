# ADR-0013: Drift is recorded in stacks.health, and reconcile follows the RBAC matrix

- Status: Proposed
- Date: 2026-11-26
- Deciders: Platform team
- Related: [ADR-0012](0012-apply-reports-drive-lifecycle.md), [06-lifecycle-management.md](../06-lifecycle-management.md) section 4, [03-data-model-and-api.md](../03-data-model-and-api.md) section 4.2, [07-security-and-policy.md](../07-security-and-policy.md) section 7

## Context

docs/03 and docs/06 modelled drift as a lifecycle state: `active -> drifted` and back. A drift scan is read-only and runs beside requests, so a status written by it can collide with one a request is moving (an `updating` stack must not be flipped to `drifted`). ADR-0012 added `stacks.health` (`unknown`, `healthy`, `degraded`), the result of the post-apply smoke test, next to the status. Drift is the same kind of fact: something observed about a live stack, independent of where it is in its lifecycle.

Separately, the old docs/06 table said reconcile needs a developer in development and staging and a maintainer in production. The permission matrix in docs/07 section 7 says developer for reconcile and codify, maintainer for ignore, in every environment.

## Decision

1. **Drift is `stacks.health = 'drifted'`, never `stacks.status`.** Migration 0013 widens the check constraint of 0011 with `drifted`. `stack_status` keeps its `drifted` value for compatibility and nothing sets it.
2. **One column, two writers, explicit rules** (pure `drift.NextHealth`, tested for all four starting states): a drift finding sets `drifted` from any state; a clean scan only clears `drifted` to `healthy` and never touches `unknown` or `degraded`, because a refresh-only scan runs no smoke test; a failed scan changes nothing; an apply sets health from its smoke result (ADR-0012), which also clears `drifted` because an apply re-asserts the configuration.
3. **The lossy path is accepted and documented:** `degraded` -> drift finding -> `drifted` -> clean scan -> `healthy` forgets a smoke failure until the next apply's smoke test. Splitting the column into smoke health and a separate drift state is the alternative if the portal needs both at once.
4. **Reconcile follows the docs/07 matrix:** developer for reconcile and codify in every environment, maintainer for ignore. A reconcile is an `update` request created through the ordinary request path, so it is also subject to everything that path enforces (the caller's role in the team, queueing behind the stack lock, review of the PR); production review gates arrive with WBS 8.8 and apply to it like to any update. The docs/06 table is corrected to the matrix.
5. **Reconcile re-asserts the last applied configuration** (`stacks.last_applied_request_id`), and is refused with `409 stack_busy` while any request is open for the stack.

## Consequences

Positive:
- A scan can never disturb a request in flight; the portal reads one `health` field.
- The permission for each resolution is one row of one matrix.

Negative:
- A single column loses the earlier smoke result when drift is found (point 3).
- An apply clears `drifted` without a scan; the next night's scan re-finds drift the apply did not fix.

## Alternatives considered

- **A separate `drift_state` column.** Loses nothing, but the portal and every query then combine two columns for one badge. Revisit if point 3 hurts.
- **Keep drift as a lifecycle status.** Rejected: it races with requests and needs a transition table entry for every lifecycle state.
