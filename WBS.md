# Work Breakdown Structure (WBS)

Hierarchical breakdown of all work for the platform. Each leaf is a deliverable sized at 0.5–3 days (one engineer).
Milestones follow [docs/08-mvp-roadmap.md](docs/08-mvp-roadmap.md). Tick boxes as work lands, and update [CHANGELOG.md](CHANGELOG.md).

**Legend:** `[x]` done · `[~]` partial · `[ ]` to do · **Est** = engineer-days · **Dep** = depends on

## Summary

| ID | Work package | Milestone | Weeks | Est | Status |
|----|--------------|-----------|-------|-----|--------|
| 1 | Design & documentation | Pre-M0 | 0 | 5 | Done |
| 2 | Foundations | M0 | 1 | 5 | To do |
| 3 | Module registry | M1 | 2–3 | 10 | To do |
| 4 | Orchestration engine (Go backend) | M2 | 4–6 | 15 | To do |
| 5 | Automation pipeline | M2–M3 | 4–8 | 8 | To do |
| 6 | Security & policy | M3 | 7–8 | 8 | To do |
| 7 | Developer portal & destroy | M4 | 9–10 | 10 | To do |
| 8 | Lifecycle & governance | Phase 2 | 11–18 | 30 | To do |
| 9 | Scale & ecosystem | Phase 3 | 19+ | 25+ | To do |
| 10 | Project management & release | Ongoing | all | 5 | Ongoing |

**MVP total (1–7):** about 61 engineer-days, roughly 10 weeks.

---

## 1. Design & documentation (Done)
- [x] 1.1 Problem analysis, requirements, build-vs-buy (`docs/00`)
- [x] 1.2 System architecture & config generation design (`docs/01`)
- [x] 1.3 AWS multi-account infrastructure design (`docs/02`)
- [x] 1.4 Data model & REST API (`docs/03`)
- [x] 1.5 Module registry spec (`docs/04`)
- [x] 1.6 Automation pipeline design (`docs/05`)
- [x] 1.7 Lifecycle management design (`docs/06`)
- [x] 1.8 Security, threat model & policies (`docs/07`)
- [x] 1.9 MVP roadmap (`docs/08`) and ADRs 0001–0008
- [x] 1.10 Python-generated diagrams (architecture, AWS, CI/CD, ERD, state machine, 5 sequences)
- [x] 1.11 OVERVIEW, WBS, CHANGELOG, AGENTS

## 2. Foundations — M0
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [x] 2.1 | Monorepo skeleton (`modules/ policies/ environments/ tests/ backend/ portal/ .github/`) | 0.5 | 1 |
| [x] 2.2 | `docker-compose.yml`: Postgres, LocalStack; Makefile targets | 1 | 2.1 |
| [ ] 2.3 | *(Deferred by ADR-0009)* Bootstrap Terraform: state bucket (SSE-KMS, versioning, TLS-only), CMK | 1 | 2.1 |
| [ ] 2.4 | *(Deferred by ADR-0009)* GitHub OIDC provider + `iacp-plan` / `iacp-apply` roles + `iacp-boundary` (dev, staging) | 1 | 2.3 |
| [ ] 2.5 | GitHub setup: repo rulesets, GitHub App registration, Environments, `iacp-approver` user | 1 | 2.1 |
| [x] 2.6 | Smoke stack `environments/development/platform/smoke/` plans against LocalStack (OIDC once ADR-0009 ends); generator-rendered, applies in a hermetic CI round trip | 0.5 | 2.2 |
| [x] 2.7 | ADR-0009: local-first development (LocalStack + Terraform local backend) | 0.5 | 1 |

**Exit:** `docker compose up` is healthy, and the smoke stack plans in CI against LocalStack with no real credentials.

## 3. Module registry — M1
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [x] 3.1 | `module.yaml` JSON Schema + validator + variables.tf parity check | 1 | 2.1 |
| [x] 3.2 | `modules/s3` (KMS, public access block, versioning, lifecycle, outputs) + examples + `terraform test` | 2 | 3.1 |
| [x] 3.3 | `modules/sqs` (+ DLQ, redrive, KMS) + examples + tests | 1.5 | 3.1 |
| [x] 3.4 | `modules/lambda` (IAM role least-privilege, log group, SQS trigger option) + tests | 2 | 3.1 |
| [x] 3.5 | Terratest suites against LocalStack (`tests/`) | 1.5 | 3.2–3.4 |
| [x] 3.6 | `module-release.yml`: checks, tests, SemVer interface-diff gate, tag `modules/<n>/vX.Y.Z`, release notes (static checks and tests run in `ci.yml`; upgrade-plan test pending) | 1.5 | 3.5 |
| [~] 3.7 | Catalog index call (`POST /v1/internal/catalog/index`) (client and workflow step done; skipped until `IACP_API_URL` and `IACP_INTERNAL_TOKEN` exist, ADR-0009) | 0.5 | 4.3, 3.6 |

**Exit:** merging to `modules/s3` creates a tag, a release, and a catalog row. A breaking change with only a minor bump fails.

