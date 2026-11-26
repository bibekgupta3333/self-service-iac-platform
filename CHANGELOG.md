# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project uses [Semantic Versioning](https://semver.org/).
Infrastructure modules are versioned separately with tags `modules/<name>/vX.Y.Z`. Their changes go in each module's own `CHANGELOG.md`.

## [Unreleased]

### Added
- ADR-0009: local-first development with LocalStack and the Terraform local backend until MVP exit.
- Monorepo layout: `modules/`, `policies/`, `environments/`, `tests/`, `backend/`, `portal/`, `tools/`, `.github/`.
- `docker-compose.yml` with LocalStack 4.4 and PostgreSQL 17, both with health checks, plus `.env.example`.
- `Makefile` targets for the local stack, Terraform checks, OPA tests, module tests and Go/Python tests; `.editorconfig`; `.terraform-version` (1.13.5).

- Policy as code (WBS 6.1–6.3):
  - `.tflint.hcl` with the pinned terraform and aws rulesets.
  - OPA policies on plan JSON under `policies/opa`:
    - required `iacp:*` tags; S3/SQS encryption with customer-managed KMS keys and key rotation
    - no public S3 access, ACLs, policies or `NotPrincipal`; bucket controls linked through configuration references
    - IAM wildcard and sensitive-read deny, `NotResource` deny, AWS-managed admin policy attachments denied
    - allowed regions with no implicit region; exact `?ref=modules/<name>/vX.Y.Z` module pins
    - no `force_destroy` outside development; forbidden `external`/`http` data sources and provisioners
    - the environment comes only from `--data context.json`, and the forbidden and production public-policy rules cannot be waived
    - 168 tests at 99.9% coverage
  - Conftest plan fixtures with `context.json` and `verify.sh`, which asserts all 24 rule IDs.
  - Custom Checkov checks `IACP_AWS_001`–`003` (tags, S3 KMS CMK, Lambda tracing and concurrency) with a pytest suite.
- CI workflow `.github/workflows/ci.yml`:
  - Jobs: actionlint, Terraform fmt/validate, TFLint and Checkov; OPA and conftest; module tests against a LocalStack service container; Go tests with a Postgres service; Terratest; Python tests.
  - Every action is pinned by commit SHA, permissions are read-only, and each job skips when its code doesn't exist yet.
- `.github/pull_request_template.md` and `.github/CODEOWNERS`.

- Go control plane `backend/` (WBS 4.1–4.3, 4.5 partial, 4.7, 4.11):
  - chi API with slog JSON logs, server timeouts and graceful shutdown
  - goose migrations with an advisory lock
  - catalog endpoints, and an internal index endpoint behind a token
  - `POST/GET /v1/requests` with a pure state machine (apply retries capped at 2), Idempotency-Key replay (24 h window) and JSON Schema input validation
  - deterministic config generator (golden-tested, `terraform fmt` and TFLint clean) with local and S3 backend strategies
  - hash-chained audit log computed in Go (ADR-0010), with a bounded `/v1/audit/verify`
  - distroless nonroot Dockerfile and an opt-in `backend` compose service
- Make targets `go-integration`, `generator-update` and `api`.

