# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project uses [Semantic Versioning](https://semver.org/).
Infrastructure modules are versioned separately with tags `modules/<name>/vX.Y.Z`. Their changes go in each module's own `CHANGELOG.md`.

## [Unreleased]

### Added
- Platform observability (WBS 7.7, partial):
  - Stack `environments/development/platform/observability/`, rendered by the generator from `modules/monitoring` pinned to `modules/monitoring/v1.0.0` (`4537d67`): a CloudWatch dashboard (request lead time, failure rate, requests finished, apply duration, lock queue depth, stacks by status, queue backlog, API from `/metrics`), a KMS-encrypted SNS topic, and six alarms on `iacp-events`, `iacp-events-dlq` and `iacp-github-outbound`. It passes the pr-validate gate, the apply/destroy round trip and the post-apply smoke test on LocalStack.
  - `docs/09-observability.md`: what each panel and alarm means, thresholds and why, what to do when each fires, the Prometheus to CloudWatch naming rule, and the gaps (no exporter; only HTTP and DB pool metrics exist in the backend; `monitoring` v1.0.0 cannot alarm on custom metrics, so the failure-rate and stuck-lock alarms are specified but not deployed).
  - docs/02 §8, docs/08 and WBS 7.7 aligned (metric namespace `IACP/Platform`, the added queue alarms).
- Portal lifecycle: live request status, stacks, update and destroy (WBS 7.4, 7.5, 7.6):
  - the request page follows `GET /v1/requests/{id}/events` through the existing proxy path with `StatusStream`, a fetch-based reader (so a 401, 404 or 429 `too_many_streams` is seen as such, which `EventSource` hides): `Last-Event-ID` resume (the first connection sends `0` to replay the whole history), exponential backoff with equal jitter, a stop on `end`, cleanup on unmount, and a polling fallback when the stream is not an event stream or keeps failing. Pure SSE parser and events-to-timeline reducer, unit tested; the history list, pull request link and a polite live region come from them. The 5-second `AutoRefresh` is removed.
  - `/stacks` (filters team, environment and status in a plain GET form; keyset paging with the API cursor) and `/stacks/{id}` (status, health wording, outputs exactly as returned, latest request with link, details), a Stacks nav entry and a home card.
  - update: `/stacks/{id}/update` reuses the schema-driven `RequestForm`, prefilled from the spec of the request that carries the stack's current spec (the API has no spec on the stack itself); without one the form says it starts from module defaults. An update that drops resources (`resources_removed`) needs an explicit checkbox acknowledgement, which sends `allow_removals`.
  - destroy: a modal `<dialog>` that warns that merging the destroy PR removes the infrastructure, requires the exact stack name and a justification, and sends a payload-bound `Idempotency-Key`. `stack_busy`, `destroy_pending`, `stack_destroyed`, 404 and 403 get plain explanations.
  - actions are shown disabled, with the reason, from the caller's roles in `GET /v1/me` (a pure mirror of the RBAC matrix) and the stack's state; dev auth assumes the user may act and lets the API answer.
- GitHub OAuth login, sessions and RBAC (WBS 4.4, core of 8.6):
  - `IACP_AUTH_MODE=github`: authorization-code flow with `state` and S256 PKCE (`/v1/auth/login`, `/v1/auth/callback`), a `GitHubIdentity` port with an `httptest`-tested adapter (fixed hosts, no cross-host redirects, timeouts, size caps), org-membership gate, fail-closed config
  - server-side sessions (`iacp_session`, hashed in the new `sessions` table, absolute and sliding idle expiry, logout and disablement revoke, fresh id on every login), per-session `X-CSRF-Token` on unsafe methods, `GET /v1/me` (also in dev mode), `POST /v1/auth/logout`
  - team sync at login (`iacp-<team>-viewers` / `<team>` / `<team>-maintainers` / admin team), manual memberships preserved, the last platform admin never downgraded
  - pure `rbac` permission matrix with exhaustive tests, route policies, `rbac.denied`, `auth.login`, `auth.login_denied`, `auth.logout` and `rbac.*` audit events; request creation now uses the matrix
  - review fixes: request list and get are scoped to the caller's teams (404 outside them) with a shared `identity.Visibility` helper; routes without an access policy are refused and a `chi.Walk` test enforces coverage; sessions migration is `0009`; platform teams with colliding GitHub role slugs get no synced roles; login starts and `rbac.denied` audit volume are rate limited; a login whose audit fails revokes its session; logout works with a dead session; `__Host-` cookie names over https
  - portal `GithubAuthProvider`, sign-in and sign-out controls, server-side cookie forwarding, a controlled `Set-Cookie` pass-through on the login routes only, and a stricter proxy CSRF check for requests that carry a cookie
