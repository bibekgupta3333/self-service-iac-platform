# ADR-0008: GitHub App for automation

- Status: Accepted
- Date: 2026-11-24
- Deciders: Platform team, Security
- Related: [ADR-0001](0001-pr-based-gitops-provisioning.md), [ADR-0002](0002-terraform-executes-in-github-actions.md)

## Context

The backend must create branches and commits, open and label PRs, dispatch workflows, read check results and receive webhooks. Options are a personal access token (PAT) for a machine user, OAuth on behalf of the requesting user, or a GitHub App.

## Decision

Use a dedicated **GitHub App** ("The platform") installed on the `iacp` repository only:

- Permissions: Contents (read/write), Pull requests (read/write), Actions (read/write, for `workflow_dispatch`), Checks (read), Metadata (read), Members (read, for team sync).
- Subscribed events: `pull_request`, `pull_request_review`, `workflow_run`, `check_run`, `push`, `membership`, `team`.
- The backend authenticates with the App private key (in AWS Secrets Manager, KMS-encrypted) to mint 1-hour installation tokens.
- Webhooks are verified with HMAC (`X-Hub-Signature-256`) and deduplicated by `X-GitHub-Delivery`.
- Commits made via the Git Data API are signed by GitHub and attributed to the App, with a `the platform-Request:` trailer and co-author line for the requesting user.
- User identity for the portal uses GitHub OAuth separately; user tokens are never used to write to the repository.

## Consequences

Positive:
- Fine-grained, repository-scoped permissions and short-lived tokens; no dependency on a human account.
- Higher API rate limits that scale with installation.
- Clear audit trail: every automated action is attributed to the App plus the requesting user in metadata.

Negative:
- GitHub Apps cannot be CODEOWNERS or approve as code owners; development auto-approval uses a separate tightly scoped machine user (see [05-automation-pipeline.md](../05-automation-pipeline.md#52-codeowners)).
- Private key management and rotation become platform responsibilities.

## Alternatives considered

| Alternative | Why rejected |
|---|---|
| Machine-user PAT | Long-lived, broad scope, tied to a seat, lower rate limits |
| Requesting user's OAuth token | Requires broad user scopes; actions attributed inconsistently; breaks when users leave |
| `GITHUB_TOKEN` only | Exists only inside workflows; cannot be used by the backend and cannot trigger further workflows |
