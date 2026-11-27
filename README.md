# Self-Service IaC Platform

Developers ask for AWS infrastructure through a portal and get a reviewed pull request instead of a ticket. They pick versioned Terraform modules (S3, SQS, Lambda, ...), fill in typed inputs, and the Go control plane renders a pinned Terraform stack root and opens a PR. GitHub Actions then validates, lints, scans, policy-checks, prices and plans the change. A merge applies it, and the platform tracks the stack afterwards: locking, drift, TTL teardown, budgets, upgrades, rollback, and a tamper-evident audit log. Terraform never runs in the backend. The backend orchestrates, and Actions executes.

[![Watch the Self-Service IaC Platform walkthrough on YouTube](https://img.youtube.com/vi/kO7bbZ8YXxs/maxresdefault.jpg)](https://youtu.be/kO7bbZ8YXxs)

▶ **[Watch the walkthrough on YouTube](https://youtu.be/kO7bbZ8YXxs)**: the problem, the architecture, the pipeline checks, a local run from request to generated stack root, the lifecycle features and the security design.

![System architecture](docs/diagrams/images/architecture.png)

The longer story, with the design decisions and trade-offs, is in [docs/case-study.md](docs/case-study.md).

## What works today

All of this runs locally against LocalStack (see [Status and limits](#status-and-limits)).

- **Module catalog.** Nine modules (`s3`, `sqs`, `lambda`, `ecs-service`, `postgres`, `redis`, `vpc`, `monitoring`, `dynamodb`) ship with secure defaults, a `module.yaml` manifest with a JSON Schema for inputs, examples and tests. Each release gets a SemVer git tag `modules/<name>/vX.Y.Z`, and an interface-diff gate fails a breaking change that is not a major bump.
- **Request to generated PR.** `POST /v1/requests` validates the spec against the catalog, takes the stack lock and renders a deterministic stack root under `environments/<env>/<team>/<stack>/`, checked by golden files. The outbox worker then commits it and opens the PR.
- **Validate, scan, plan.** `pr-validate.yml` runs, per changed stack: a static stack guard, fmt, module pin verification, validate, TFLint, Checkov, plan, conftest (OPA) on the plan JSON, an isolated Infracost job, a budget gate, and a hermetic apply, re-plan and destroy round trip.
- **Apply.** `apply.yml` on `main` claims the lock, re-plans, checks the plan hash against the approved one, applies, runs a smoke test on outputs and tags, and reports each stage to the API.
- **Locking.** A per-stack platform lock with fencing tokens, a lease with heartbeat, takeover of expired leases and a FIFO queue.
- **Status stream.** `GET /v1/requests/{id}/events` is a server-sent event stream that resumes with `Last-Event-ID`. The portal timeline falls back to polling.
- **Drift.** A nightly `drift.yml` runs refresh-only plans, records `drift_reports`, marks stack health `drifted`, and supports reconcile, codify and ignore.
- **TTL.** Ephemeral stacks carry a TTL with bounded extensions, and the hourly `ttl-reaper.yml` opens destroy requests for expired stacks.
- **Budgets.** Team budgets come with an admin API, a bounded override and `budget.rego`, which denies a change that would exceed a hard limit.
- **Upgrades.** An upgrade-path API and `kind: upgrade` requests move one major at a time, with breaking-change acknowledgement and upgrade notes taken from the module changelog.
- **Recovery and rollback.** A pure failure classifier, automatic retries of transient apply failures (at most 2, with backoff), `POST /v1/stacks/{id}/rollback` to the last applied configuration, and a break-glass `state-surgery.yml` gated by a GitHub Environment.
- **RBAC.** Viewer, developer, maintainer and admin roles in a matrix, a route policy table that refuses unlisted routes, and audited denials. GitHub OAuth login, or a dev mode for local use.
- **Audit chain.** `audit_events` is append-only and SHA-256 hash-chained. Hashes are computed in Go, linkage is enforced by PostgreSQL triggers, and `GET /v1/audit/verify` recomputes the chain.

## Tech stack

| Layer | Choice |
|-------|--------|
| Infrastructure | Terraform (>= 1.10), AWS provider, LocalStack for local runs |
| Control plane | Go, chi, pgx, goose migrations, PostgreSQL |
| Portal | Next.js (React, TypeScript), Vitest |
| Pipeline | GitHub Actions (pinned by SHA), Python tooling in `tools/` |
| Policy and security | OPA/conftest, Checkov (with custom checks), TFLint, GitHub OIDC |
| Cost | Infracost |
| Testing | Go unit and integration tests, `terraform test`, Terratest, `opa test`, pytest, golden files |
| Diagrams | Python `diagrams` and graphviz |

## Quickstart

Needs Docker, Go, Node (version in `portal/.nvmrc`), Terraform and Python 3 for the tooling.

```bash
make up            # LocalStack and PostgreSQL on loopback (docker compose)
make api           # API on 127.0.0.1:8080, dev auth mode, migrations applied on start
make portal-dev    # portal on http://localhost:3000 (second terminal; npm ci on first run)
```

Index the modules into the catalog. The Python tools need a virtualenv:

```bash
python3 -m venv .venv && .venv/bin/pip install -r tools/requirements.txt
for m in s3 sqs lambda; do
  IACP_API_URL=http://127.0.0.1:8080 IACP_INTERNAL_TOKEN=local-dev-internal-token-0123456789abcdef \
    .venv/bin/python -m tools.module_release index "$m" --commit-sha "$(git rev-parse HEAD)"
done
```

Creating a request needs a team in which the dev user has the `developer` role. Until GitHub team sync runs, add one by hand (from [backend/README.md](backend/README.md#run)):

```bash
psql postgres://iacp:iacp-local-only@localhost:5432/iacp <<'SQL'
INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
VALUES ('payments', 'Payments', 1, 'payments', 'cc-0001');
INSERT INTO memberships (team_id, user_id, role)
SELECT t.id, u.id, 'developer' FROM teams t, users u
WHERE t.slug = 'payments' AND u.github_login = 'local-dev';
SQL
```

Send a sample request, or use the request form in the portal:

```bash
curl -s localhost:8080/v1/requests \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"kind":"create","team":"payments","environment":"development","stack":"order-events",
       "justification":"demo","resources":[{"name":"events_bucket","module":"s3",
       "version":"1.0.0","inputs":{"name":"order-events"}}]}'
```

With the default `make api` the request is recorded and stays in `draft`, because no publisher is configured. To see the generated branch and a simulated PR, run the API with `IACP_PUBLISHER=local` against a scratch clone, as in [backend/README.md](backend/README.md#try-it-locally-iacp_publisherlocal). To plan and apply a stack root against LocalStack, use `make stack-plan STACK=...` and `make stack-apply STACK=...`.

Checks: `make lint` (fmt, validate, TFLint, Checkov, OPA tests and repository checks), `make test` (Go, Python, OPA and module tests), `make portal-test`, `make integration-test` (Terratest). Run `make help` for every target.

## Repository layout

```
modules/        Terraform modules, each with module.yaml, README, examples/, tests/
policies/       opa/ (Rego on plan JSON, run by conftest) and checkov/ (custom checks)
environments/   generated stack roots <env>/<team>/<stack>/ (do not hand-edit)
backend/        Go API, outbox worker and config generator (internal/generator)
portal/         Next.js developer portal
tools/          Python pipeline tooling (stack_pipeline, module_release, ttl_reaper, ...)
tests/          Terratest integration tests against LocalStack
.github/        workflows: ci, pr-validate, apply, drift, ttl-reaper, module-release, state-surgery
docs/           design docs, ADRs, diagrams
```

## Documentation

- [OVERVIEW.md](OVERVIEW.md): the project on one page
- [docs/case-study.md](docs/case-study.md): problem, design decisions, hardening, trade-offs
- [docs/README.md](docs/README.md): design docs 00 to 09, in reading order
- [docs/adr/](docs/adr/README.md): architecture decision records
- [docs/diagrams/](docs/diagrams/README.md): diagram sources and rendered images
- [WBS.md](WBS.md): task status, [CHANGELOG.md](CHANGELOG.md): history, [AGENTS.md](AGENTS.md): contributor rules

## Status and limits

This is a portfolio project, and it is honest about what it does not do yet.

- **Local-first.** Under [ADR-0009](docs/adr/0009-local-first-localstack-and-local-backend.md) every AWS call goes to LocalStack with dummy credentials, and stacks use the Terraform `local` backend. State is therefore throw-away per CI job. The S3 backend and the real-AWS provider blocks are implemented in the generator but not wired.
- **Real AWS needs setup.** The state bucket, KMS key, GitHub OIDC provider and the `iacp-plan` / `iacp-apply` roles with their permission boundary are designed (docs/02) but not deployed. Cost Explorer actuals need a real account.
- **GitHub setup is manual.** The GitHub App adapter and the webhook ingester exist, but live PRs need an App registration. The branch ruleset and Environment payloads are checked in under `.github/rulesets/` and still have to be applied to the repository. Catalog indexing and run reports from CI are skipped until a hosted API URL and token exist.
- **Known gaps.** Nothing records a stack's committed cost yet, so the budget limit is effectively per stack ([ADR-0014](docs/adr/0014-budget-gate-inputs-and-deferrals.md)). Production is seeded with its RBAC gates and GitHub Environment binding, but the `prod_approval.rego` pre-apply check is written and tested, not yet run by `apply.yml` (WBS 8.8).
- **Not started.** Phase 3 items: cross-stack dependencies, multi-account vending, policy exceptions, merge queue, a CLI and platform SLOs.

See [WBS.md](WBS.md) for the task-level status.

## Regenerating diagrams

```bash
brew install graphviz
python3 -m venv .venv && .venv/bin/pip install -r docs/diagrams/requirements.txt
.venv/bin/python docs/diagrams/src/render_all.py
```