- Stacks and request kinds (WBS 4.5, prerequisites of 5.3 and 7.4):
  - `GET /v1/internal/requests/by-path?stack_path=&head_sha=` resolves the request holding a stack's lock (ids, fencing token, approved plan hash) for `apply.yml`; it fails closed (no lock, expired lease, holder not approved, merge commit mismatch). New route class `request_lookup`: `apply.yml` on main via `push` or `workflow_dispatch` only, never `pr-validate.yml`.
  - `tools.stack_pipeline lock acquire --stack-path [--head-sha]` resolves the UUIDs and the approved plan hash through it; `apply.yml` uses them for the claim, the release and the plan-hash check, which removes the "fails closed because UUIDs are unknown" gap and the `IACP_API_URL` prerequisite.
  - `GET /v1/stacks` (filters `team`, `env`, `status`; keyset pagination) and `GET /v1/stacks/{id}` (status, outputs, latest request and link), scoped in SQL to the caller's teams (platform admins see all; a hidden stack is `404`).
  - `update` and `destroy` requests for existing stacks, named by `stack_id`. Kind-aware pure validation, `CheckStackAllowsKind` (`stack_busy`, `stack_destroyed`, `destroy_pending`), a destroy needs its stack name and a justification; the same idempotency, audit chain, stack lock and queue as a create. A destroy renders an empty root marked `"lifecycle": "destroy"` (golden-file tested) and the PR body warns. `upgrade` stays `unsupported_kind`.
  - `GET /v1/requests/{id}/events`: Server-Sent Events from the audit log (snapshot, status changes with the audit seq as event id, `Last-Event-ID` resume, heartbeat, `end` also on a resume at the terminal seq, bounded lifetime, per-poll authorization, 5 streams per user and 500 in total, ended on server shutdown).
  - Integration with GitHub login and RBAC: route policies for the stacks and event routes (any-team read); create, update and destroy follow `rbac.Decide` through one function (an unknown environment counts as production); stacks and the event stream use `identity.Visibility` like `/v1/requests`, so the dev-mode admin sees the same teams everywhere; an open stream ends when its session is revoked, expired or disabled; the portal proxy streams `v1/requests/{uuid}/events` (GET only, unbuffered, `Last-Event-ID` forwarded, upstream aborted when the browser leaves).
  - Review fixes: `verify-modules` accepts a root with no module calls (a destroy root writes no `modules.json`) when nothing is pinned; by-path requires a recorded merge commit (`merge_not_recorded`, retried by the tools client) and `lock acquire --stack-path` always resolves, even with ids; a stack in another team is a plain `404` for update and destroy; the destroy audit payload carries the justification and confirmed name; an update that drops resources needs `allow_removals`.
- PR publishing and GitHub webhooks (WBS 4.8, 4.9):
  - `publish.Publisher` port with pure branch naming (`iacp/<short id>`), path checks and a deterministic PR title, body (plan placeholder, request link), labels and commit message.
  - `githubapp` adapter: App JWT → cached, repository-scoped installation token; atomic commits with the git data API; idempotent PR, labels, `workflow_dispatch`; timeouts, same-host redirects only, retries on 5xx/429 honouring `Retry-After`.
  - `localgit` adapter (ADR-0009): commits to a local clone with git plumbing via `os/exec` argument arrays and records fake PRs, so the flow runs without a registered App.
  - `IACP_PUBLISHER=none|local|github` (default `none`), App settings, `IACP_GITHUB_WEBHOOK_SECRET`, `IACP_PORTAL_URL`, `IACP_LOCALSTACK_ENDPOINT`, `IACP_WORKER_POLL_INTERVAL`; half configuration fails at startup.
  - Migration 0008: `webhook_deliveries` and `outbox` with least-privilege `iacp_app` grants.
  - `internal/outbox`: Postgres job queue and in-process worker (SKIP LOCKED, lease, backoff, dead jobs).
  - Request creation enqueues `request.publish`; `internal/prflow` renders, commits, opens and labels the PR, then records `pr_opened` in a second transaction (fencing-token checked). Failures are audited per attempt and the last one fails the request.
  - State machine: `pr_merged` (approved → applying) and `publish_failed` (draft → failed). Terminal transitions release the stack lock in the same transaction and publish the promoted request.
  - `POST /v1/webhooks/github`: HMAC-SHA256 verification (fails closed), 2 MiB cap, `X-GitHub-Delivery` dedupe, pure event mapping, idempotent worker-side transitions with audit; request responses carry `github {branch, pr_number, pr_url}`.
  - Review fixes: publishing and recording check the current lock row, not the request's stored token; every promotion (release, `DELETE …/lock`, expired-lease handoff) schedules the promoted request's publish job through one lock-service hook; outbox outcome writes are guarded by the claim's attempt number and handlers run with a deadline inside the lease; commits replace the stack directory as a whole; a final failure withdraws the unrecorded branch and PR; webhook commands carry the PR head SHA (`requests.github_head_sha`): only pushes are head-checked, and a stale push resyncs the head from GitHub (`PullRequestHead`) instead of being dropped, so closes and merges after a lost push still release the lock; events racing the PR record are retried for about 13.5 minutes; root replacement keeps `.terraform.lock.hcl` and lists only the stack subtree on GitHub; withdrawal re-reads the request and skips a recorded PR; the repository filter ignores case; the GitHub token is narrowed to contents/pull_requests/actions write, dispatch is never retried and only an existing ref is force-moved; `localgit` requires a work tree root.
- ADR-0009: local-first development with LocalStack and the Terraform local backend until MVP exit.
- Monorepo layout: `modules/`, `policies/`, `environments/`, `tests/`, `backend/`, `portal/`, `tools/`, `.github/`.
- `docker-compose.yml` with LocalStack 4.4 and PostgreSQL 17, both with health checks, plus `.env.example`.
- `Makefile` targets for the local stack, Terraform checks, OPA tests, module tests and Go/Python tests; `.editorconfig`; `.terraform-version` (1.13.5).

