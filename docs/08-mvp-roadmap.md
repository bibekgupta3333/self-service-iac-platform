# 08 — MVP and Roadmap

> Purpose: define what is built first, in what order, how each milestone is accepted and demonstrated, and what follows the MVP.

Related: [00-problem-analysis.md](00-problem-analysis.md) · [01-system-architecture.md](01-system-architecture.md) · [04-module-registry.md](04-module-registry.md) · [05-automation-pipeline.md](05-automation-pipeline.md) · [06-lifecycle-management.md](06-lifecycle-management.md) · [07-security-and-policy.md](07-security-and-policy.md)

## Contents

1. [MVP scope](#1-mvp-scope)
2. [Prioritisation principles](#2-prioritisation-principles)
3. [MVP milestones](#3-mvp-milestones)
4. [End-to-end MVP demo script](#4-end-to-end-mvp-demo-script)
5. [Phase 2 and Phase 3](#5-phase-2-and-phase-3)
6. [Success metrics](#6-success-metrics)
7. [Portfolio and resume presentation](#7-portfolio-and-resume-presentation)
8. [Risks and mitigations](#8-risks-and-mitigations)

---

## 1. MVP scope

| # | MVP capability | In scope | Explicitly out of scope for MVP |
|---|---|---|---|
| 1 | Packages | `s3`, `sqs`, `lambda` modules meeting the module contract | ECS, RDS, Redis, VPC, Monitoring, SNS, API Gateway |
| 2 | Versioned registry | `module.yaml`, git tags, `module-release.yml`, catalog indexing | Deprecation/yank UI (API only), upgrade-path UI |
| 3 | Go API | Auth (GitHub OAuth), catalog, requests, generator, GitHub App integration, webhook ingester, worker, audit writer | Budgets, drift scheduler, TTL scheduler |
| 4 | PR provisioning | Generate config → branch → PR → merge → apply | Auto-approval for dev, cross-stack wiring |
| 5 | Security / policy validation | TFLint, Checkov, OPA/conftest on plan JSON, OIDC roles, permission boundary | Budget policy, drift severity policy |
| 6 | State management | S3 backend per stack, native lockfile, platform lock row | Break-glass workflow (documented runbook only), cross-region replica |
| 7 | Simple dashboard | Catalog list, request form from JSON Schema, request/stack status, plan summary, links to PR/run | Cost charts, audit explorer, drift views |
| 8 | Create / destroy | `create` and `destroy` requests end to end | `upgrade`, `rollback`, TTL |

Environments for MVP: `development` and `staging` in one workload account each; `production` is modelled but disabled in config until Phase 2.

## 2. Prioritisation principles

1. **Registry, orchestration engine and security policies first.** These are the hard, differentiating parts; the frontend is a thin client over a well-specified API, and a CLI (`curl`/`httpie` scripts) can demo everything before the portal exists.
2. **Walking skeleton early.** One request must travel portal/API → PR → plan → merge → apply → status by the end of M2, even if ugly.
3. **Security is not a later phase.** OIDC-only AWS access, scoped roles and policy gates are in the first pipeline that touches AWS.
4. **Local-first.** Every milestone runs against LocalStack via `docker compose` before touching a real account.

## 3. MVP milestones

Durations assume one engineer; adjust linearly.

### M0 — Foundations (week 1)

| Tasks |
|---|
| Monorepo skeleton per layout in [01-system-architecture.md](01-system-architecture.md); `docker compose` with Postgres, LocalStack, backend, portal |
| Platform bootstrap Terraform (applied once by an admin; see [02-aws-infrastructure.md](02-aws-infrastructure.md)): state bucket + CMK, GitHub OIDC provider, `iacp-plan`/`iacp-apply` roles + `iacp-boundary` in dev/staging accounts |
| GitHub org setup: repo rulesets, GitHub App registration (permissions: contents rw, pull requests rw, checks r, actions rw, metadata r), Environments |
| ADRs 0001–0008 accepted ([adr/README.md](adr/README.md)) |

**Acceptance:** `docker compose up` brings all services healthy; a hand-written stack in `environments/development/platform/smoke/` plans in GitHub Actions via OIDC with no static credentials in the repo or org secrets (except the Infracost key and App key).

### M1 — Module registry (weeks 2–3)

| Tasks |
|---|
| `modules/s3`, `modules/sqs`, `modules/lambda` with contract inputs/outputs, secure defaults, examples, `terraform test` |
| `module.yaml` JSON Schema (`modules/_shared/module.schema.json`) + validator + `variables.tf` parity check |
| Terratest suites against LocalStack ([05-automation-pipeline.md](05-automation-pipeline.md#7-terratest-moduless3-against-localstack)) |
| `module-release.yml`: static checks, tests, SemVer gate (interface diff), tag + release |
| Backend: `modules`, `module_versions` tables; `POST /v1/internal/catalog/index`; `GET /v1/catalog/modules`, `GET /v1/catalog/modules/{name}/versions` |

**Acceptance:**
- Merging a change to `modules/s3` with `metadata.version: 1.1.0` creates tag `modules/s3/v1.1.0`, a GitHub Release, and a `module_versions` row whose `inputs_schema` equals the manifest.
- Removing an input with a MINOR bump fails `module-release.yml`.
- Checkov reports zero failed checks on all three modules' `examples/complete`.

### M2 — Orchestration engine walking skeleton (weeks 4–6)

| Tasks |
|---|
| Auth: GitHub OAuth login, session cookie, team sync → `memberships` |
| Request service + FSM (`draft → … → applied/failed`), platform lock + queue |
| Config Generator: templates for `main.tf`, `backend.tf`, `versions.tf`, `providers.tf`, `iacp.stack.json`; golden-file tests; `terraform fmt` clean output |
| GitHub App client: branch, signed commit via Git Data API, PR with labels and body linking to the request |
| Webhook ingester (`pull_request`, `workflow_run`, `check_run`) → SQS → worker status updates; delivery-ID dedupe |
| `pr-validate.yml` (fmt, validate, plan, PR comment, API report) and `apply.yml` (re-plan, hash check, apply, outputs report) |
| OIDC JWT verification middleware for `POST /v1/runs` (Actions callbacks) and `/v1/internal/*` endpoints |
| Audit writer with hash chain |

**Acceptance:**
- `POST /v1/requests` for an S3 bucket produces a PR within 10 s whose files match golden output.
- After merge, the bucket exists in the dev account, the request is `applied`, stack `active`, outputs visible via `GET /stacks/{id}`.
- A second request for the same stack while the first is open is queued, then auto-promoted.
- Tampering with the plan between approval and apply (pushing a commit directly in a test repo) fails with `plan_mismatch`.
- Every transition above appears in `audit_events`; chain verification passes.

### M3 — Security and policy gates (weeks 7–8)

| Tasks |
|---|
| TFLint + Checkov (+ custom `IACP_*` checks) in `pr-validate.yml`, SARIF upload |
| OPA policies with unit tests (`opa test`): mandatory tags, encryption, no public access, module source allow-list + exact-tag pinning, IAM wildcard deny, environment restrictions (no `alpha` modules outside dev), yanked-version deny |
| `GET /v1/internal/budget-context` endpoint supplying team, budget and module context to conftest |
| `iacp-apply` role scoping per account + `iacp-boundary` verified with IAM Access Analyzer policy validation |
| Branch ruleset, CODEOWNERS generation, Environment protection |

**Acceptance:**
- A request with `force_destroy=true` in staging is blocked by OPA with a readable message in the PR comment and portal.
- A hand-crafted PR using a public registry module or `?ref=main` is denied.
- Policy test coverage ≥ 90% of rules with positive and negative cases (`opa test --coverage`).
- No IAM principal in workload accounts other than `iacp-apply`/break-glass can create resources tagged `iacp:managed-by=iacp` (SCP or boundary check documented in [07-security-and-policy.md](07-security-and-policy.md)).

### M4 — Dashboard and destroy (weeks 9–10)

| Tasks |
|---|
| Next.js portal: GitHub login, catalog list + module detail (README, versions, inputs), dynamic form from JSON Schema (`@rjsf/core`), request detail with status timeline, plan summary, links to PR and run; stack list per team |
| `destroy` request: `desired_state: absent` → `plan -destroy` → apply → cleanup PR → stack `destroyed` |
| Infracost in PR comment (informational only in MVP) |
| Basic observability: structured logs, `/metrics`, CloudWatch dashboard for lead time and failure rate |

**Acceptance:**
- A developer who has never used Terraform can create an SQS queue with DLQ and a Lambda consuming it (same stack) from the portal in under 5 minutes of interaction.
- Destroying that stack from the portal leaves no tagged resources (verified via Resource Groups Tagging API) and the state object is empty.
- Lighthouse accessibility score ≥ 90 on catalog and request pages.

### MVP exit criteria

- All eight MVP capabilities demonstrated by the script below in `development` and `staging`.
- Median request→applied lead time < 15 minutes for development (excluding human review wait).
- Zero long-lived AWS credentials anywhere in GitHub or the backend.
- Runbooks for failed apply and stale lock exist and have been exercised once.

## 4. End-to-end MVP demo script

Duration ~12 minutes. Pre-reqs: platform deployed, `payments` team with demo users `dev-alice` (developer) and `lead-bob` (maintainer).

| Step | Action | What the audience sees |
|---|---|---|
| 1 | Show repo tree and `modules/s3/module.yaml`; show tag list `modules/s3/v1.0.0`, `v1.1.0` | Registry = Git + manifest + tags |
| 2 | Portal → Catalog → S3 → versions; form rendered from JSON Schema | Typed inputs, secure defaults pre-filled, `force_destroy` danger toggle |
| 3 | As `dev-alice`, create `orders-events` stack in `development`: SQS (`max_receive_count=5`) + Lambda wired to it + S3 bucket | Request goes `draft → pr_open` in seconds; PR link appears |
| 4 | Open PR: generated HCL with exact `?ref=` pins; checks running | GitOps PR, no hand-written Terraform |
| 5 | Sticky comment: plan (+9 resources), Checkov/OPA pass, Infracost +$1.40/mo | Security and cost visible before merge |
| 6 | Edit the request to set `force_destroy=true` on S3 in `staging` copy | OPA deny message in PR + portal; request `failed` validation |
| 7 | Merge the dev PR (auto-approved in dev) | `apply.yml` runs with `iacp-apply` via OIDC; status `applying → applied` |
| 8 | Show AWS console / CLI: bucket encrypted with CMK, public access blocked, DLQ present; CloudTrail session name `apply-<run>-<stack>` | Secure defaults are real; traceability |
| 9 | Submit a second request on same stack while one is open | Queued behind the platform lock |
| 10 | Audit tab: full timeline with hash-chain verified badge | Auditability |
| 11 | Destroy stack | Destroy PR → apply → `destroyed`; cleanup PR removes directory |
| 12 | (Backup slide) Release `modules/s3` v1.2.0 via PR | Tag + catalog entry appear automatically |

## 5. Phase 2 and Phase 3

### Phase 2 — Lifecycle and governance (weeks 11–18)

| Item | Key tasks | Acceptance |
|---|---|---|
| Remaining packages | `ecs-service` (+ALB, ECR), `postgres`, `redis`, `vpc`, `monitoring`; Terratest against sandbox account for RDS/ElastiCache/ECS | Each reaches `stable` with full checklist ([04-module-registry.md](04-module-registry.md#132-review-checklist-pr-template-for-modules)) |
| Version upgrades | Upgrade-path API + UI, minor auto-suggest, major migration flow, SemVer upgrade-plan test in release | A minor upgrade produces no replace; a major requires acknowledgement of notes |
| Drift detection | Nightly 02:00 UTC `drift.yml` (`-lock=false`), EventBridge-driven sharding, `drift_reports`, reconcile/codify/ignore | Out-of-band tag edit detected within 24 h and reverted from portal |
| TTL | Scheduler integration, 24 h warning, auto destroy (bot-approved in non-prod), extend, hourly `ttl-reaper.yml` backstop | 72 h ephemeral stack is destroyed within 1 h of expiry with prior warning |
| Budgets + Infracost gating | `budgets`, `budget.rego`, admin override, Cost Explorer actuals | Over-budget plan blocked; override audited |
| RBAC | viewer/developer/maintainer/admin from GitHub teams, production enabled with maintainer + SRE gates | Developer cannot approve production; matrix in [06-lifecycle-management.md](06-lifecycle-management.md#9-rbac) enforced by tests |
| Recovery | Failure classifier, auto-retry (max 2 with backoff), rollback (revert PR), break-glass `state-surgery` workflow | Transient failure auto-recovers; rollback restores previous config |

### Phase 3 — Scale and ecosystem (weeks 19+)

| Item | Notes |
|---|---|
| Dependency DAG | Cross-stack wiring, `stack_dependencies`, topological propagation, reverse-order destroy |
| Multi-account / multi-region | Per-team accounts via AWS Organizations vending; stack placement by account; regional state buckets |
| More packages | SNS, API Gateway, EventBridge, DynamoDB; external module contributions by product teams |
| Policy self-service | Team-level policy exceptions with expiry and approval workflow |
| Merge queue | GitHub merge queue to reduce "branch up to date" friction at higher PR volume |
| CLI + Backstage plugin | `iacp` CLI over the same API; catalog surfaced in Backstage |
| SLOs | Platform SLOs (API availability 99.9%, p95 lead time) with error budgets |

## 6. Success metrics

| Metric | Definition | MVP target | Phase 2 target |
|---|---|---|---|
| Lead time to infrastructure | `requests.created_at` → `applied_at`, median, dev | < 15 min | < 10 min |
| Change failure rate | Failed applies / total applies (30 d) | < 10% | < 5% |
| Pipeline policy catch rate | Requests blocked by policy before apply / requests with violations (seeded tests) | 100% of seeded violations | 100% |
| Platform adoption | % of new AWS resources (by tag) created via the platform | 1 pilot team | > 60% of teams |
| Drift count | Stacks in `drifted` at any time | n/a | < 2% of stacks, MTTR < 2 days |
| Orphaned ephemeral cost | Spend on resources past `expires_at` | n/a | < 1% of dev spend |
| Ticket deflection | Infra tickets to platform team per month | baseline | −50% |
| Security posture | Static AWS keys in CI; Checkov HIGH findings in deployed stacks | 0 / 0 | 0 / 0 |

Metrics are computed from `requests`, `pipeline_runs`, `drift_reports`, `cost_estimates` and exposed on the platform dashboard and CloudWatch.

## 7. Portfolio and resume presentation

**What to show:**

- A 3-minute screen recording of the demo script (steps 2–8 and 11).
- The architecture and sequence diagrams ([01-system-architecture.md](01-system-architecture.md)) and the ADR index — evidence of deliberate trade-offs.
- One real PR from the platform with the sticky comment (plan, policy, cost).
- Test evidence: Terratest run output, `opa test --coverage` report, generator golden-file tests, Go coverage badge.
- A short write-up on a hard problem: plan-hash verification between approval and apply, or the two-layer locking model.

**Resume bullets (examples):**

- Designed and built *Self-Service IaC Platform*, a self-service infrastructure platform (Go, PostgreSQL, Next.js, Terraform, GitHub Actions) that turns portal requests into policy-checked pull requests and applies them via GitHub OIDC with zero static cloud credentials.
- Built a versioned Terraform module registry (SemVer git tags, JSON-Schema manifests, automated release and catalog indexing) with secure-by-default S3, SQS and Lambda packages validated by Checkov, OPA and Terratest on LocalStack.
- Implemented a stateful orchestration engine with a request state machine, per-stack locking and queueing, approved-plan hash verification and a hash-chained audit log.
- Added lifecycle automation — drift detection, TTL-based teardown, upgrade paths and Infracost-based budget enforcement — reducing orphaned dev spend and manual infra tickets.

## 8. Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| GitHub Actions minutes/concurrency limits at scale | Medium | Medium | Changed-stack matrix only; `max-parallel`; self-hosted runners (ECS/ARC) in platform account in Phase 3 |
| Plan hash mismatches from non-deterministic providers | Medium | Medium | Hash normalized `resource_changes` (address, actions, after); allow-list of known-volatile attributes; fallback to re-approval rather than blind apply |
| Generated HCL diverges from manual edits in `environments/` | Medium | High | `DO NOT EDIT` header, CODEOWNERS, CI check that regenerates from the API and diffs |
| GitHub outage blocks provisioning | Low | High | Requests stay queued in DB; nothing applies outside Git by design; documented break-glass for emergencies |
| LocalStack fidelity gaps hide real bugs | Medium | Medium | Nightly sandbox-account Terratest for every module; LocalStack only for fast feedback |
| Module SemVer violations break consumers | Medium | High | Interface diff + upgrade-plan test gate; yanking; OPA deny on stateful replace |
| Over-broad `iacp-apply` permissions | Low | Critical | Permission boundary, per-account scoping, Access Analyzer checks, SCP guardrails ([07-security-and-policy.md](07-security-and-policy.md)) |
| Scope creep into the portal | High | Medium | Principle §2.1; API-first acceptance criteria; portal milestone time-boxed |
| Infracost estimates inaccurate for usage-based services | High | Low | Treat as signal; compare with Cost Explorer actuals; usage files per module |
| Single maintainer / bus factor | Medium | High | ADRs, runbooks, golden-file tests, documented demo environment rebuild |