- PR validation pipeline (WBS 2.6, 5.1, 5.2 partial, 5.4):
  - `tools/stack_pipeline`, tested with pytest:
    - `detect` maps changed files to `environments/<env>/<team>/<stack>/` roots and emits a JSON matrix. It skips deleted roots and enforces the 20-stack and dependency-edge guard rails. A pipeline change re-validates every stack.
    - `context` builds the conftest data from `iacp.stack.json`.
    - `summary` renders the sticky PR comment and a JSON payload with a normalised plan hash.
    - `verify-pins` checks that published module tags resolve to the recorded commit. It serves unpublished tags from a local mirror, but only for commits already on `main`.
  - Smoke stack `environments/development/platform/smoke/`, rendered by the generator: `modules/s3` pinned to `modules/s3/v1.0.0`, LocalStack endpoints, local backend, `iacp:*` default tags, and a lock file for linux and darwin.
  - `.github/workflows/pr-validate.yml`:
    - Jobs: `detect`, then a per-stack matrix against LocalStack, then sticky comments, then a single `pr-validate-result` check.
    - Matrix steps: fmt, module pins, init, validate, TFLint, Checkov, plan, `show -json`, conftest, and a hermetic apply/re-plan/destroy round trip.
    - Only the comment job holds `pull-requests: write`.
    - Per-stack concurrency groups; provider and TFLint plugin caches. The API report is a `TODO(WBS 4.10)`.
  - Make targets `stack-detect`, `stack-plan`, `stack-apply`, `stack-destroy` and `stack-clean`.
  - Review hardening:
    - `detect` fails closed on any `environments/` path outside a valid stack root, and on a root with files but no `iacp.stack.json`.
    - Stack ids are joined with `__`, so they are injective.
    - New `verify-modules` step after init: the installed module calls must match the pins one to one, at the recorded commits.
    - Serving unpublished tags now requires `IACP_PIN_BOOTSTRAP` (`--allow-unpublished`).
    - The comment job writes the comment marker itself.

- Run reports and the apply pipeline (WBS 5.2 report, 5.3, 5.6):
  - `tools/stack_pipeline report` builds a `POST /v1/runs` body that mirrors `runs.Report` and `runs.Validate`, from the plan summary, `terraform output -json` (sensitive values redacted) and the runner environment. `head_sha` is always `GITHUB_SHA`.
  - `tools/stack_pipeline lock acquire|release` claims and releases the platform stack lock.
  - `api_client.py`: a GitHub OIDC token for the configured audience, falling back to `IACP_INTERNAL_TOKEN`. Tokens are masked, redirects are refused, and only idempotent calls are retried. Both commands skip with a notice while `IACP_API_URL` is empty.
  - `.github/actions/iacp-report`: a thin composite action over `report`. Every input passes through `env:`.
  - `pr-validate.yml`: a separate `report` job, the only one with `id-token: write`, reports each stack's summary as pipeline `validate`. It never affects `pr-validate-result`.
  - `.github/workflows/apply.yml`: on push to `main` (`environments/**`) or dispatch, per stack against LocalStack:
    - lock claim, verify-pins, init, verify-modules
    - plan (or `plan -destroy`), then a conftest pre-apply gate
    - plan report and the plan-hash check when an approved hash is given
    - apply, outputs, apply and summary reports, then lock release in an `always()` step
    - one apply per stack (`iacp-stack-<id>-apply`, never cancelled)
  - State is hermetic per job until the S3 backend arrives. That limit is documented in docs/05 and ADR-0009.
  - Review hardening:
    - The pr-validate `report` job is OIDC only, and `apply.yml` never receives `IACP_INTERNAL_TOKEN`. A job that can mint OIDC tokens no longer falls back to the shared token.
    - `apply.yml` runs only from `main`.
    - The plan-hash check fails closed once the API is configured, and both hashes are format-checked.
    - A lock claim is recorded before the call, so an ambiguous claim is still released (by request when no token came back). A failed release is reported as its own stage.
    - Destroy dispatches have their own concurrency group.
    - The dispatched `stack_path` and server text are kept out of workflow commands.
    - The workflow runs on every PR, and policy and tool-version changes re-validate every stack.
  - After the first module releases:
    - The smoke stack is pinned to the published `modules/s3/v1.0.0` (`681144a`).
    - `IACP_PIN_BOOTSTRAP` is off in the workflow and Makefile; `--allow-unpublished` stays as an opt-in.
    - Deleted-root detection counts only files tracked by git.