- Policy as code (WBS 6.1–6.3):
  - `.tflint.hcl` with the pinned terraform and aws rulesets.
  - OPA policies on plan JSON under `policies/opa`:
    - required `iacp:*` tags; S3/SQS encryption with customer-managed KMS keys and key rotation
    - no public S3 access, ACLs, policies or `NotPrincipal`; bucket controls linked through configuration references
    - IAM wildcard and sensitive-read deny, `NotResource` deny, AWS-managed admin policy attachments denied
    - allowed regions with no implicit region; exact `?ref=modules/<name>/vX.Y.Z` module pins
    - no `force_destroy` outside development; forbidden `external`/`http` data sources and provisioners
    - the environment comes only from `--data context.json`, and the forbidden and production public-policy rules cannot be waived
    - 168 tests at 99.9% coverage
  - Conftest plan fixtures with `context.json` and `verify.sh`, which asserts all 24 rule IDs.
  - Custom Checkov checks `IACP_AWS_001`–`003` (tags, S3 KMS CMK, Lambda tracing and concurrency) with a pytest suite.
- CI workflow `.github/workflows/ci.yml`:
  - Jobs: actionlint, Terraform fmt/validate, TFLint and Checkov; OPA and conftest; module tests against a LocalStack service container; Go tests with a Postgres service; Terratest; Python tests.
  - Every action is pinned by commit SHA, permissions are read-only, and each job skips when its code doesn't exist yet.
- `.github/pull_request_template.md` and `.github/CODEOWNERS`.

- Go control plane `backend/` (WBS 4.1–4.3, 4.5 partial, 4.7, 4.11):
  - chi API with slog JSON logs, server timeouts and graceful shutdown
  - goose migrations with an advisory lock
  - catalog endpoints, and an internal index endpoint behind a token
  - `POST/GET /v1/requests` with a pure state machine (apply retries capped at 2), Idempotency-Key replay (24 h window) and JSON Schema input validation
  - deterministic config generator (golden-tested, `terraform fmt` and TFLint clean) with local and S3 backend strategies
  - hash-chained audit log computed in Go (ADR-0010), with a bounded `/v1/audit/verify`
  - distroless nonroot Dockerfile and an opt-in `backend` compose service
- Make targets `go-integration`, `generator-update` and `api`.

- PR validation pipeline (WBS 2.6, 5.1, 5.2 partial, 5.4):
  - `tools/stack_pipeline`, tested with pytest:
    - `detect` maps changed files to `environments/<env>/<team>/<stack>/` roots and emits a JSON matrix. It skips deleted roots and enforces the 20-stack and dependency-edge guard rails. A pipeline change re-validates every stack.
    - `context` builds the conftest data from `iacp.stack.json`.
    - `summary` renders the sticky PR comment and a JSON payload with a normalised plan hash.
    - `verify-pins` checks that published module tags resolve to the recorded commit. It serves unpublished tags from a local mirror, but only for commits already on `main`.
  - Smoke stack `environments/development/platform/smoke/`, rendered by the generator: `modules/s3` pinned to `modules/s3/v1.0.0`, LocalStack endpoints, local backend, `iacp:*` default tags, and a lock file for linux and darwin.
  - `.github/workflows/pr-validate.yml`:
    - Jobs: `detect`, then a per-stack matrix against LocalStack, then sticky comments, then a single `pr-validate-result` check.
    - Matrix steps: fmt, module pins, init, validate, TFLint, Checkov, plan, `show -json`, conftest, and a hermetic apply/re-plan/destroy round trip.
    - Only the comment job holds `pull-requests: write`.
    - Per-stack concurrency groups; provider and TFLint plugin caches. The API report is a `TODO(WBS 4.10)`.
  - Make targets `stack-detect`, `stack-plan`, `stack-apply`, `stack-destroy` and `stack-clean`.
  - Review hardening:
    - `detect` fails closed on any `environments/` path outside a valid stack root, and on a root with files but no `iacp.stack.json`.
    - Stack ids are joined with `__`, so they are injective.
    - New `verify-modules` step after init: the installed module calls must match the pins one to one, at the recorded commits.
    - Serving unpublished tags now requires `IACP_PIN_BOOTSTRAP` (`--allow-unpublished`).
    - The comment job writes the comment marker itself.

