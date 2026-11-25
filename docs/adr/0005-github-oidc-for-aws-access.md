# ADR-0005: GitHub OIDC instead of static AWS keys

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team, Security
- Related: [07-security-and-policy.md](../07-security-and-policy.md), [05-automation-pipeline.md](../05-automation-pipeline.md)

## Context

Workflows need AWS credentials to plan and apply. Static IAM user keys stored as GitHub secrets are long-lived, hard to rotate, easy to exfiltrate from a compromised workflow, and cannot be scoped to a branch or environment.

## Decision

- Each AWS account has an IAM OIDC identity provider for `token.actions.githubusercontent.com`.
- Workflows call `sts:AssumeRoleWithWebIdentity` via `aws-actions/configure-aws-credentials`.
- Two roles per workload account:
  - `iacp-plan`: read-only + state read; trust `sub` = `repo:<org>/iacp:pull_request` and `repo:<org>/iacp:ref:refs/heads/main` (drift).
  - `iacp-apply`: scoped write with permission boundary `iacp-boundary`; trust `sub` = `repo:<org>/iacp:environment:<env>` only, so it is usable solely by jobs bound to the protected GitHub Environment.
- Trust policies also pin `aud = sts.amazonaws.com` and, where supported, `job_workflow_ref` to the approved workflow files.
- The same OIDC tokens (audience `iacp-api`) authenticate workflow callbacks to the Platform API.
- No static AWS keys exist anywhere in GitHub or the backend.

## Consequences

Positive:
- Credentials last ≤ 1 hour and are bound to repository, branch/environment and workflow.
- Production apply is impossible without passing the Environment reviewer gate.
- CloudTrail session names (`apply-<run_id>-<stack_id>`) tie AWS actions to pipeline runs.

Negative:
- Trust policies must be maintained carefully; a too-broad `sub` wildcard is a critical misconfiguration (covered by Checkov custom check and Access Analyzer).
- Requires GitHub OIDC availability; outage blocks pipelines (acceptable: everything is Git-driven anyway).

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| IAM user access keys in GitHub secrets | Long-lived, broad, rotation burden, high blast radius |
| Self-hosted runners with instance profiles | Credentials tied to hosts rather than workflows; still worth considering for scale, combined with OIDC |
| Vault AWS secrets engine | Additional critical service to operate; OIDC provides equivalent short-lived credentials natively |
