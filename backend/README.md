# backend/

Go control plane: REST API, request orchestration, catalog, config generator and audit log.
Terraform never runs here (AGENTS.md hard rule 2). The backend renders files and records state,
and GitHub Actions plans and applies.

Design: [docs/01-system-architecture.md](../docs/01-system-architecture.md),
[docs/03-data-model-and-api.md](../docs/03-data-model-and-api.md),
[ADR-0009](../docs/adr/0009-local-first-localstack-and-local-backend.md) (local-first),
[ADR-0010](../docs/adr/0010-audit-hash-chain-computed-in-go.md) (audit chain).

## Layout

```
cmd/api/                 HTTP API entrypoint: config, pool, migrations, wiring, graceful shutdown
migrations/              goose SQL migrations, embedded into the binary (embed.FS)
internal/
  config/                IACP_* environment config (pure Load over a lookup func)
  observability/         slog JSON logger, request-scoped logger in context
  platform/              Clock and IDGenerator ports (+ system and fixed implementations)
  fp/                    small generic helpers: Map, Filter, Reduce, SortedKeys, Duplicates
  apperr/                transport-agnostic errors (kind + stable code + field errors)
  identity/              Principal, context helpers, UserStore (dev user upsert)
  db/                    pgx pool, Migrator (goose), TxRunner, DBTX; dbtest/ for integration tests
  inputschema/           JSON Schema 2020-12 compile + validate, pointer-addressed violations
  catalog/               modules, versions, SemVer, index validation, CatalogService, Postgres repo
  requests/              state machine, Request entity, spec validation, RequestService, Postgres repo
  locks/                 per-stack platform lock: pure Decide/NextInQueue/CheckToken, Service, Postgres repo
  auth/                  GitHub Actions OIDC: claim allow-lists (pure), JWKSCache, Verifier
  runs/                  POST /v1/runs reports: pure Validate/CheckProvenance/RunStatus, Service, Postgres repo
  generator/             pure Terraform renderer + BackendStrategy / ProviderStrategy, golden files
  publish/               Publisher port; pure BranchName/ParseBranch, ValidatePath/StackFiles, PR Title/Body/Labels
    githubapp/           GitHub App adapter: App JWT → cached installation token, git data API commit, PRs, labels, dispatch
    localgit/            local-first adapter (ADR-0009): git plumbing on a scratch clone, fake PRs in the git dir
  outbox/                Postgres job queue (outbox table): Enqueue in the producer's tx, Worker with lease/backoff
  prflow/                request.publish handler: render → commit on iacp/<short id> → PR → record pr_opened
  webhooks/              pure VerifySignature and Map (event → command), Ingester (dedupe + enqueue), Applier
  audit/                 pure hash chain (CanonicalJSON, ChainHash, VerifyChain), Writer, Verifier
  httpapi/               chi router, middleware, problem+json, Authenticator seam, handlers
```

### Style: about 70% object-oriented, 30% functional

- **Objects (structure):** services, repositories, handlers and the generator are structs built by
  constructors that take small interfaces (`catalog.NewService(repo, clock, repoURL)`,
  `requests.NewService(repo, versions, members, locker, clock, ids)`). Entities guard their
  invariants in methods (`Request.Fire`, `Request.Approve`, `ModuleVersion.Usable`).
- **Functions (logic):** the request transition table and `Transition`, SemVer parsing and ordering,
  `ValidateIndex`, `ValidateShape` / `ValidateResources` / `ValidateEnvironment`, reference masking,
  all generator renderers, `ChainHash` / `VerifyChain`, cursors and request hashes, the lock
  decisions (`locks.Decide`, `NextInQueue`, `CheckToken`), OIDC `CheckClaims` / `ParseJWKS`, and run
  `Validate` / `CheckProvenance` / `RunStatus`. They take values and return values with no I/O, so
  their tests need no fakes.
- **Boundary:** I/O lives only in adapters: `*PostgresRepository`, `audit.Writer` / `Verifier`,
  `identity.UserStore`, `db.Migrator`, `auth.JWKSCache` and the HTTP handlers.

### Migrations: why goose

