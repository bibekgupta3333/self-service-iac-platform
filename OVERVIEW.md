# Foundry — Project Overview

**A package manager for cloud infrastructure, plus a secure deployment and lifecycle platform.**

## The problem
Teams request AWS infrastructure through tickets or hand-written Terraform. The result is slow provisioning, inconsistent environments, security drift, and no clear record of who changed what.

## The solution
Developers choose a versioned infrastructure package (S3, SQS, Lambda, …) in a portal and fill in typed inputs. Foundry turns that into a reviewed pull request. Automated checks run on the PR. After merge, the change is applied and tracked for its whole life.

```
Portal ──► Go API ──► generates Terraform ──► GitHub PR
                                                 │
        validate → TFLint → Checkov → plan → OPA → Infracost
                                                 │
                         review + merge ──► apply (OIDC, no static keys)
                                                 │
                 track: status · outputs · drift · TTL · cost · audit
```

## Main parts
| Part | What it does |
|------|--------------|
| **Module registry** | Reusable Terraform modules with secure defaults, a `module.yaml` manifest, and SemVer git tags |
| **Orchestration engine** (Go) | Requests, config generation, GitHub App PRs, webhooks, locking, audit log |
| **Automation pipeline** (GitHub Actions) | Validate, lint, security scan, policy check, cost, plan, apply, integration tests |
| **Security** | GitHub OIDC with plan and apply roles, a permission boundary, KMS-encrypted state, OPA and Checkov policies, RBAC |
| **Lifecycle** | Upgrades, drift detection, TTL teardown, recovery and rollback, dependencies, budgets |
| **Developer portal** (Next.js) | Browse packages, request resources, see cost and status, upgrade or destroy |

## Tech stack
Terraform · AWS (S3, KMS, IAM, ECS Fargate, RDS, SQS, EventBridge, CloudWatch) · Go · PostgreSQL · Next.js · GitHub Actions · TFLint · Checkov · OPA · Infracost · Terratest · LocalStack · Python (diagrams)

## MVP (about 10 weeks)
1. S3, SQS and Lambda packages
2. Versioned registry
3. Go API
4. PR-based provisioning
5. Security and policy gates
6. State management
7. Simple dashboard
8. Create and destroy

## Where to look
- [docs/README.md](docs/README.md): full design docs and diagrams
- [WBS.md](WBS.md): work breakdown and task status
- [CHANGELOG.md](CHANGELOG.md): release history
- [AGENTS.md](AGENTS.md): rules for anyone (human or automation) working in this repo
