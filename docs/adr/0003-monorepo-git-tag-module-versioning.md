# ADR-0003: Monorepo with git-tag module versioning

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team
- Related: [04-module-registry.md](../04-module-registry.md)

## Context

Modules must be versioned so stacks can pin exact versions and upgrade deliberately. We also need the generator, policies, workflows and modules to evolve together, and we want a "registry" experience (catalog, versions, changelog, deprecation) without operating a separate artifact store.

## Decision

- Keep modules, policies, generated environments, tests, backend and portal in **one repository** (`iacp`).
- Version each module independently with **SemVer git tags** `modules/<name>/vX.Y.Z`, created only by `module-release.yml` and protected by a tag ruleset.
- Consumers reference `git::https://github.com/<org>/self-service-iac-platform.git//modules/<name>?ref=modules/<name>/vX.Y.Z`.
- Each module ships a `module.yaml` manifest; on release it is indexed into the Postgres catalog, which is a derived view (Git remains the source of truth).

## Consequences

Positive:
- Atomic changes across module + policy + tests in one PR; one CI configuration.
- No registry server to operate; tags are immutable snapshots and integrity is checked against the recorded commit SHA.
- Independent module versions despite a single repository.

Negative:
- `terraform init` clones the repository per module source (shallow by tag); acceptable at current size, cacheable later.
- Tag namespace discipline is required; enforced by rulesets and CI.
- Fine-grained repository permissions are coarser than with per-module repos; mitigated with CODEOWNERS.
- Version constraints (`~>`) are not available for git sources; exact pins are intentional here.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| One repository per module | Cross-cutting changes need N PRs; harder to keep contract and policies in sync |
| Private Terraform registry (TFC/TFE, or self-hosted protocol implementation) | Adds a service or licence; still needs our catalog for schemas, deprecation and cost hints; possible later as a façade over the same tags |
| Repo-wide version (single tag for all modules) | Forces unrelated upgrades; noisy changelogs |
| S3-hosted module archives | Loses Git provenance; needs separate signing/integrity scheme |