- Module registry MVP (WBS 3.1–3.5):
  - `modules/_shared/module.schema.json`, the JSON Schema for `module.yaml`.
  - `tools/manifest_validator`: schema, inputs-schema, variables.tf parity and `compatibility` against `required_version`/`required_providers` checks. Pytest suite; run it with `make validate-manifests`.
  - `modules/s3` v1.0.0:
    - KMS CMK, public access block, TLS-only and TLS ≥ 1.2 bucket policy, versioning
    - one lifecycle rule, optional access logging, `additional_policy_statements` and `key_service_principals`
  - `modules/sqs` v1.0.0: SSE-KMS queue with a DLQ and an inline redrive-allow policy, and `key_service_principals`.
  - `modules/lambda` v1.0.0:
    - execution role under `/iacp/` that always has the `iacp-boundary`
    - log group with retention, active tracing and reserved concurrency in production
    - tagged SQS event source with validated batching, KMS grants for queues, and guarded `extra_policy_jsons`
  - LocalStack examples with `policy-context.json` and `terraform test` suites (s3 14, sqs 11, lambda 23).
  - Terratest suite (`tests/`) for s3 and sqs that asserts the platform tag values. Run it with `make integration-test`.
- Module release (WBS 3.6, 3.7 partial):
  - `tools/module_release`, a SemVer interface-diff gate. It parses the module with `python-hcl2` (pinned 8.1.4) at the last `modules/<name>/vX.Y.Z` tag and at HEAD, classifies changes as major, minor or patch, and fails when `metadata.version` is bumped too little or not at all. Run it with `make module-gate`, which is also part of `make lint`.
    - It compares variables (type, default, `nullable`, validation conditions with string literals kept verbatim), outputs (name and `sensitive`; a non-literal `sensitive` is treated as sensitive), and the floors of `required_version` and `required_providers` (a raised minimum major version or a removed provider is major; providers are keyed by their implied `hashicorp/<name>` source when none is given).
    - It handles first releases and `0.x` modules, and fails when an already tagged version (prereleases included) changed, or when a prerelease is lower than an existing one.
  - `notes` builds release notes from the module's CHANGELOG section, or from a generated diff summary against the last final release below the declared version.
  - `index` posts the parsed `module.yaml` to `POST /v1/internal/catalog/index` in the backend's `IndexCommand` shape. It authenticates with `IACP_INTERNAL_TOKEN`, retries 5xx responses, refuses redirects, and accepts plain http only for loopback hosts. Setting only one of `IACP_API_URL` and `IACP_INTERNAL_TOKEN` is an error.
  - Pytest suite (209 tests, including temporary git repositories, a fake HTTP server and an opt-in `IACP_INTEGRATION=1` test against a running API).
  - `.github/workflows/module-release.yml`:
    - It runs on every PR, so the `semver gate result` check always reports. PRs that don't touch modules or the tooling get an empty matrix without installing Python.
    - On pushes to `main` (or a manual `workflow_dispatch` on `main`), it creates any missing tags and GitHub releases (also retrying tags that have no release), then calls the catalog index, which is skipped with a notice until `IACP_API_URL` and `IACP_INTERNAL_TOKEN` are configured.
    - Steps run with bash `pipefail`, and annotated tags are peeled to their commit before indexing.
    - Actions are pinned by SHA, and only the release job has `contents: write`.
    - The first merge to `main` creates `modules/{s3,sqs,lambda}/v1.0.0`.
- Per-stack platform lock (WBS 4.6), `backend/internal/locks`:
  - fencing tokens from `lock_fencing_seq`, a 7-day lease with heartbeat, takeover of expired leases, and a FIFO queue (`requests.queued_behind`) promoted on release
  - pure decisions (`Decide`, `NextInQueue`, `CheckToken`); the repository serialises each stack with `SELECT … FOR UPDATE` and a concurrency test proves one winner
  - `POST /v1/requests` acquires or queues in its own transaction; a queued request cannot open its PR
  - internal endpoints `GET/POST/DELETE /v1/internal/stacks/{id}/lock` and `POST …/lock/heartbeat`; `lock.acquired`, `lock.queued`, `lock.released` audit events
