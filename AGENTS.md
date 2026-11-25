# AGENTS.md — Working in this repository

Guidelines for anyone changing this repo, whether a contributor or an automated coding agent. Read [OVERVIEW.md](OVERVIEW.md) first, then the relevant doc in [docs/](docs/README.md).

## Project in one line
The Self-Service IaC Platform is a self-service IaC platform. Developers request versioned Terraform modules through a portal, the platform opens a PR, GitHub Actions validates, scans, plans, and applies the change, and the platform manages its lifecycle.

## Repository layout
```
modules/        Terraform packages (s3, sqs, lambda, …) — each has module.yaml, README, examples/, tests/
policies/       opa/ (Rego on plan JSON, run by conftest) and checkov/ (custom checks)
environments/   GENERATED stack roots: <env>/<team>/<stack>/ — do not hand-edit
tests/          Terratest (Go) integration tests
backend/        Go API + worker + internal/generator
portal/         Next.js developer portal
.github/        workflows: pr-validate, apply, drift, ttl-reaper, module-release
docs/           design docs, ADRs, diagrams (docs/diagrams/src → images/)
```

## Source of truth
| Topic | Document |
|-------|----------|
| Architecture and flows | `docs/01-system-architecture.md` |
| AWS accounts, IAM, state | `docs/02-aws-infrastructure.md` |
| DB schema and REST API | `docs/03-data-model-and-api.md` |
| Module contract and versioning | `docs/04-module-registry.md` |
| CI/CD workflows | `docs/05-automation-pipeline.md` |
| Lifecycle (drift, TTL, locking, recovery) | `docs/06-lifecycle-management.md` |
| Security and policies | `docs/07-security-and-policy.md` |
| Scope and milestones | `docs/08-mvp-roadmap.md`, `WBS.md` |

If code and docs disagree, fix one of them in the same change. A significant design change needs a new ADR in `docs/adr/`.

## Hard rules
1. **No static cloud credentials.** AWS access is GitHub OIDC only (`iacp-plan` / `iacp-apply`). Never commit keys, `.env` files, state, or plan files.
2. **Terraform never runs in the backend.** The backend orchestrates; plans and applies run only in GitHub Actions.
3. **Never `terraform apply` against real accounts from a laptop.** Use LocalStack (`tflocal`) locally.
4. **Pin everything.** Use exact module refs (`?ref=modules/<name>/vX.Y.Z`), never `main`. Pin providers in `versions.tf` and commit the lock file. Pin GitHub Actions by commit SHA.
5. **Modules keep secure defaults:** encryption with KMS, public access blocked, least-privilege IAM, required tags (`iacp:team`, `iacp:environment`, `iacp:stack`, `iacp:managed-by`).
6. **Breaking module change means a major version bump** plus upgrade notes. Use `moved` blocks where possible.
7. **`environments/` is generated** by `backend/internal/generator`. To change output, change the templates and update the golden files.
8. **Audit log is append-only.** Never add UPDATE or DELETE paths for `audit_events`.
9. **State surgery** goes only through the break-glass `state-surgery` workflow.

## Commands
```bash
# Local stack
docker compose up -d                     # Postgres, LocalStack, backend, portal

# Terraform modules
terraform fmt -recursive -check
terraform -chdir=modules/s3 init -backend=false && terraform -chdir=modules/s3 validate
tflint --recursive
checkov -d modules/
conftest test plan.json -p policies/opa
opa test policies/opa -v

# Go
cd backend && go test ./... && go vet ./...
cd tests && go test -timeout 30m ./...   # Terratest (LocalStack)

# Portal
cd portal && npm ci && npm run lint && npm test

# Diagrams
.venv/bin/python docs/diagrams/src/render_all.py   # needs graphviz
```

## Coding conventions
- **Go:** standard layout (`cmd/`, `internal/`), chi router, pgx and sqlc, context everywhere, structured JSON logs, table-driven tests, wrapped errors (`fmt.Errorf("…: %w", err)`).
- **Terraform:** one resource concern per file. Every variable has a `description`, a `type`, and `validation` where useful. Outputs are documented. Run `terraform-docs` for READMEs.
- **Rego:** each policy has positive and negative tests and a clear `deny` message naming the resource and the fix.
- **Python:** only for tooling and diagrams. Keep scripts self-contained under `docs/diagrams/src/`.

## Git and commits
- Author for this repo is `bibekgupta3333 <bibekgupta3333@gmail.com>` (repo-local git config).
- Use Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`, `chore:`.
- Do not add co-author trailers or tool and assistant attribution lines.
- Update `CHANGELOG.md` under `[Unreleased]` and tick tasks in `WBS.md` when work lands.
- Branch from `main`. Every change goes through a PR with green checks.

## Definition of done
- [ ] Tests added or updated and passing (unit, golden files, policy tests, Terratest where relevant)
- [ ] `fmt`, lint, Checkov and OPA are clean
- [ ] Docs, ADRs and diagrams updated if behaviour or architecture changed
- [ ] `CHANGELOG.md` and `WBS.md` updated