## 4. Orchestration engine (Go backend) — M2
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [x] 4.1 | Backend scaffold: chi, config, structured logging, health checks, migrations, Prometheus `/metrics` (route-pattern labels, pool gauges; loopback or `IACP_METRICS_TOKEN`) | 1 | 2.2 |
| [x] 4.2 | DB migrations for all core tables (incl. hash-chained `audit_events`, `locks`) | 1.5 | 4.1 |
| [x] 4.3 | Catalog service: `GET /v1/catalog/modules`, `/versions`, index endpoint | 1 | 4.2 |
| [~] 4.4 | Auth: GitHub OAuth login, sessions/JWT, team sync → memberships, RBAC middleware (membership check on create done) | 2 | 4.2 |
| [x] 4.5 | Request service + state machine + `Idempotency-Key` (create, update and destroy kinds; `upgrade`, `reconcile` and `rollback` are rejected until their own tasks) | 2 | 4.2 |
| [x] 4.6 | Platform per-stack lock, fencing token, queueing (`queued_behind`) (admin force-release on `/v1/stacks/{id}/lock` lands with RBAC, 4.4) | 1 | 4.5 |
| [x] 4.7 | Config generator: templates (main/backend/versions/providers/`iacp.stack.json`), golden tests | 2 | 4.3 |
| [~] 4.8 | GitHub App client: branch, commit, PR, labels, workflow_dispatch (Publisher port, `githubapp` and `localgit` adapters, publish on create via the outbox done; live use waits for the App registration in 2.5) | 1.5 | 2.5 |
| [~] 4.9 | Webhook ingester (HMAC, delivery dedupe) → SQS → worker status updates (Postgres outbox + in-process worker instead of SQS per ADR-0009; `pull_request` and `workflow_run` mapped; review, `check_run`, `push` and installation events pending) | 1.5 | 4.5 |
| [x] 4.10 | OIDC JWT verifier for `POST /v1/runs` and `/v1/internal/*` (verifier, allow-lists, run provenance, `POST /v1/runs`, `jti` replay protection in `oidc_jti` shared by replicas) | 1 | 4.1 |
| [x] 4.11 | Audit writer with hash chain + `/audit/verify` | 0.5 | 4.2 |

**Exit:** `POST /v1/requests` opens a PR in under 10 s with golden-matching files, and the request reaches `applied` after merge.

