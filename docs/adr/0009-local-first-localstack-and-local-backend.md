# ADR-0009: Local-first development with LocalStack and the Terraform local backend

- Status: Accepted
- Date: 2026-11-25
- Deciders: Platform team
- Related: [ADR-0004](0004-s3-native-locking-plus-platform-lock.md), [ADR-0005](0005-github-oidc-for-aws-access.md), [02-aws-infrastructure.md](../02-aws-infrastructure.md), [08-mvp-roadmap.md](../08-mvp-roadmap.md)

## Context

The target design (docs 02, 05, ADR-0004, ADR-0005) runs every plan and apply against real AWS accounts. It uses GitHub OIDC roles and an S3 state bucket encrypted with KMS. Building the MVP against real accounts this early has costs:

- Every iteration of a module or the pipeline costs money and needs a bootstrapped multi-account setup.
- Contributors can't run the full loop on a laptop, and CI can't run it without cloud credentials.
- Most of the platform's value (registry, generator, orchestration, policies) can be built and proven without real AWS.

## Decision

Until the platform reaches the MVP exit criteria in `docs/08-mvp-roadmap.md`, all AWS use goes through **LocalStack**:

1. **AWS emulator:** LocalStack runs via `docker compose` locally and as a service container in CI. Terraform targets it through provider `endpoints` overrides, `tflocal` or the `localstack` example provider config. Module tests (`terraform test`, Terratest) run against it.
2. **State:** stacks and examples use the **Terraform `local` backend**. Paths follow the production key layout: `environments/<env>/<team>/<stack>/terraform.tfstate`. State files are git-ignored and never committed.
3. **Credentials:** only the static dummy credentials LocalStack accepts (`test`/`test`) are used. No real AWS credentials exist anywhere in the repo, CI or backend, so hard rule 1 in `AGENTS.md` still holds.
4. **Code shape stays production-ready:** the generator keeps the backend block behind a small abstraction (`local` now, `s3` with `use_lockfile` later). Modules take no LocalStack-specific inputs. Only provider configuration in examples, tests and generated roots differs.

## Consequences

Positive:
- Zero cloud cost during the MVP, and the full request → PR → plan → apply loop works offline.
- CI is hermetic, with no secrets needed to run tests on PRs.
- Moving to real AWS later is a configuration change in the generator and workflows, not a redesign.

Negative:
- LocalStack has gaps: some IAM enforcement, KMS key policies and service limits are not faithful. Policy and Checkov checks stay the main guard, and a real sandbox account is needed before production.
- A local backend has no shared locking. The platform-level stack lock (ADR-0004 part 2) is the only concurrency guard until the S3 backend comes back.
- The OIDC roles and S3 state bucket in ADR-0004 and ADR-0005 are deferred, not dropped.

## Alternatives considered

- **Real AWS sandbox account from day one:** most realistic, but needs cost, credentials and bootstrap work before any value is shown.
- **Moto server:** lighter than LocalStack, but has weaker Terraform provider coverage and community tooling (`tflocal`, `awslocal`).
- **S3 backend on LocalStack:** possible, but adds moving parts without proving anything new for the MVP. It can be added later to rehearse the migration.