Migrations use [goose](https://github.com/pressly/goose) through its `Provider` API, with no global
state, over the embedded `migrations.FS`. docs/03 already specifies goose. It handles plpgsql
blocks (`-- +goose StatementBegin`) and down migrations, and a hand-written runner would only
re-implement its version table. The API applies pending migrations on start when
`IACP_AUTO_MIGRATE=true` (the default), over the owner connection
(`IACP_MIGRATE_DATABASE_URL`), under a goose Postgres session lock so replicas starting
together apply each migration once.

| File | Contents |
|------|----------|
| `0001_init.sql` | enums, teams, users, memberships, modules, module_versions, environments, stacks, stack_resources, requests, locks, idempotency_keys |
| `0002_audit.sql` | append-only `audit_events`: link-check trigger, UPDATE/DELETE/TRUNCATE rejection, `iacp_app` INSERT/SELECT grant |
| `0003_seed_local.sql` | `iacp-bot` system user and the `development` environment (LocalStack account `000000000000`) |
| `0004_app_role_grants.sql` | runtime grants for `iacp_app`: DML on platform tables, INSERT/SELECT only on `audit_events`, nothing on `goose_db_version` |
| `0006_webhooks_outbox.sql` | `webhook_deliveries` (delivery id primary key) and `outbox` (job queue: `available_at`, `locked_until`, `attempts`, `dedupe_key`); `iacp_app` gets SELECT/INSERT/DELETE and SELECT/INSERT/UPDATE respectively |
| `0005_pipeline_runs.sql` | `pipeline_kind`, `pipeline_status`, `pipeline_runs` (one row per run, attempt and stack, `stack_id NOT NULL`; one `stages` entry per stage name); `iacp_app` gets SELECT/INSERT/UPDATE only |

### Database roles

The API never serves requests as the schema owner:

- `IACP_MIGRATE_DATABASE_URL` (owner) applies migrations and, when `IACP_APP_DB_PASSWORD` is set,
  gives `iacp_app` a login with that password. The connection is closed before serving.
- `IACP_DATABASE_URL` (`iacp_app`) serves requests. It cannot update or delete audit rows,
  disable triggers, set `session_replication_role`, or run DDL. If it points at an owner or
  superuser, the API logs a warning at startup.

In `IACP_AUTH_MODE=dev` the app password defaults to `iacp-app-local-only`, which matches the
default runtime URL, so a local `go run` works against the compose Postgres with no extra setup.

Tables for later milestones (plan summaries, costs, drift, budgets, policy exceptions, stack
dependencies) arrive with their features.

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `IACP_AUTH_MODE` | none: **required** | `dev` (every user call is the local user). The API refuses to start without it |
| `IACP_HTTP_ADDR` | `127.0.0.1:8080` | listen address; the container image sets `0.0.0.0:8080` |
| `IACP_DATABASE_URL` | `postgres://iacp_app:iacp-app-local-only@localhost:5432/iacp?sslmode=disable` | runtime connection as `iacp_app` |
| `IACP_MIGRATE_DATABASE_URL` | `postgres://iacp:iacp-local-only@localhost:5432/iacp?sslmode=disable` | schema-owner connection, used only for migrations |
| `IACP_APP_DB_PASSWORD` | `iacp-app-local-only` in dev mode, otherwise unset | password applied to `iacp_app` after migrating |
| `IACP_INTERNAL_TOKEN` | unset | shared bearer token for `/v1/internal/*` and `/v1/runs`, at least 32 characters, accepted next to GitHub OIDC (fallback until every workflow sends an OIDC token) |
| `IACP_OIDC_AUDIENCE` | unset (OIDC off) | `aud` the workflows request (`core.getIDToken(aud)`); must be set together with `IACP_OIDC_REPOSITORY` |
| `IACP_OIDC_REPOSITORY` | unset | `owner/name` allowed to call; `repository_owner` must match the owner part |
| `IACP_OIDC_REPOSITORY_ID` | unset (startup warning) | numeric `repository_id` every token must carry; set it in any shared environment (owner/name can be re-registered after a rename or delete) |
| `IACP_OIDC_ALLOWED_REFS` | `refs/heads/main,refs/pull/*/merge` | comma-separated `path.Match` patterns for the `ref` claim |
| `IACP_OIDC_ALLOWED_WORKFLOWS` | `<repo>/.github/workflows/*@refs/heads/main,<repo>/.github/workflows/pr-validate.yml@refs/pull/*/merge` | patterns for `job_workflow_ref` |
| `IACP_OIDC_LEEWAY` | `30s` | clock skew allowed on `exp`/`nbf`/`iat` (max `5m`) |
| `IACP_OIDC_ISSUER` | `https://token.actions.githubusercontent.com` | override for tests only (plain http allowed only on loopback) |
| `IACP_OIDC_JWKS_URL` | `<issuer>/.well-known/jwks` | override for tests only; must be https (plain http only on loopback), or the API refuses to start |
| `IACP_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `IACP_SHUTDOWN_TIMEOUT` | `15s` | drain time on SIGINT/SIGTERM |
| `IACP_AUTO_MIGRATE` | `true` | apply migrations on start |
| `IACP_DEV_USER` | `local-dev` | login of the local user (upserted on start) |
| `IACP_MODULE_REPO_URL` | this repository's GitHub URL | git URL used in pinned module sources |
| `IACP_PUBLISHER` | `none` | `none` (requests stay in `draft`), `local` (commit to a local clone, fake PR) or `github` (GitHub App) |
| `IACP_PUBLISH_BASE_BRANCH` | `main` | branch PRs target and request branches start from |
| `IACP_LOCAL_GIT_REPO` | unset | `local` only: path of the clone to commit to (required) |
| `IACP_GITHUB_APP_ID` / `IACP_GITHUB_INSTALLATION_ID` | unset | `github` only: App and installation ids (required) |
| `IACP_GITHUB_PRIVATE_KEY_PATH` | unset | `github` only: path to the App's PEM key file (required; never put the key itself in env) |
| `IACP_GITHUB_REPOSITORY` | unset | `owner/name` the App writes to (required for `github`); when set, webhooks for other repositories are ignored |
| `IACP_GITHUB_API_URL` | `https://api.github.com` | GitHub API base (https; plain http only on loopback) |
| `IACP_GITHUB_WEBHOOK_SECRET` | unset (webhooks rejected) | HMAC secret of the App webhook, at least 16 characters |
| `IACP_PORTAL_URL` | `http://localhost:3000` | portal base URL for the request link in PR bodies |
| `IACP_LOCALSTACK_ENDPOINT` | `http://localhost:4566` | endpoint rendered into generated provider blocks |
| `IACP_WORKER_POLL_INTERVAL` | `1s` | idle poll interval of the in-process outbox worker (50ms–1m) |

The publisher settings fail closed: `github` with any App setting missing, App settings with
another publisher, `local` without `IACP_LOCAL_GIT_REPO`, or an unknown value stop the API at startup.

## Run

```bash
docker compose up -d postgres            # from the repo root
cd backend && IACP_AUTH_MODE=dev go run ./cmd/api

curl -s localhost:8080/healthz           # liveness
curl -s localhost:8080/readyz            # database reachable
```

Teams and memberships come from GitHub team sync (WBS 4.4). Creating a request needs the
`developer` role (or higher) in the target team, so until then add both by hand:

```bash
psql postgres://iacp:iacp-local-only@localhost:5432/iacp <<'SQL'
INSERT INTO teams (slug, name, github_team_id, github_team_slug, cost_center)
VALUES ('payments', 'Payments', 1, 'payments', 'cc-0001');
INSERT INTO memberships (team_id, user_id, role)
SELECT t.id, u.id, 'developer' FROM teams t, users u
WHERE t.slug = 'payments' AND u.github_login = 'local-dev';
SQL
```

Container image (static binary on distroless, non-root):

```bash
docker build -t iacp-api:dev backend/
docker run --rm -p 127.0.0.1:8080:8080 -e IACP_AUTH_MODE=dev \
  -e IACP_DATABASE_URL=... -e IACP_MIGRATE_DATABASE_URL=... iacp-api:dev
```

## API (this slice)

| Method | Path | Notes |
|--------|------|-------|
| GET | `/healthz`, `/readyz` | no auth |
| GET | `/v1/catalog/modules?category=&status=` | modules with `latest_version` |
| GET | `/v1/catalog/modules/{name}` | module + latest version detail |
| GET | `/v1/catalog/modules/{name}/versions` | SemVer descending |
| GET | `/v1/catalog/modules/{name}/versions/{version}` | manifest view: `inputs_schema`, outputs, pinned `source` |
| POST | `/v1/internal/catalog/index` | machine auth (below); `201` created, `200` same commit re-indexed (metadata changes are audited as `module.updated`), `409 version_immutable` |
| GET | `/v1/internal/stacks/{id}/lock` | machine auth; current lock (`404` when unlocked) |
| POST | `/v1/internal/stacks/{id}/lock` | machine auth; `{"request_id"}`; `200 {"lock":"granted"\|"held", "current":{…, "fencing_token"}}` or `202 {"lock":"queued","queued_behind":…}` |
| POST | `/v1/internal/stacks/{id}/lock/heartbeat` | machine auth; `{"request_id","fencing_token"}` renews the lease; `409 stale_fencing_token` if superseded |
| DELETE | `/v1/internal/stacks/{id}/lock?request_id=&fencing_token=` | machine auth; releases and promotes the oldest queued request: `{"released":…, "promoted":…\|null}` |
| POST | `/v1/runs` | machine auth; run report (docs/03 §6.9) stored in `pipeline_runs`; `200 {"accepted":true,"run_id","run_status","request_status"}`, `409 stale_fencing_token`, `403 run_mismatch` / `workflow_not_allowed` |
| POST | `/v1/requests` | `kind=create` only so far; developer role in the team (`403` otherwise); requires `Idempotency-Key` (remembered 24 h); `202`; replays set `Idempotent-Replayed: true` |
| GET | `/v1/requests?team=&status=&kind=&mine=true&limit=&cursor=` | keyset pagination, `next_cursor` |
| GET | `/v1/requests/{id}` | includes the normalised `spec`, `allowed_events`, `github {branch, pr_number, pr_url}` once published and `failure_reason` |
| POST | `/v1/webhooks/github` | GitHub App webhook, authenticated by `X-Hub-Signature-256` only; `202 {"accepted","duplicate","enqueued"}`, `401 invalid_signature` / `webhook_not_configured`, `400` for a bad delivery id, event header or payload |
| GET | `/v1/audit/verify?from_seq=&to_seq=` | recomputes the hash chain, at most 10,000 events per call (`has_more`) |

Errors are RFC 9457 `application/problem+json` with `code`, `request_id` and `errors[].pointer`.
`X-Request-Id` is echoed (or generated) and recorded as the audit `correlation_id`.

Authentication goes through `httpapi.Authenticator`, and it fails closed:

- User routes (`/v1/...`) use the authenticator selected by `IACP_AUTH_MODE` (only `dev` for now).
- Machine routes (`/v1/internal/*` and `POST /v1/runs`) use a separate authenticator; the dev
  user never reaches them. It accepts a GitHub Actions OIDC JWT when `IACP_OIDC_*` is set and the
  shared `IACP_INTERNAL_TOKEN` (constant-time compare) when set. With neither, every call gets 401.
- OIDC verification (`internal/auth`): RS256 signature against the issuer's JWKS, then `iss`,
  `aud`, `exp` (required) / `nbf` / `iat` with the configured leeway, then `repository`,
  `repository_owner`, `repository_id` (required on every token when `IACP_OIDC_REPOSITORY_ID` is
  set; the API warns at startup when it is not), and the global `ref` / `job_workflow_ref`
  allow-lists. `alg: none`, HS256 and every other algorithm are rejected. A genuine token from a
  workflow outside an allow-list gets `403 workflow_not_allowed`.
- JWKS (`auth.JWKSCache`): https only (plain http only on loopback), redirects never followed,
  responses capped at 1 MiB. Keys are cached 1 h; an unknown `kid` triggers a refetch at most
  every 30 s after the last *completed* attempt. Concurrent misses share one fetch
  (singleflight) that runs detached from the caller's context with its own 10 s timeout, so a
  client that disconnects cannot cancel it or start the rate limit, and no lock is held during
  I/O. Cached keys keep working during an issuer outage.
- Route classes (`auth.RoutePolicies`, enforced by middleware): lock routes accept only
  `apply.yml` / `ttl-reaper.yml` on main via `push`, `workflow_dispatch` or `schedule`; catalog
  indexing only `module-release.yml` on main via `push` or `workflow_dispatch`; `/v1/runs` the four
  pipeline workflows. A pull-request token therefore only ever reaches `/v1/runs`.
- `POST /v1/runs` then checks provenance per pipeline: the report's `run_id`/`run_attempt` and
  `head_sha` must be the token's run and `sha` (`403 run_mismatch`); `validate` must come from
  `pr-validate.yml` on `pull_request`, `apply` from `apply.yml` on main via `push` /
  `workflow_dispatch`, `drift` and `ttl_destroy` from `drift.yml` / `ttl-reaper.yml` on main via
  `schedule` / `workflow_dispatch`. `pull_request_target` and `workflow_run` never qualify.
- Calls with the shared token are not workflow-scoped and skip these OIDC checks.
- 401 bodies are generic; the reason is only logged.

GitHub OAuth sessions (WBS 4.4) plug in at the same seam without handler changes.

## Stack lock (docs/06 §3)

`locks.Service` keeps one lease per stack in `locks` with a fencing token from `lock_fencing_seq`
(it only grows, so a later grant always has a larger token than every earlier one on the stack).

- `POST /v1/requests` takes the lock in the request-creation transaction, or leaves the request in
  `draft` with `queued_behind` set to the holder. A queued request cannot fire `pr_opened`
  (`409 stack_locked`).
- Every lock operation first takes `SELECT … FOR UPDATE` on the `stacks` row, so operations on one
  stack serialise; the grant itself is the docs/03 upsert. `TestConcurrentAcquireHasOneWinner`
  shows two racing acquirers always produce one `granted` and one `queued`.
- Lease: 7 days (`locks.DefaultLease`), renewed by heartbeat. An expired lease can be taken over,
  but the oldest queued request gets it first, so newcomers never jump the queue.
- Release checks the holder and token (`409 stale_fencing_token` otherwise), deletes the lease and,
  in the same transaction, grants the oldest waiter a new token and re-points the rest of the queue
  at it. `lock.acquired`, `lock.queued` and `lock.released` are written to the audit log; a waiter
  polling again behind the same holder writes nothing.
- `POST /v1/runs` checks `fencing_token` against the current lease (`FOR SHARE`) for `validate`,
  `apply` and `ttl_destroy` reports, so a late callback from a superseded run is rejected. It
  locks the `stacks` row (`FOR KEY SHARE`) first, the same order as lock operations, so reports
  cannot deadlock with release (`TestRecordAndReleaseDoNotDeadlock`). A report for a stage the run
  already recorded replaces that entry, so retries are idempotent; a run keeps at most 32 stages.

Request ids look like `req-` followed by 10 Crockford base32 characters, so collisions on the
unique `short_id` are negligible.

## Publishing PRs and GitHub webhooks (WBS 4.8, 4.9)

```
POST /v1/requests ─tx─► request (draft) + stack lock + outbox "request.publish" + audit
outbox worker ─────────► prflow: render (generator) → Publisher.CommitFiles(iacp/<short id>)
                         → OpenPullRequest → AddLabels           (no DB transaction held)
                ─tx─►    RecordPublished: pr_opened, branch/PR/tree sha, audit   (or RecordPublishFailure)
POST /v1/webhooks/github → verify HMAC → Map (pure) ─tx─► webhook_deliveries + outbox "github.event"
outbox worker ─────────► Applier → ApplyGitHubEvent / RecordWorkflowHint (idempotent, audited)
```

- **Publisher port** (`internal/publish`): `CommitFiles`, `OpenPullRequest`, `AddLabels`,
  `DispatchWorkflow`, `ClosePullRequest`, `DeleteBranch`. Commits replace the stack root as a whole
  (`Commit.Root`): files under it that a render no longer produces are deleted. Every call except
  dispatch is safe to retry: the branch is created or force-moved, and opening a PR whose head
  already has one returns it.
- **`githubapp`**: RS256 App JWT (iat −60 s, exp +9 min) exchanged for an installation token
  scoped to `IACP_GITHUB_REPOSITORY` with `contents`, `pull_requests` and `actions` write only,
  cached until 5 minutes before expiry and refreshed once on a 401. Files are committed atomically
  with the git data API (blobs, one tree on the base tree with null-sha deletions from a recursive
  listing, a commit, then the ref; the ref is force-moved only when GitHub says it already exists).
  15 s per-request timeout, redirects only to the API host, bodies capped at 10 MiB, retries
  (4 attempts) on 5xx, 429 and secondary rate limits honouring `Retry-After` (more than 60 s fails
  fast). Workflow dispatch is sent once, never retried. The App needs Contents, Pull requests and
  Actions (read/write) and Metadata (read). Registering it is WBS 2.5.
- **`localgit`**: `git` via `os/exec` with argument arrays (never a shell), hooks disabled, inherited
  `GIT_*` dropped. `IACP_LOCAL_GIT_REPO` must be the root of a work tree (`rev-parse
  --show-toplevel`), so a directory inside another repository is refused. Blobs and the tree are
  built on a temporary index, so the clone's checkout is untouched and the branch moves with one
  `update-ref`. Fake PRs, labels and dispatches are recorded in `<git dir>/iacp-local/state.json`
  and the PR URL is `file://<repo>#pull/<n>`. Repository-local git config of the clone (for example
  `core.fsmonitor`) still applies, so use an operator-owned clone.
- **Two-step consistency**: the publish job commits with the request. GitHub is called outside any
  transaction, and the result is recorded in a second one that checks the request is still `draft`
  and holds the **current lease** of its stack (the lock row, via `locks.CheckToken`) with the
  token the files were rendered with; `PublishTarget` makes the same check before rendering. Each
  failed attempt is audited (`request.publish_failed`); after the last (6, backoff 2 s doubling up
  to 5 min), or a permanent error such as an unresolvable module version or a conflict when
  recording, the request moves to `failed` (`failure_reason = publish_failed`), releases its lock,
  and the branch and PR it left behind are withdrawn best effort (PR closed, branch deleted; the
  outcome is in the audit payload).
- **Promotion**: every time a queued request becomes the lock holder (a terminal transition,
  `DELETE /v1/internal/stacks/{id}/lock`, or an expired lease handed to the head of the queue by a
  newcomer's acquire), `locks.Service`'s promotion hook (`requests.PublishOnPromote`) enqueues its
  publish job in the same transaction. Terminal transitions release the lock in their own
  transaction, stacks row locked first, as every lock operation does.
- **Webhooks**: `X-Hub-Signature-256` is checked with `hmac.Equal`; no secret means every delivery
  gets 401. The body is capped at 2 MiB. `X-GitHub-Delivery` is deduped by the
  `webhook_deliveries` primary key in the same transaction that enqueues the command, so a
  redelivery is acknowledged with `duplicate: true` and no second job. Mapping:
  `pull_request` closed+merged → `pr_merged` (approved → applying), closed unmerged → `pr_closed`
  (→ rejected), `synchronize` → `new_commit`, `workflow_run` completed → audited hint. Only
  `iacp/<short id>` branches map to requests; everything else gets `202` with no job. The
  repository filter ignores case.
- **Ordering and replay**: commands carry the PR head SHA. `requests.github_head_sha` tracks the
  head (published commit, then each accepted push); a push must start from it and any other event
  must name it, otherwise the event is audited as `stale_head`. Since the delivery id is not signed,
  this is what stops a replayed body from moving a request. Events not allowed in the current
  status are audited as `request.github_event_ignored`. An event that arrives before the PR is
  recorded is retried through the outbox instead of being dropped.
- **Queue**: the `outbox` table (migration 0006) and an in-process worker goroutine
  (`FOR UPDATE SKIP LOCKED`, 2-minute lease, so several API replicas can drain it). Outcome writes
  are guarded by the claim's attempt number, so a worker whose lease ran out cannot complete or
  reschedule a job another worker reclaimed, and each handler runs with a deadline of 3/4 of the
  lease. An SQS adapter is a follow-up behind the same `outbox.Handler` contract.
- **Housekeeping (follow-up)**: nothing calls `PurgeDeliveries` (7-day retention) yet, and processed
  outbox rows are not removed; both need a scheduled job before real GitHub traffic.

### Try it locally (`IACP_PUBLISHER=local`)

```bash
git clone --no-checkout . /tmp/iacp-scratch           # or any repo with a main branch
cd backend && IACP_AUTH_MODE=dev IACP_PUBLISHER=local IACP_LOCAL_GIT_REPO=/tmp/iacp-scratch \
  IACP_GITHUB_WEBHOOK_SECRET=local-webhook-secret-1 go run ./cmd/api
# add a team + membership (see Run), index a module version, then POST /v1/requests:
# within about a second the request is pr_open with github.branch = iacp/<short id>
git -C /tmp/iacp-scratch ls-tree -r --name-only iacp/<short id>   # the generated stack root

# simulate GitHub closing the PR unmerged:
BODY='{"action":"closed","number":1,"pull_request":{"merged":false,"head":{"ref":"iacp/<short id>"}}}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac local-webhook-secret-1 -hex | awk '{print $NF}')"
curl -s localhost:8080/v1/webhooks/github -H 'X-GitHub-Event: pull_request' \
  -H "X-GitHub-Delivery: $(uuidgen)" -H "X-Hub-Signature-256: $SIG" -d "$BODY"
# → 202; the request becomes rejected and the stack lock is released
```

Use a fresh scratch clone with a fresh database: fake PR numbers restart at 1 per clone and
`requests.github_pr_number` is unique.

## Config generator

`generator.New(repoURL, backend, provider).Render(spec)` returns `main.tf`, `versions.tf`,
`providers.tf`, `backend.tf`, `outputs.tf`, `variables.tf` and `iacp.stack.json` as bytes, with no I/O. Rendered files are `terraform fmt`- and `tflint`-clean with the repository config (both checked in tests when the tools are on `PATH`).

- Output is deterministic: resources and keys are sorted, there are no timestamps, and
  `hclwrite.Format` gives `terraform fmt`-clean files. User strings are escaped, so `${` cannot
  inject expressions. Only `{"$ref": "<resource>.<output>"}` becomes `module.<resource>.<output>`.
- Module sources are always pinned: `git::<repo>//modules/<name>?ref=modules/<name>/vX.Y.Z`.
  The resolved commit SHA is recorded in `iacp.stack.json`.
- `BackendStrategy`: `LocalBackend` (used now, ADR-0009) and `S3Backend` (S3 with `use_lockfile`,
  implemented but not wired).
- `ProviderStrategy`: `LocalStackProvider` (endpoints only, with credentials taken from the environment
  such as the Makefile's `test`/`test`) and `AWSProvider` (`allowed_account_ids`, OIDC credentials).

Golden files live in `internal/generator/testdata/golden/<case>/`. After an intended change:

```bash
go test ./internal/generator -update && git diff internal/generator/testdata
```

## Test

```bash
cd backend
go vet ./... && gofmt -l . && go test ./...          # unit + handler tests, no database needed

# Integration tests: repositories, migrations, audit triggers, concurrent idempotency.
# Each test creates and drops its own database on the local Postgres and connects as
# iacp_app like the API does. The admin URL comes from IACP_TEST_ADMIN_DATABASE_URL,
# IACP_MIGRATE_DATABASE_URL or IACP_DATABASE_URL (CI sets the last to the superuser).
docker compose up -d postgres
IACP_INTEGRATION=1 go test -race -count=1 ./...
```

`TestTerraformFmtClean` runs `terraform fmt -check` on the rendered files when `terraform` is on
`PATH`, and skips otherwise.
