# ADR-0014: Budget gate inputs, what it trusts, and what is deferred

- Status: Accepted
- Date: 2026-11-26
- Deciders: Platform team
- Related: [06-lifecycle-management.md §10](../06-lifecycle-management.md#10-budget-management), [07-security-and-policy.md §6.7](../07-security-and-policy.md#67-budget-limit), [ADR-0006](0006-opa-conftest-and-checkov.md), [ADR-0009](0009-local-first-localstack-and-local-backend.md)

## Context

WBS 8.5 adds team budgets and a policy gate (`policies/opa/budget.rego`) fed by the Infracost estimate. The original design in docs 06 and 07 had four parts: a team-wide committed total (`current_committed_usd`, summed from `cost_estimates` of the team's active stacks), a per-request override with a maximum delta (`budget_overrides`), a single-change cap of 5000 per month, and Cost Explorer actuals pulled by the worker. Review of the first implementation found two problems: the PR's own stack code could run at `terraform plan` time and shape the estimate, and the committed total, the override bound and the cap had been dropped without a decision record.

## Decision

1. **The estimate is the stack's whole monthly cost as planned, not a delta.** There is no Infracost `diff` against `main` yet. The gate compares `used + estimate` with the limit, where `used` is the largest of `spent_usd`, `actual_usd` and `current_committed_usd`. For an update the stack itself is excluded from the committed total, so it is not counted twice.
2. **Committed total, restored.** `stacks.committed_monthly_usd` holds each stack's committed monthly cost. The budget context serves `current_committed_usd`: the sum over the team's other active stacks, in the budget's scope (one environment for an environment budget, all for a team-wide one). It is written by `POST /v1/internal/budget-committed` (shared internal token only, audited as `stack.cost.committed`).
3. **Override, bounded.** The budget-level override (granted with `PUT /v1/teams/{slug}/budgets/{month}`) carries a reason, an expiry of at most 31 days and `max_delta_usd`. It waives a hard limit or the cap only in development and staging, only while the overrun it allows (`projected - limit`, or `estimate - cap`) is at most the bound (so several stacks under one override cannot overrun by more than that each), and never waives a missing estimate (the bound cannot be checked). It is refused on a production-scoped budget. The per-request `budget_overrides` table stays planned and is not needed for the gate.
4. **Single-change cap, restored.** `iacp.budget.single_change_cap` denies a stack whose estimate exceeds `single_change_cap_usd` (0 to 1,000,000), whatever the headroom. The cap is **opt-in per budget** (NULL, no cap, by default): the estimate is the whole stack, not the change, and a default would block every update of an existing stack above it, in production with no override. Once an Infracost diff or a recorded per-stack committed cost makes the delta known, the cap can apply to the change and a default can be considered.
5. **The plan is made safe before it is priced.** A static check (`tools.stack_pipeline check-stack`, run from the base commit before `terraform init`, in isolation under `python -I`, with no fallback once the base has it) refuses everything in a stack root that could run code at plan time: any entry that is not a file the generator writes (so no `.terraform` directory of provider binaries), any block except `terraform`, `provider`, `module`, `variable`, `output`, `locals` and `moved`, provisioners, providers outside the allow-list (`hashicorp/aws`, `hashicorp/random`) in code and in the lock file, lock versions and hashes the base commit does not already hold, backends other than `local` and `s3`, module sources other than the platform repository at an exact release tag, JSON configuration and symlinks. Any backslash or template in the lock file is refused (Terraform decodes escapes, the scanner does not), and every quoted string in its hash list must be a hash the base already holds. The Terraform plugin cache is kept under `$RUNNER_TEMP`, `.terraform` is removed before `init`, and `pr-validate.yml` restores no provider or TFLint plugin cache (a PR can save into the cache scope it restores from) and keeps both plugin directories out of the checkout; the lock is read without comments and without heredocs, over the whole provider block; `apply.yml` and `drift.yml` use an exact key on `main`.

## Deferred, and why

- **Nothing records the committed cost yet.** `apply.yml` does not price a plan (the Infracost key lives only in the isolated cost job of `pr-validate.yml`), so no caller of `budget-committed` exists. Until one does, `current_committed_usd` is 0 and `spent_usd` is not filled, so the limit is effectively **per stack**: a team can split spend over several stacks, each under the limit. The follow-up is to carry the approved estimate from the validate run to the request and record it when the apply succeeds (or to price the plan in `apply.yml` in an isolated job). Tracked in `WBS.md` 8.5.
- **Cost Explorer actuals and `alert_thresholds` alerts** need a real AWS account (ADR-0009). `POST /v1/internal/budget-actuals` exists for a future worker.
- **A destroyed or failed stack's committed cost** is only excluded by status (`destroyed`). A failed apply that left resources is not distinguished.
- **The pre-apply gate (WBS 8.8)** that re-evaluates policy from `main` at apply time is the backstop for a merged change to `.github/`, `tools/` or `policies/`. Until it exists the `pr-validate` budget job is a strong early check, not the last word.

## Consequences

- The gate's honesty depends on one writer that does not exist yet; the docs say so in 06 §10 and 07 §6.7.
- A team with a staging or production budget while Infracost is disabled has every change denied (`no_estimate`); enabling Infracost first is documented.
- The admin API grows two bounded knobs (`max_delta_usd`, `single_change_cap_usd`) that a reviewer can audit in `budget.set` and `budget.override.granted` events.
