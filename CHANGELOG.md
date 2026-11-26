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
- Module registry MVP (WBS 3.1–3.5):
  - `modules/_shared/module.schema.json`, the JSON Schema for `module.yaml`.
  - `tools/manifest_validator`: schema, inputs-schema and variables.tf parity checks. Pytest suite; run it with `make validate-manifests`.
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

### Security
- The backend fails closed: it binds to loopback by default, refuses to start without `IACP_AUTH_MODE`, keeps internal endpoints behind a constant-time token check, and enforces team membership on request creation.
- The API runs as the `iacp_app` role, which can only INSERT and SELECT on `audit_events`. Triggers block UPDATE, DELETE and TRUNCATE.
- The local stack binds LocalStack and Postgres to loopback only. Images are pinned by digest.
- `make` forces LocalStack dummy credentials and unexports `AWS_PROFILE` and session tokens.

### Changed
- `AGENTS.md`: local-first rules, the 70/30 object-oriented and functional coding style, and the small-commit, reviewed-PR workflow.

### Planned
- M0 Foundations (remaining): smoke stack against LocalStack. The real-AWS state bucket and OIDC roles are deferred by ADR-0009.
- M1 Module registry (remaining): `module-release.yml` tagging and catalog indexing (3.6–3.7), Lambda Terratest.
- M2 Orchestration engine (remaining): stack lock and queue, GitHub App PRs, webhooks, OIDC verifier, OAuth/RBAC, `pr-validate.yml`, `apply.yml`.
- M3 Security gates (remaining): SARIF upload, budget context, approval and budget policies, environment protection.
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