- Run reports and the apply pipeline (WBS 5.2 report, 5.3, 5.6):
  - `tools/stack_pipeline report` builds a `POST /v1/runs` body that mirrors `runs.Report` and `runs.Validate`, from the plan summary, `terraform output -json` (sensitive values redacted) and the runner environment. `head_sha` is always `GITHUB_SHA`.
  - `tools/stack_pipeline lock acquire|release` claims and releases the platform stack lock.
  - `api_client.py`: a GitHub OIDC token for the configured audience, falling back to `IACP_INTERNAL_TOKEN`. Tokens are masked, redirects are refused, and only idempotent calls are retried. Both commands skip with a notice while `IACP_API_URL` is empty.
  - `.github/actions/iacp-report`: a thin composite action over `report`. Every input passes through `env:`.
  - `pr-validate.yml`: a separate `report` job, the only one with `id-token: write`, reports each stack's summary as pipeline `validate`. It never affects `pr-validate-result`.
  - `.github/workflows/apply.yml`: on push to `main` (`environments/**`) or dispatch, per stack against LocalStack:
    - lock claim, verify-pins, init, verify-modules
    - plan (or `plan -destroy`), then a conftest pre-apply gate
    - plan report and the plan-hash check when an approved hash is given
    - apply, outputs, apply and summary reports, then lock release in an `always()` step
    - one apply per stack (`iacp-stack-<id>-apply`, never cancelled)
  - State is hermetic per job until the S3 backend arrives. That limit is documented in docs/05 and ADR-0009.
  - Review hardening:
    - The pr-validate `report` job is OIDC only, and `apply.yml` never receives `IACP_INTERNAL_TOKEN`. A job that can mint OIDC tokens no longer falls back to the shared token.
    - `apply.yml` runs only from `main`.
    - The plan-hash check fails closed once the API is configured, and both hashes are format-checked.
    - A lock claim is recorded before the call, so an ambiguous claim is still released (by request when no token came back). A failed release is reported as its own stage.
    - Destroy dispatches have their own concurrency group.
    - The dispatched `stack_path` and server text are kept out of workflow commands.
    - The workflow runs on every PR, and policy and tool-version changes re-validate every stack.
  - After the first module releases:
    - The smoke stack is pinned to the published `modules/s3/v1.0.0` (`681144a`).
    - `IACP_PIN_BOOTSTRAP` is off in the workflow and Makefile; `--allow-unpublished` stays as an opt-in.
    - Deleted-root detection counts only files tracked by git.
- Module registry MVP (WBS 3.1–3.5):
  - `modules/_shared/module.schema.json`, the JSON Schema for `module.yaml`.
  - `tools/manifest_validator`: schema, inputs-schema, variables.tf parity and `compatibility` against `required_version`/`required_providers` checks. Pytest suite; run it with `make validate-manifests`.
  - `modules/s3` v1.0.0:
    - KMS CMK, public access block, TLS-only and TLS ≥ 1.2 bucket policy, versioning
    - one lifecycle rule, optional access logging, `additional_policy_statements` and `key_service_principals`
  - `modules/sqs` v1.0.0: SSE-KMS queue with a DLQ and an inline redrive-allow policy, and `key_service_principals`.
  - `modules/lambda` v1.0.0:
    - execution role under `/iacp/` that always has the `iacp-boundary`
    - log group with retention, active tracing and reserved concurrency in production
    - tagged SQS event source with validated batching, KMS grants for queues, and guarded `extra_policy_jsons`
  - LocalStack examples with `policy-context.json` and `terraform test` suites (s3 14, sqs 11, lambda 23).
  - Terratest suite (`tests/`) for s3 and sqs that asserts the platform tag values. Run it with `make integration-test`.
- Module release (WBS 3.6, 3.7 partial):
  - `tools/module_release`, a SemVer interface-diff gate. It parses the module with `python-hcl2` (pinned 8.1.4) at the last `modules/<name>/vX.Y.Z` tag and at HEAD, classifies changes as major, minor or patch, and fails when `metadata.version` is bumped too little or not at all. Run it with `make module-gate`, which is also part of `make lint`.
    - It compares variables (type, default, `nullable`, validation conditions with string literals kept verbatim), outputs (name and `sensitive`; a non-literal `sensitive` is treated as sensitive), and the floors of `required_version` and `required_providers` (a raised minimum major version or a removed provider is major; providers are keyed by their implied `hashicorp/<name>` source when none is given).
    - It handles first releases and `0.x` modules, and fails when an already tagged version (prereleases included) changed, or when a prerelease is lower than an existing one.
  - `notes` builds release notes from the module's CHANGELOG section, or from a generated diff summary against the last final release below the declared version.
  - `index` posts the parsed `module.yaml` to `POST /v1/internal/catalog/index` in the backend's `IndexCommand` shape. It authenticates with `IACP_INTERNAL_TOKEN`, retries 5xx responses, refuses redirects, and accepts plain http only for loopback hosts. Setting only one of `IACP_API_URL` and `IACP_INTERNAL_TOKEN` is an error.
  - Pytest suite (209 tests, including temporary git repositories, a fake HTTP server and an opt-in `IACP_INTEGRATION=1` test against a running API).
  - `.github/workflows/module-release.yml`:
    - It runs on every PR, so the `semver gate result` check always reports. PRs that don't touch modules or the tooling get an empty matrix without installing Python.
    - On pushes to `main` (or a manual `workflow_dispatch` on `main`), it creates any missing tags and GitHub releases (also retrying tags that have no release), then calls the catalog index, which is skipped with a notice until `IACP_API_URL` and `IACP_INTERNAL_TOKEN` are configured.
    - Steps run with bash `pipefail`, and annotated tags are peeled to their commit before indexing.
    - Actions are pinned by SHA, and only the release job has `contents: write`.
    - The first merge to `main` creates `modules/{s3,sqs,lambda}/v1.0.0`.
