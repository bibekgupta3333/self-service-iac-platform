# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project uses [Semantic Versioning](https://semver.org/).
Infrastructure modules are versioned separately with tags `modules/<name>/vX.Y.Z`. Their changes go in each module's own `CHANGELOG.md`.

## [Unreleased]

### Planned
- M0 Foundations: monorepo skeleton, docker compose (Postgres, LocalStack), bootstrap Terraform for the state bucket, KMS and OIDC roles.
- M1 Module registry: `modules/s3`, `modules/sqs`, `modules/lambda`, `module.yaml` schema, `module-release.yml`.
- M2 Orchestration engine: Go API, config generator, GitHub App PRs, webhooks, `pr-validate.yml`, `apply.yml`.
- M3 Security gates: TFLint, Checkov, OPA policies, budget context, environment protection.
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