## 5. Automation pipeline — M2/M3
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [x] 5.1 | Changed-stack detection + matrix (`tools/stack_pipeline detect`) | 0.5 | 2.1 |
| [~] 5.2 | `pr-validate.yml`: fmt, validate, plan (plan role), PR comment, report to API (LocalStack instead of the plan role per ADR-0009; API report wired through the 5.6 action in a separate `report` job, skipped until `IACP_API_URL` is set; SARIF (6.1) and Infracost (5.5) wired) | 2 | 2.4, 4.10 |
| [~] 5.3 | `apply.yml`: lock claim, re-plan, plan-hash verify, apply (apply role), outputs report (LocalStack and hermetic local state per ADR-0009; `GET /v1/internal/requests/by-path` now supplies the lock target and approved plan hash, so `IACP_API_URL` can be set; request and stack status transitions from the apply reports are still pending; apply role and GitHub Environments pending) | 2 | 5.2, 4.6 |
| [x] 5.4 | Concurrency groups per stack, caching (providers, plugins; `apply.yml` shares pr-validate's provider cache key and runs no TFLint) | 0.5 | 5.2 |
| [x] 5.5 | Infracost step + PR comment (informational in MVP; per-stack `infracost breakdown` in its own `cost` job on a redacted plan, folded into the summary and sticky comment by `stack_pipeline fold-cost`; skipped with a notice until the `INFRACOST_API_KEY` secret is set, no `diff` against `main` yet) | 1 | 5.2 |
| [x] 5.6 | Reusable `.github/actions/iacp-report` composite action | 1 | 5.2 |
| [~] 5.7 | Smoke tests / Terratest post-apply for ephemeral envs (`stack_pipeline smoke` in `apply.yml`: declared outputs, `iacp:*` tags in state and live on LocalStack, reported as stage `smoke`; Terratest stays in `ci.yml` on every PR. Pending: `health=degraded` instead of a failed run needs backend support, and ephemeral stacks arrive with WBS 8.4) | 1 | 3.5, 5.3 |

## 6. Security & policy — M3
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [x] 6.1 | TFLint config + Checkov (+ custom `IACP_*` checks); SARIF uploaded to code scanning from `ci.yml` and `pr-validate.yml` (one category per tool and stack) | 1.5 | 5.2 |
| [x] 6.2 | OPA policies: tags, encryption, no public access, source allow-list + exact pin, IAM wildcard deny, env restrictions; yanked-version check (`iacp.modules.yanked`) from the budget-context `yanked_module_versions` | 3 | 5.2 |
| [x] 6.3 | `opa test` suite with ≥ 90% rule coverage | 1 | 6.2 |
| [x] 6.4 | `GET /v1/internal/budget-context` for conftest input (`budgets` table, `budget_context` route class; workflows not wired yet, 5.2/5.6) | 0.5 | 4.2 |
| [ ] 6.5 | Apply-role scoping + boundary validated with IAM Access Analyzer | 1 | 2.4 |
| [~] 6.6 | CODEOWNERS generation, branch ruleset, Environment protection for staging/prod (`tools/codeowners` + CI `--check` done; ruleset and Environment payloads checked in under `.github/rulesets/`, applying them is part of 2.5) | 1 | 2.5 |

**Exit:** OPA denies `force_destroy` in staging and `?ref=main`, with readable messages in the PR and the portal.

## 7. Developer portal & destroy — M4
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [~] 7.1 | Next.js scaffold, GitHub login, API client, layout — scaffold, `ApiClient`, proxy, layout and `AuthProvider` with the dev-mode provider done; GitHub login waits for 4.4 | 1.5 | 4.4 |
| [~] 7.2 | Catalog list + module detail (README, versions, inputs) — list, versions, outputs and generated inputs table done; README rendering needs the README in the catalog API | 1.5 | 4.3 |
| [~] 7.3 | Dynamic request form from JSON Schema (typed renderer, [ADR-0011](docs/adr/0011-portal-form-renderer.md)), preview diff — form, payload-bound Idempotency-Key, field-mapped errors and request preview done; generated-files diff needs `POST /v1/requests/{id}/preview` | 2 | 4.7 |
| [ ] 7.4 | Request detail: status timeline (SSE), plan summary, cost, PR/run links (backend `GET /v1/requests/{id}/events` is ready; the portal side is open) | 1.5 | 4.9 |
| [~] 7.5 | Stacks list per team, outputs view — requests list and detail with status timeline (polling) done; `GET /v1/stacks` and `GET /v1/stacks/{id}` (status, outputs) are available; the portal views are open | 1 | 4.5 |
| [ ] 7.6 | Destroy flow: `plan -destroy` → apply → cleanup PR → `destroyed` (the `destroy` request and its empty marked root are done under 4.5; the cleanup PR and the `destroyed` transition are open) | 1.5 | 5.3 |
| [ ] 7.7 | Observability: CloudWatch dashboard (lead time, failure rate), alarms | 1 | 4.1 |

**MVP exit:** the end-to-end demo script in `docs/08-mvp-roadmap.md` §4 passes in development and staging.

## 8. Lifecycle & governance — Phase 2
| ID | Task | Est | Dep |
|----|------|-----|-----|
| [~] 8.1 | Packages: `ecs-service` (+ALB, ECR), `postgres`, `redis`, `vpc`, `monitoring` — `ecs-service`, `postgres` and `redis` v1.0.0 done with offline `terraform test` suites (LocalStack community has no ECS/RDS/ElastiCache), new OPA rules and conftest-checked example plans; `vpc` and `monitoring` in a separate PR | 12 | 3.6 |
| [ ] 8.2 | Version upgrades: upgrade-path API + UI, major migration flow | 3 | 4.3, 7.2 |
| [ ] 8.3 | Drift detection: `drift.yml` 02:00 UTC, `drift_reports`, reconcile/codify/ignore | 3 | 5.2 |
| [ ] 8.4 | TTL: EventBridge Scheduler, 24 h warning, auto destroy (non-prod), extend, hourly reaper | 3 | 7.6 |
| [ ] 8.5 | Budgets: `budget.rego`, Infracost gating, admin override, Cost Explorer actuals | 3 | 5.5, 6.2 |
| [ ] 8.6 | RBAC: viewer/developer/maintainer/admin, production gates, matrix tests | 2 | 4.4 |
| [ ] 8.7 | Recovery: failure classifier, retry (max 2, backoff), rollback PR, break-glass `state-surgery` | 3 | 5.3 |
| [ ] 8.8 | Production environment enablement | 1 | 8.6 |

## 9. Scale & ecosystem — Phase 3
- [ ] 9.1 Dependency DAG: `stack_dependencies`, output injection, topological apply and reverse destroy
- [ ] 9.2 Multi-account vending + multi-region state
- [ ] 9.3 More packages: SNS, API Gateway, EventBridge, DynamoDB
- [ ] 9.4 Policy exceptions self-service with expiry
- [ ] 9.5 GitHub merge queue
- [ ] 9.6 `iacp` CLI + Backstage plugin
- [ ] 9.7 Platform SLOs and error budgets

## 10. Project management & release (ongoing)
- [x] 10.1 Repo init, git author config
- [ ] 10.2 GitHub remote, README badges, issue/PR templates
- [ ] 10.3 Release v0.2.0 at M2 (walking skeleton), v1.0.0 at MVP exit
- [ ] 10.4 Demo video + portfolio write-up

## Critical path
`2.1 → 2.3 → 2.4 → 3.1 → 3.2 → 4.2 → 4.5 → 4.7 → 4.8 → 5.2 → 5.3 → 6.2 → 7.3 → 7.6 → MVP exit`