- Per-stack platform lock (WBS 4.6), `backend/internal/locks`:
  - fencing tokens from `lock_fencing_seq`, a 7-day lease with heartbeat, takeover of expired leases, and a FIFO queue (`requests.queued_behind`) promoted on release
  - pure decisions (`Decide`, `NextInQueue`, `CheckToken`); the repository serialises each stack with `SELECT … FOR UPDATE` and a concurrency test proves one winner
  - `POST /v1/requests` acquires or queues in its own transaction; a queued request cannot open its PR
  - internal endpoints `GET/POST/DELETE /v1/internal/stacks/{id}/lock` and `POST …/lock/heartbeat`; `lock.acquired`, `lock.queued`, `lock.released` audit events
- GitHub Actions OIDC verifier (WBS 4.10), `backend/internal/auth`:
  - RS256 only, cached JWKS with refetch on an unknown `kid` (rate limited), `iss`/`aud`/`exp`/`nbf`/`iat` with leeway, and `repository`/`repository_owner`/`repository_id`/`ref`/`job_workflow_ref` allow-lists
  - configured with `IACP_OIDC_AUDIENCE` and `IACP_OIDC_REPOSITORY` (plus optional overrides); accepted on `/v1/internal/*` next to the shared token
- `POST /v1/runs` records run reports in the new `pipeline_runs` table (migration `0005`), checking the fencing token, the reporting run id and commit, and the workflow and trigger allowed for each pipeline. Replayed stage reports replace their entry instead of duplicating it, and reports lock the stack row before the lock row so they cannot deadlock with lock release.
- Backend hardening (WBS 4.1, 4.10, 6.2, 6.4, 6.6 partial):
  - `GET /metrics` in Prometheus format (`prometheus/client_golang` v1.24.1): request count and latency per method and chi route pattern (unmatched paths share one label), database pool gauges, Go and process collectors.
  - OIDC `jti` replay protection: `auth.ReplayGuard` claims each token id until `exp` plus leeway. The default store is the new `oidc_jti` table (migration `0006`), shared by every replica; `IACP_OIDC_REPLAY_STORE=memory` uses a bounded per-process cache instead.
  - `GET /v1/internal/budget-context?stack_path=` returns the conftest `--data` document: environment, allowed regions, the team's monthly budget from the new `budgets` table (migration `0007`), and the yanked module versions. It sits behind a new `budget_context` route class for `pr-validate.yml` and `apply.yml`.
  - OPA `iacp.modules.yanked` denies plans that create or update resources in a module pinned to a yanked version. `iacp.modules.yanked_pinned` only warns when the module is left alone or destroyed. 11 new tests; `verify.sh` also checks the rule against `context-yanked.json`.
  - `tools/codeowners` generates `.github/CODEOWNERS` from `.github/teams.yaml`. CI and `make lint` run `--check`, which fails on drift or on a stack whose team is not declared. The branch ruleset and Environment protection payloads are checked in under `.github/rulesets/`, for the owner to apply with `gh api` in WBS 2.5.
- Developer portal `portal/` (WBS 7.1–7.3, 7.5 partial):
  - Next.js 16 App Router with strict TypeScript, Tailwind 4, ESLint and Vitest + Testing Library; exact dependency pins and a committed `package-lock.json`; Node 24 from `portal/.nvmrc`
  - typed `ApiClient` (fetch, base URL and `AuthProvider` injected) with `ApiError` built from the backend's problem+json, including `errors[].pointer`
  - `AuthProvider` seam with `DevAuthProvider`, matching the backend's dev auth mode (no credentials, fixed login); GitHub OAuth plugs in later
  - same-origin proxy `/api/iacp/v1/...` for browser calls (the API sends no CORS headers):
    - forwards only an allow-list of user routes (`catalog`, `requests`; POST only on `/v1/requests`), matched exactly, with no dot segments or encoded slashes
    - allow-lists request and response headers and rewrites `Location` into the proxy prefix
    - answers HEAD without a body, rejects other methods (405) and bodies over 1 MiB (413)
    - rejects cross-site POSTs (403) using `Sec-Fetch-Site` or `Origin`
  - catalog list (category filters derived from the modules) and module detail with versions, outputs and an inputs table generated from the JSON Schema
  - Content-Security-Policy and other security headers on every response
  - request form generated from the version's input schema by a small typed renderer ([ADR-0011](docs/adr/0011-portal-form-renderer.md)):
    - the `Idempotency-Key` is bound to the payload: reused for unchanged retries, renewed when the values change
    - `idempotency_key_reuse` and ambiguous failures (network error, 5xx) are explained, with a link to the user's requests
    - backend validation errors are mapped onto fields by pointer, including missing required inputs
    - exclusive bounds are respected, and clearing an optional field sends null or an explicit empty value where the schema allows it
    - a preview shows the exact request
  - requests list (All/Mine, team filter, cursor paging) and request detail with a status timeline, refreshed every 5 s until terminal
  - CI job `portal` (npm ci, lint, typecheck, test, build) and make targets `portal-install`, `portal-dev`, `portal-test`