- GitHub Actions OIDC verifier (WBS 4.10), `backend/internal/auth`:
  - RS256 only, cached JWKS with refetch on an unknown `kid` (rate limited), `iss`/`aud`/`exp`/`nbf`/`iat` with leeway, and `repository`/`repository_owner`/`repository_id`/`ref`/`job_workflow_ref` allow-lists
  - configured with `IACP_OIDC_AUDIENCE` and `IACP_OIDC_REPOSITORY` (plus optional overrides); accepted on `/v1/internal/*` next to the shared token
- `POST /v1/runs` records run reports in the new `pipeline_runs` table (migration `0005`), checking the fencing token, the reporting run id and commit, and the workflow and trigger allowed for each pipeline. Replayed stage reports replace their entry instead of duplicating it, and reports lock the stack row before the lock row so they cannot deadlock with lock release.
- Backend hardening (WBS 4.1, 4.10, 6.2, 6.4, 6.6 partial):
  - `GET /metrics` in Prometheus format (`prometheus/client_golang` v1.24.1): request count and latency per method and chi route pattern (unmatched paths share one label), database pool gauges, Go and process collectors.
  - OIDC `jti` replay protection: `auth.ReplayGuard` claims each token id until `exp` plus leeway. The default store is the new `oidc_jti` table (migration `0006`), shared by every replica; `IACP_OIDC_REPLAY_STORE=memory` uses a bounded per-process cache instead.
  - `GET /v1/internal/budget-context?stack_path=` returns the conftest `--data` document: environment, allowed regions, the team's monthly budget from the new `budgets` table (migration `0007`), and the yanked module versions. It sits behind a new `budget_context` route class for `pr-validate.yml` and `apply.yml`.
  - OPA `iacp.modules.yanked` denies plans that create or update resources in a module pinned to a yanked version. `iacp.modules.yanked_pinned` only warns when the module is left alone or destroyed. 11 new tests; `verify.sh` also checks the rule against `context-yanked.json`.
  - `tools/codeowners` generates `.github/CODEOWNERS` from `.github/teams.yaml`. CI and `make lint` run `--check`, which fails on drift or on a stack whose team is not declared. The branch ruleset and Environment protection payloads are checked in under `.github/rulesets/`, for the owner to apply with `gh api` in WBS 2.5.
- Developer portal `portal/` (WBS 7.1–7.3, 7.5 partial):
  - Next.js 16 App Router with strict TypeScript, Tailwind 4, ESLint and Vitest + Testing Library; exact dependency pins and a committed `package-lock.json`; Node 24 from `portal/.nvmrc`
  - typed `ApiClient` (fetch, base URL and `AuthProvider` injected) with `ApiError` built from the backend's problem+json, including `errors[].pointer`
  - `AuthProvider` seam with `DevAuthProvider`, matching the backend's dev auth mode (no credentials, fixed login); GitHub OAuth plugs in later
  - same-origin proxy `/api/iacp/v1/...` for browser calls (the API sends no CORS headers):
    - forwards only an allow-list of user routes (`catalog`, `requests`; POST only on `/v1/requests`), matched exactly, with no dot segments or encoded slashes
    - allow-lists request and response headers and rewrites `Location` into the proxy prefix
    - answers HEAD without a body, rejects other methods (405) and bodies over 1 MiB (413)
    - rejects cross-site POSTs (403) using `Sec-Fetch-Site` or `Origin`
  - catalog list (category filters derived from the modules) and module detail with versions, outputs and an inputs table generated from the JSON Schema
  - Content-Security-Policy and other security headers on every response
  - request form generated from the version's input schema by a small typed renderer ([ADR-0011](docs/adr/0011-portal-form-renderer.md)):
    - the `Idempotency-Key` is bound to the payload: reused for unchanged retries, renewed when the values change
    - `idempotency_key_reuse` and ambiguous failures (network error, 5xx) are explained, with a link to the user's requests
    - backend validation errors are mapped onto fields by pointer, including missing required inputs
    - exclusive bounds are respected, and clearing an optional field sends null or an explicit empty value where the schema allows it
    - a preview shows the exact request
  - requests list (All/Mine, team filter, cursor paging) and request detail with a status timeline, refreshed every 5 s until terminal
  - CI job `portal` (npm ci, lint, typecheck, test, build) and make targets `portal-install`, `portal-dev`, `portal-test`

