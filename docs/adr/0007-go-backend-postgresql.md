# ADR-0007: Go backend with PostgreSQL

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team
- Related: [01-system-architecture.md](../01-system-architecture.md), [03-data-model-and-api.md](../03-data-model-and-api.md)

## Context

The backend is an orchestration service: REST API, webhook ingestion, background workers, state machines, GitHub and AWS SDK integrations, and HCL generation. It needs strong consistency for request state, locks and an append-only audit log, and it should be simple to deploy as small containers.

## Decision

- **Go** for the API and worker (single module in `backend/`): `chi` router, `pgx` driver, `sqlc` for type-safe queries, `golang-migrate` for migrations, `hashicorp/hcl/v2` `hclwrite` plus `text/template` in `internal/generator`, `go-github` for the GitHub App, AWS SDK v2.
- **PostgreSQL** (RDS Multi-AZ) as the system of record for teams, catalog, stacks, requests, runs, locks, budgets and audit events. JSONB for module input schemas, plan summaries and drift details.
- Asynchronous work via SQS (webhooks, scheduled jobs) with a transactional outbox table so state changes and enqueued work commit atomically.

## Consequences

Positive:
- Go fits the ecosystem (Terraform, OPA, Terratest, GitHub and AWS SDKs) and produces small static binaries with low memory use on Fargate.
- Postgres gives transactions for FSM transitions + audit + outbox, row-level locking for the platform lock, and JSONB flexibility for manifests.
- `sqlc` keeps SQL explicit and reviewable.

Negative:
- More boilerplate than higher-level frameworks; mitigated by code generation (`sqlc`, OpenAPI).
- Relational schema migrations need discipline (expand/contract).
- Portal is TypeScript (Next.js), so two languages in the stack.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Node.js/TypeScript backend | Single language with portal, but weaker fit with Terraform/OPA/Terratest tooling and concurrency model for workers |
| Python (FastAPI) | Good productivity; weaker static typing and deployment footprint for this use |
| DynamoDB | Transactions across FSM, audit and outbox and ad-hoc reporting queries are harder; no strong need for its scale profile |
| Event-sourced store | Overkill; append-only `audit_events` gives the needed history |
