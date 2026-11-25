# ADR-0001: PR-based GitOps provisioning

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team
- Related: [ADR-0002](0002-terraform-executes-in-github-actions.md), [ADR-0008](0008-github-app-for-automation.md), [05-automation-pipeline.md](../05-automation-pipeline.md)

## Context

Developers need self-service infrastructure without learning Terraform, while the organisation needs every change reviewed, policy-checked, reproducible and auditable. Options range from a portal that calls cloud APIs directly, to a portal that runs Terraform itself, to a portal that writes Terraform into Git and lets a pipeline apply it.

Requirements that drive the decision:

- A human-readable, diffable record of the desired state of every stack.
- Reviews and approvals using tools engineers already trust (GitHub PRs, CODEOWNERS).
- Rollback as a first-class, low-risk operation.
- The ability for platform engineers to operate even if the portal is down.

## Decision

Every change to infrastructure is expressed as a **pull request** against the `foundry` monorepo. The platform generates Terraform root modules under `environments/<env>/<team>/<stack>/`, opens a PR via the GitHub App, and the pipeline validates and plans on the PR. Merge to `main` is the approval; `main` is the desired state; `apply.yml` reconciles AWS to `main`.

The portal and API are a request/orchestration layer over Git, not an alternative path to AWS.

## Consequences

Positive:
- Full history and blame for every stack; rollback is a revert PR.
- Reuses GitHub's review, branch protection, CODEOWNERS and Environment approvals.
- Plans, policy results and cost appear where reviewers already look.
- Disaster recovery of desired state is `git clone`.

Negative:
- Latency: PR creation, checks and merge add minutes compared with direct API calls.
- Generated code in Git can be hand-edited; mitigated by `DO NOT EDIT` headers, CODEOWNERS and a regenerate-and-diff check.
- Repository grows with one directory per stack; acceptable for thousands of stacks, revisit with sharding (per-environment repos) beyond that.
- GitHub becomes a hard dependency for provisioning.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Portal calls AWS APIs / CloudFormation directly | No reviewable desired state, weak drift story, bespoke approval system |
| Portal triggers Terraform with inputs only (no Git) | State of intent lives only in DB; rollback and audit depend entirely on the platform |
| Terraform Cloud / Spacelift workspaces | Viable, but adds licence cost and a second control plane; less suitable as a self-built platform; Git-first design keeps a migration path open |
| Crossplane / Kubernetes operators | Requires a Kubernetes control plane and CRD-based modules; mismatch with existing Terraform skills |