- Phase 2 modules `ecs-service`, `postgres` and `redis` v1.0.0 (WBS 8.1, partial). Each has a `module.yaml`, secure defaults, a plan-only `examples/complete` with `policy-context.json`, a README and a CHANGELOG:
  - `ecs-service`: Fargate service with a deployment circuit breaker and rollback, an ECR repository (scan on push, immutable tags, KMS, lifecycle policy), task and execution roles under `/iacp/` with the `iacp-boundary` permissions boundary, KMS-encrypted logs, ALB-only ingress, and an optional internal or public ALB with an HTTPS listener and an HTTP-to-HTTPS redirect
  - `postgres`: private RDS PostgreSQL with CMK storage, `rds.force_ssl=1`, an RDS-managed master password in Secrets Manager, backups, deletion protection and a final snapshot outside development, Multi-AZ in production and encrypted Performance Insights
  - `redis`: private ElastiCache replication group with CMK at-rest encryption, required TLS, an AUTH token in Secrets Manager or an RBAC user group, and automatic failover in production
  - Offline `terraform test` suites, because LocalStack community 4.4 has no ECS, ECR, ELBv2, RDS or ElastiCache: `plan` runs use the real provider with every endpoint on a closed local port, and `apply` runs use `mock_provider "aws"`
- OPA rules for the new resource types, each with positive and negative tests (213 tests, 99.9 % coverage):
  - `iacp.encryption.rds_storage`, `iacp.encryption.rds_cmk`, `iacp.encryption.elasticache_at_rest`, `iacp.encryption.elasticache_cmk`, `iacp.encryption.elasticache_transit`, `iacp.encryption.ecr_kms`
  - `iacp.network.rds_public`, `iacp.network.ecs_public_ip`
  - `iacp.ecr.scan_on_push`, `iacp.ecr.immutable_tags`
  - The non-compliant conftest fixture trips every new rule; `verify.sh` now asserts 30 deny and 4 warn rule IDs.
- `policies/example-plan.sh` and `make example-plans`: plan a module's `examples/complete` and run conftest with its `policy-context.json`, allowing only the deny for relative `../` module sources.
- CI: a `module-tests-offline` job runs suites that mock the AWS provider without LocalStack, and the `module-tests` job, which skips those suites, now plans and checks every module example with conftest. LocalStack suites run as before.