- More packages (WBS 8.1 partial, 9.3 partial), each `v1.0.0` with `module.yaml`, a LocalStack example with development and staging policy contexts, a `terraform test` suite, README and CHANGELOG:
  - `modules/vpc`: public and private subnets per zone keyed by zone name, no public IP on launch, a stripped default security group, optional NAT (off in development by default, per zone required in production), and flow logs to a CMK-encrypted log group through a bounded `/iacp/` role, and readable errors when the region lacks the requested zones (17 runs).
  - `modules/monitoring`: an SNS alarm topic on a CMK that CloudWatch may use, a TLS-only topic policy limited to CloudWatch, typed subscriptions (a scoped invoke permission for `lambda`; `sqs` is rejected because the queue policy belongs to the queue's stack), a dashboard from typed widgets with bounded markdown and an alarm status widget, and alarm helpers for SQS age and depth, Lambda errors and throttles, and DynamoDB throttles (21 runs).
  - `modules/dynamodb`: a table on a CMK with PITR always on, deletion protection outside development, on-demand billing by default, typed GSIs with derived attribute definitions, optional TTL and stream, and read/write policy documents, the read one covering the stream when enabled (16 runs).
  - Terratest `TestVPCComplete`, `TestMonitoringComplete` and `TestDynamoDBComplete` assert the platform tag values and the secure defaults through the AWS SDK. The CloudWatch client stays on v1.52.x, the last AWSQuery release, because LocalStack 4.4 cannot decode Smithy RPC v2 CBOR.
  - OPA `iacp.encryption.sns_sse`, `iacp.encryption.sns_kms` and `iacp.encryption.dynamodb_cmk` with 17 new tests (196 in total, 99.9% coverage); the conftest fixtures and `verify.sh` cover the three new rule IDs.
  - `policies/example-plan.sh` also checks each `policy-context.<env>.json` against a plan made with `-var environment=<env>`, so the staging contexts of these three examples run in `make example-plans` and CI.
  - LocalStack (`docker-compose.yml` and every workflow) also starts `ec2`, `dynamodb`, `dynamodbstreams` and `kinesis`. CI discovers modules by glob, so `module-tests`, `terratest` and the release gate pick the new modules up without workflow changes.

- Phase 2 modules `ecs-service`, `postgres` and `redis` v1.0.0 (WBS 8.1, partial). Each has a `module.yaml`, secure defaults, a plan-only `examples/complete` with `policy-context.json`, a README and a CHANGELOG:
  - `ecs-service`: Fargate service with a deployment circuit breaker and rollback, an ECR repository (scan on push, immutable tags, KMS, lifecycle policy), task and execution roles under `/iacp/` with the `iacp-boundary` permissions boundary, KMS-encrypted logs, ALB-only ingress, and an optional internal or public ALB with an HTTPS listener and an HTTP-to-HTTPS redirect
  - `postgres`: private RDS PostgreSQL with CMK storage, `rds.force_ssl=1`, an RDS-managed master password in Secrets Manager, backups, deletion protection and a final snapshot outside development, Multi-AZ in production and encrypted Performance Insights
  - `redis`: private ElastiCache replication group with CMK at-rest encryption, required TLS, an AUTH token in Secrets Manager or an RBAC user group, and automatic failover in production
  - Offline `terraform test` suites, because LocalStack community 4.4 has no ECS, ECR, ELBv2, RDS or ElastiCache: `plan` runs use the real provider with every endpoint on a closed local port, and `apply` runs use `mock_provider "aws"`
  - Final snapshots of `postgres` and `redis` are named `<name>-final-<suffix>` with a `random_id` suffix kept in state, so destroying a re-created instance does not collide with an earlier snapshot (`postgres` now also requires `hashicorp/random`)
  - `team` rejects trailing and consecutive hyphens, which made invalid RDS and ElastiCache identifiers
  - The `redis` key grants `delivery.logs.amazonaws.com`, scoped to the account, for log delivery into its encrypted log groups
  - `ecs-service` rejects `iam:PassRole` and `iam:` actions on `Resource "*"` in `extra_policy_jsons` (stricter than `lambda` 1.0.0)
  - Each README lists known limits: the `redis` AUTH token in state (use `auth_mode = "rbac"`), the `https_egress_cidr_blocks` default, and the unexercised aws provider floor (5.70)
- OPA rules for the new resource types, each with positive and negative tests (218 tests, 99.9 % coverage); `ecr_kms` requires a customer-managed `kms_key` (KMS without one is the AWS-managed `aws/ecr` key):
  - `iacp.encryption.rds_storage`, `iacp.encryption.rds_cmk`, `iacp.encryption.elasticache_at_rest`, `iacp.encryption.elasticache_cmk`, `iacp.encryption.elasticache_transit`, `iacp.encryption.ecr_kms`
  - `iacp.network.rds_public`, `iacp.network.ecs_public_ip`
  - `iacp.ecr.scan_on_push`, `iacp.ecr.immutable_tags`
  - The non-compliant conftest fixture trips every new rule; `verify.sh` now asserts 30 deny and 4 warn rule IDs, and `verify-example-gate.sh` proves the example gate rejects `?ref=main` and third-party sources.
- `policies/example-plan.sh` and `make example-plans`: plan a module's `examples/complete` and run conftest with its `policy-context.json`, allowing only the deny for relative `../` module sources.
- CI: a `module-tests-offline` job runs suites that mock the AWS provider without LocalStack, and the `module-tests` job, which skips those suites, now plans and checks every module example with conftest. LocalStack suites run as before.
- Pipeline extras (WBS 5.4, 5.5, 5.7, 6.1):
  - Code scanning: TFLint and Checkov also write SARIF in `ci.yml` (modules) and `pr-validate.yml` (per stack); the gating runs are unchanged. `tools.stack_pipeline sarif` rewrites stack SARIF to repository paths, and a separate `code-scanning` job uploads it with `github/codeql-action/upload-sarif` under one category per tool and stack (`ci/tflint`, `ci/checkov`, `pr-validate/<tool>/<stack-id>`).
  - Informational Infracost estimate per stack in `pr-validate.yml` (pinned `0.10.49`, sha256 verified), in its own `cost` job on a redacted plan (`cost-input`), folded into `summary.json` and the sticky comment by a `cost-summary` job (`fold-cost`). It never changes the result. Skipped with a notice while `INFRACOST_API_KEY` is not configured.
  - Post-apply smoke test in `apply.yml`: `tools.stack_pipeline smoke` checks the declared outputs and the required `iacp:*` tags in state and on the live LocalStack resources (read-only AWS CLI calls), writes the job summary and is reported as stage `smoke`. It refuses any endpoint that is not loopback or `*.localstack.cloud`.
  - `apply.yml` documents its provider cache parity with `pr-validate.yml`.

### Security
- The backend fails closed: it binds to loopback by default, refuses to start without `IACP_AUTH_MODE`, keeps internal endpoints behind a constant-time token check, and enforces team membership on request creation.
- The API runs as the `iacp_app` role, which can only INSERT and SELECT on `audit_events`. Triggers block UPDATE, DELETE and TRUNCATE.
- The local stack binds LocalStack and Postgres to loopback only. Images are pinned by digest.
- `make` forces LocalStack dummy credentials and unexports `AWS_PROFILE` and session tokens.
- Machine endpoints fail closed: with neither OIDC nor `IACP_INTERNAL_TOKEN` configured they reject every call, a half OIDC configuration stops the API from starting, and a genuine token from a workflow outside the allow-list gets `403 workflow_not_allowed`.
- OIDC callers are scoped per route class: lock routes accept only `apply.yml`/`ttl-reaper.yml` on main, catalog indexing only `module-release.yml` on main, so a pull-request token only reaches `/v1/runs`. Each pipeline also has an `event_name` allow-list (`pull_request_target` and `workflow_run` never qualify), and run reports must carry the token's `sha`.
- The JWKS endpoint must be https (loopback excepted) and redirects are not followed. Fetches are shared (singleflight) and detached from the caller, so a disconnecting client cannot block key refreshes.
- When `IACP_OIDC_REPOSITORY_ID` is set every token must carry it; the API warns when it is unset.
- `iacp_app` can only SELECT, INSERT and UPDATE `pipeline_runs`.
- `/metrics` shares the API listener but is closed by default. It answers direct loopback peers, never a request with forwarding headers, and otherwise needs `IACP_METRICS_TOKEN`, which is separate from the internal token. Methods outside the standard set are labelled `OTHER`, so junk methods cannot add series. The loopback rule assumes no same-host proxy relays outside traffic without forwarding headers.
- A replayed GitHub OIDC token or one without `jti` gets a 401. A replay-store outage also refuses the token, but with a retryable `503 replay_store_unavailable` and its own log line. A claim deletes only its own expired row; a background sweep purges the rest in batches of 1,000. `iacp_app` has no UPDATE on `oidc_jti`.
- Pull-request tokens can now also read the budget context, which is read-only, for any team's stack path (an accepted exposure, documented in docs/07). They still cannot take locks or index the catalog.
- `tools/stack_pipeline` mints a new GitHub OIDC token for every API request, retries included, because the API now rejects a reused `jti`; the shared internal token is still reused.
- `tools/codeowners` only counts git-tracked stack roots and rejects `platform_paths` under `/environments/<env>/`, which team rules would shadow.
- `security-events: write` is granted only to the `code-scanning` upload jobs, which run no PR code; fork and Dependabot PRs skip the upload. Stack SARIF paths always stay inside the repository, and a run's own `automationDetails` is dropped so a PR cannot pick its category. `INFRACOST_API_KEY` is passed only to the `cost` job, which runs no PR code.

### Fixed
- Portal live status and lifecycle review follow-ups:
  - the status stream gave up after about 25 minutes on a healthy idle stream, because heartbeats did not count and the backend ends every stream at 5 minutes. A connection now counts as failed only if nothing arrived (not even a heartbeat) and it closed within 10 seconds or went silent;
  - an idle watchdog cuts a connection after 45 seconds without a frame or heartbeat and resumes with `Last-Event-ID`;
  - after a healthy connection, reconnects start at least 3 seconds apart, so a stream that sends one event per connection cannot loop tightly;
  - the SSE parser scans each chunk once instead of re-parsing the whole buffer (which was quadratic), caps a frame at 256 KiB, and drops an oversized frame and resynchronises;
  - a `200` that is not an event stream has its body cancelled;
  - the polling fallback (`RequestPoller`) stops on 401, 403 and 404 with a message and pauses while the tab is hidden;
  - a request that was already terminal when the page loaded opens no stream;
  - pull request links are rendered only for `https://github.com/` URLs;
  - the destroy dialog clears the typed name and justification on Cancel and Escape, ignores a second submit while one is in flight, and stays open on Escape while it is;
  - without a spec-carrying request, the update page says plainly that the stack's real resources are unknown;
  - new tests for the role mirror with an unknown environment and for a resumed stream that replays a seq. The stream and resume behaviour is documented in docs/06 §8.1.
- The post-apply smoke test no longer fails stacks with CloudWatch alarms on LocalStack, which drops the tags passed to `PutMetricAlarm`: `aws_cloudwatch_metric_alarm` is left out of the tag checks (`EMULATOR_UNTAGGED`) and named in the report. For the same reason the pr-validate apply round trip checks the re-plan with `tools.stack_pipeline converged` instead of `-detailed-exitcode`: it still fails on any leftover change except a tag-only update of an alarm.
- Portal request form: focus moved to the error summary in a microtask that could run before the first error's summary existed, so the first failed submit left focus on the button. It now moves in an effect.
- The catalog index accepted the category `data`, which `module.yaml` cannot carry, and rejected `database` and `cache`, which it can. It now accepts exactly the categories in `modules/_shared/module.schema.json`, so the `dynamodb` manifest indexes.

### Changed
- `AGENTS.md`: local-first rules, the 70/30 object-oriented and functional coding style, and the small-commit, reviewed-PR workflow.
- Review follow-ups for the tools and CI:
  - SARIF paths with `%`, a backslash or a drive letter go to the stack's `main.tf`; the smoke endpoint check rejects backslashes and userinfo.
  - `cost-input` builds the Infracost plan from an allow-list of the fields Infracost reads (unknown keys, `resource_drift`, variable defaults and literal expressions are dropped) instead of a deny-list.
  - `ci.yml` strips `automationDetails` and `versionControlProvenance` from the TFLint and Checkov SARIF with `jq` into a separate directory that is the only one uploaded (fails closed, no PR code or pip install in that step).
  - `cost-summary` uploads `summary-cost-<id>-<run attempt>` instead of overwriting `pr-validate-<id>`; `comment` and `report` prefer it and fall back to the original.
  - The Infracost switch is the repository variable `INFRACOST_ENABLED=true`; `detect` no longer reads the secret.
  - New `module-tests-floor` job and `make module-test-floor`: the offline module suites run on each module's minimum provider versions (`tools.provider_floor`).
  - `tools.required_checks` (`make required-checks-check`) fails when a required status check is not a job name; `main-branch.json` now requires the module test, provider floor, Terratest and semver gate jobs and the new `ci-result` aggregate (a failed `detect` cannot pass by skipping the jobs that need it); the tool also fails on an empty list and on a required name shared by two jobs.
  - `modules/lambda` 2.0.0 and `modules/ecs-service` 2.0.0 (breaking): `extra_policy_jsons` rejects `iam:PassRole` in any wildcard spelling (`iam:Pass?ole`, `i?m:PassRole`) and `iam:` actions on `Resource "*"` or a wildcard account such as `arn:aws:iam::*:*`; ecs-service 1.0.0 accepted the `?` spellings. See the module CHANGELOGs for upgrade notes.
  - The provider floor job also asserts that `terraform init` selected exactly each floor (`tools.provider_floor verify`), and `make module-test-floor` finds the same suites as CI.

### Removed
- The compiled `backend/api` binary (8.7 MB) is no longer tracked; Go build outputs under `backend/` and `tests/` are ignored.

### Planned
- M0 Foundations (remaining): smoke stack against LocalStack. The real-AWS state bucket and OIDC roles are deferred by ADR-0009.
- M1 Module registry (remaining): upgrade-plan test and CHANGELOG/upgrade-notes check in `module-release.yml`, a live catalog index once a hosted API exists, Lambda Terratest.
- M2 Orchestration engine (remaining): GitHub App PRs, webhooks, OAuth/RBAC, `pr-validate.yml`, `apply.yml`.
- M3 Security gates (remaining): wiring the budget context into the workflows, approval and budget policies, applying the ruleset and Environment protection.
- M4 Portal and destroy: Next.js dashboard, destroy flow, Infracost budget gating (WBS 8.5).

## [0.1.0] - 2026-11-24

### Added
- Project overview (`OVERVIEW.md`), work breakdown (`WBS.md`), agent guidelines (`AGENTS.md`).
- Design documentation in `docs/`:
  - `00-problem-analysis.md`: problem, goals, personas, requirements, build-vs-buy, risks.
  - `01-system-architecture.md`: components, control plane and execution plane, config generation.
  - `02-aws-infrastructure.md`: multi-account layout, state backend, OIDC roles, cost estimate.
  - `03-data-model-and-api.md`: PostgreSQL DDL, state machines, REST API.
  - `04-module-registry.md`: module contract, `module.yaml` spec, versioning.
  - `05-automation-pipeline.md`: GitHub Actions workflows and pipeline stages.
  - `06-lifecycle-management.md`: upgrades, locking, drift, TTL, recovery, RBAC, budgets.
  - `07-security-and-policy.md`: threat model, IAM, OPA and Checkov policies.
  - `08-mvp-roadmap.md`: milestones, acceptance criteria, demo script.
  - `adr/0001`–`0008`: architecture decision records.
- Diagrams generated with Python (`diagrams`, graphviz, matplotlib) in `docs/diagrams/`:
  - Architecture, AWS infrastructure, CI/CD pipeline, backend components.
  - Data model (ERD) and request and stack state machines.
  - Sequence diagrams: provision request, apply on merge, drift detection, TTL teardown, module release.

[Unreleased]: https://github.com/bibekgupta3333/self-service-iac-platform/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/bibekgupta3333/self-service-iac-platform/releases/tag/v0.1.0
