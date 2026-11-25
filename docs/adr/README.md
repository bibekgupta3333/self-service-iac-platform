# Architecture Decision Records

> Purpose: index of significant, hard-to-reverse decisions for Foundry, in MADR-lite format (Context / Decision / Consequences / Alternatives).

| ADR | Title | Status |
|---|---|---|
| [0001](0001-pr-based-gitops-provisioning.md) | PR-based GitOps provisioning | Accepted |
| [0002](0002-terraform-executes-in-github-actions.md) | Terraform executes in GitHub Actions, not in the backend | Accepted |
| [0003](0003-monorepo-git-tag-module-versioning.md) | Monorepo with git-tag module versioning | Accepted |
| [0004](0004-s3-native-locking-plus-platform-lock.md) | S3 native state locking plus a platform-level stack lock | Accepted |
| [0005](0005-github-oidc-for-aws-access.md) | GitHub OIDC instead of static AWS keys | Accepted |
| [0006](0006-opa-conftest-and-checkov.md) | OPA/conftest on plan JSON plus Checkov | Accepted |
| [0007](0007-go-backend-postgresql.md) | Go backend with PostgreSQL | Accepted |
| [0008](0008-github-app-for-automation.md) | GitHub App for automation | Accepted |

## Process

- New ADRs are proposed by PR as `NNNN-short-title.md` with status `Proposed`; they become `Accepted` on merge after review by `@acme/platform-team` (and `@acme/security` for security-relevant decisions).
- ADRs are immutable once accepted. A change of direction is a new ADR that marks the old one `Superseded by ADR-NNNN`.

## Template

```markdown
# ADR-NNNN: Title

- Status: Proposed | Accepted | Superseded by ADR-NNNN
- Date: YYYY-MM-DD
- Deciders: ...
- Related: ...

## Context
## Decision
## Consequences
## Alternatives considered
```