### Security
- The backend fails closed: it binds to loopback by default, refuses to start without `IACP_AUTH_MODE`, keeps internal endpoints behind a constant-time token check, and enforces team membership on request creation.
- The API runs as the `iacp_app` role, which can only INSERT and SELECT on `audit_events`. Triggers block UPDATE, DELETE and TRUNCATE.
- The local stack binds LocalStack and Postgres to loopback only. Images are pinned by digest.
- `make` forces LocalStack dummy credentials and unexports `AWS_PROFILE` and session tokens.
- Machine endpoints fail closed: with neither OIDC nor `IACP_INTERNAL_TOKEN` configured they reject every call, a half OIDC configuration stops the API from starting, and a genuine token from a workflow outside the allow-list gets `403 workflow_not_allowed`.
- OIDC callers are scoped per route class: lock routes accept only `apply.yml`/`ttl-reaper.yml` on main, catalog indexing only `module-release.yml` on main, so a pull-request token only reaches `/v1/runs`. Each pipeline also has an `event_name` allow-list (`pull_request_target` and `workflow_run` never qualify), and run reports must carry the token's `sha`.
- The JWKS endpoint must be https (loopback excepted) and redirects are not followed. Fetches are shared (singleflight) and detached from the caller, so a disconnecting client cannot block key refreshes.
- When `IACP_OIDC_REPOSITORY_ID` is set every token must carry it; the API warns when it is unset.
- `iacp_app` can only SELECT, INSERT and UPDATE `pipeline_runs`.
- `/metrics` shares the API listener but is closed by default. It answers direct loopback peers, never a request with forwarding headers, and otherwise needs `IACP_METRICS_TOKEN`, which is separate from the internal token. Methods outside the standard set are labelled `OTHER`, so junk methods cannot add series. The loopback rule assumes no same-host proxy relays outside traffic without forwarding headers.
- A replayed GitHub OIDC token or one without `jti` gets a 401. A replay-store outage also refuses the token, but with a retryable `503 replay_store_unavailable` and its own log line. A claim deletes only its own expired row; a background sweep purges the rest in batches of 1,000. `iacp_app` has no UPDATE on `oidc_jti`.
- Pull-request tokens can now also read the budget context, which is read-only, for any team's stack path (an accepted exposure, documented in docs/07). They still cannot take locks or index the catalog.
- `tools/stack_pipeline` mints a new GitHub OIDC token for every API request, retries included, because the API now rejects a reused `jti`; the shared internal token is still reused.
- `tools/codeowners` only counts git-tracked stack roots and rejects `platform_paths` under `/environments/<env>/`, which team rules would shadow.

### Changed
- `AGENTS.md`: local-first rules, the 70/30 object-oriented and functional coding style, and the small-commit, reviewed-PR workflow.

### Planned
- M0 Foundations (remaining): smoke stack against LocalStack. The real-AWS state bucket and OIDC roles are deferred by ADR-0009.
- M1 Module registry (remaining): upgrade-plan test and CHANGELOG/upgrade-notes check in `module-release.yml`, a live catalog index once a hosted API exists, Lambda Terratest.
- M2 Orchestration engine (remaining): GitHub App PRs, webhooks, OAuth/RBAC, `pr-validate.yml`, `apply.yml`.
- M3 Security gates (remaining): SARIF upload, wiring the budget context into the workflows, approval and budget policies, applying the ruleset and Environment protection.
- M4 Portal and destroy: Next.js dashboard, destroy flow, Infracost comments.

## [0.1.0] - 2026-11-24

### Added
- Project overview (`OVERVIEW.md`), work breakdown (`WBS.md`), agent guidelines (`AGENTS.md`).
- Design documentation in `docs/`:
  - `00-problem-analysis.md`: problem, goals, personas, requirements, build-vs-buy, risks.
  - `01-system-architecture.md`: components, control plane and execution plane, config generation.
  - `02-aws-infrastructure.md`: multi-account layout, state backend, OIDC roles, cost estimate.
  - `03-data-model-and-api.md`: PostgreSQL DDL, state machines, REST API.
  - `04-module-registry.md`: module contract, `module.yaml` spec, versioning.
  - `05-automation-pipeline.md`: GitHub Actions workflows and pipeline stages.
  - `06-lifecycle-management.md`: upgrades, locking, drift, TTL, recovery, RBAC, budgets.
  - `07-security-and-policy.md`: threat model, IAM, OPA and Checkov policies.
  - `08-mvp-roadmap.md`: milestones, acceptance criteria, demo script.
  - `adr/0001`–`0008`: architecture decision records.
- Diagrams generated with Python (`diagrams`, graphviz, matplotlib) in `docs/diagrams/`:
  - Architecture, AWS infrastructure, CI/CD pipeline, backend components.
  - Data model (ERD) and request and stack state machines.
  - Sequence diagrams: provision request, apply on merge, drift detection, TTL teardown, module release.

[Unreleased]: https://github.com/bibekgupta3333/self-service-iac-platform/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/bibekgupta3333/self-service-iac-platform/releases/tag/v0.1.0
