# ADR-0002: Terraform executes in GitHub Actions, not in the backend

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team
- Related: [ADR-0001](0001-pr-based-gitops-provisioning.md), [ADR-0005](0005-github-oidc-for-aws-access.md), [05-automation-pipeline.md](../05-automation-pipeline.md)

## Context

Something must run `terraform plan` and `terraform apply`. Running it inside the Go backend/worker would give tight control over execution, but it would also make the backend a holder of powerful AWS credentials across every workload account, require sandboxing of untrusted provider plugins and module code, and duplicate the logs/artifacts/approval features of CI.

## Decision

Terraform **never runs in the backend**. All plans and applies run in GitHub Actions workflows (`pr-validate.yml`, `apply.yml`, `drift.yml`). The backend orchestrates through the GitHub API (commits, PRs, `workflow_dispatch`) and observes through webhooks plus callbacks that workflows make to the API with their GitHub OIDC token.

## Consequences

Positive:
- The backend has no AWS write access to workload accounts; compromise of the API does not directly yield infrastructure control.
- Ephemeral, isolated runners per job; provider binaries never execute in the platform's process.
- Logs, artifacts, Environment approvals and concurrency groups come for free.
- Engineers can reason about and debug runs in a familiar UI.

Negative:
- Status is eventually consistent; the backend must reconcile from webhooks and callbacks and tolerate missing events (hourly reconciler).
- Actions limits (concurrency, minutes, 256-job matrix) bound throughput; mitigated by changed-stack matrices and optional self-hosted runners.
- Cross-stack orchestration (DAG ordering) must be done by the backend dispatching separate runs rather than within one process.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Worker runs Terraform in ECS tasks | Backend gains broad AWS credentials; must build log streaming, artifact storage, sandboxing |
| Atlantis | Mature PR automation, but a second always-on server holding credentials; less control over plan-hash verification and API callbacks |
| AWS CodeBuild triggered by backend | Credentials stay in AWS, but splits the review experience away from GitHub and adds glue |
