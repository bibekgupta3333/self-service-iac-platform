# Foundry — Self-Service Infrastructure-as-Code Platform

A self-service cloud platform where developers request standardized AWS environments through reviewed pull requests, backed by reusable, versioned Terraform modules, automated plans, tests, security and policy checks, and deployment records you can audit.

**Core idea:** a package manager for cloud infrastructure combined with a secure deployment and lifecycle management platform.

**Technologies:** Terraform, AWS, GitHub Actions, TFLint, Checkov, OPA, IAM, KMS, S3, CloudWatch, Go, Python, PostgreSQL, Next.js

## Highlights
- **Versioned module registry**: S3, SQS, Lambda (MVP), then ECS/Fargate, RDS Postgres, Redis, VPC, Monitoring, each with secure defaults and standard inputs and outputs.
- **PR-based provisioning**: the portal turns a request into generated Terraform and opens a pull request. Merging the PR applies it.
- **Policy as code**: TFLint, Checkov, and OPA policies run against the plan JSON. Infracost checks the cost against team budgets.
- **Least privilege**: GitHub OIDC with separate plan and apply roles and a permission boundary. No static keys.
- **Lifecycle**: upgrades, drift detection, TTL teardown of ephemeral environments, recovery and rollback, dependency ordering, RBAC, and an audit log.

## Repository layout (target)
```
modules/        reusable Terraform packages (versioned via git tags)
policies/       OPA (Rego) + Checkov custom checks
environments/   generated stack roots per env/team/stack
tests/          Terratest integration tests
backend/        Go API + worker + config generator
portal/         Next.js developer portal
.github/        CI/CD workflows
docs/           design documentation and diagrams
```

## Documentation
Start at [docs/README.md](docs/README.md).

## Regenerating diagrams
```bash
brew install graphviz
uv venv .venv && uv pip install --python .venv/bin/python -r docs/diagrams/requirements.txt
.venv/bin/python docs/diagrams/src/render_all.py
```
