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
  observability/         slog JSON logger, request-scoped logger in context, Prometheus registry (Metrics)
  platform/              Clock and IDGenerator ports (+ system and fixed implementations)
  fp/                    small generic helpers: Map, Filter, Reduce, SortedKeys, Duplicates
  apperr/                transport-agnostic errors (kind + stable code + field errors)
  identity/              Principal, context helpers, UserStore (dev user upsert)
  db/                    pgx pool, Migrator (goose), TxRunner, DBTX; dbtest/ for integration tests
  inputschema/           JSON Schema 2020-12 compile + validate, pointer-addressed violations
  catalog/               modules, versions, SemVer, index validation, CatalogService, Postgres repo
  requests/              state machine, Request entity, spec validation (create, update, destroy), stack-state gating, RequestService, status feed, apply-target lookup, Postgres repo
  stacks/                read side of stacks: list/show scoped to the caller's teams, Postgres repo
  locks/                 per-stack platform lock: pure Decide/NextInQueue/CheckToken, Service, Postgres repo
  auth/                  GitHub Actions OIDC: claim allow-lists (pure), JWKSCache, Verifier, jti ReplayGuard
  budgets/               policy context for conftest (pure ParseStackPath / BuildContext, Service, Postgres repo) and budget administration (pure NewWrite validation and AuditEvents, AdminService, PostgresAdminRepository)
  drift/                 drift reports: pure ClassifyExit/NextHealth/Allowed/Validate, Service (Record, Resolve), Postgres repo,
                         RequestReconciler adapter onto requests.Service.ReconcileStack
  runs/                  POST /v1/runs reports: pure Validate/CheckProvenance/RunStatus/Progress, Service (drives apply reports into requests and stacks), Postgres repo
  retention/             hourly sweeper for processed outbox jobs and webhook delivery ids
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
  decisions (`locks.Decide`, `NextInQueue`, `CheckToken`), OIDC `CheckClaims` / `ParseJWKS`, run
  `Validate` / `CheckProvenance` / `RunStatus`, the jti window's eviction (`jtiWindow`, time passed in),
  `budgets.ParseStackPath` / `BuildContext`, and `httpapi.IsDirectLoopback`. They take values and return values with no I/O, so
  their tests need no fakes.
- **Boundary:** I/O lives only in adapters: `*PostgresRepository`, `audit.Writer` / `Verifier`,
  `identity.UserStore`, `db.Migrator`, `auth.JWKSCache`, `auth.MemoryReplayCache` (mutex around the pure
  window), `auth.PostgresReplayStore`, `budgets.PostgresRepository` and the HTTP handlers.

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
| `0005_pipeline_runs.sql` | `pipeline_kind`, `pipeline_status`, `pipeline_runs` (one row per run, attempt and stack, `stack_id NOT NULL`; one `stages` entry per stage name); `iacp_app` gets SELECT/INSERT/UPDATE only |
| `0006_oidc_jti.sql` | `oidc_jti` (used GitHub OIDC token ids until `exp` + leeway, shared by replicas); `iacp_app` gets SELECT/INSERT/DELETE only, no UPDATE |
| `0007_budgets.sql` | `budgets` (team monthly budget, optionally per environment; `spent_usd` 0 until cost data lands in WBS 8.5) |
| `0008_webhooks_outbox.sql` | `webhook_deliveries` (delivery id primary key) and `outbox` (job queue: `available_at`, `locked_until`, `attempts`, `dedupe_key`); `iacp_app` gets SELECT/INSERT/DELETE and SELECT/INSERT/UPDATE respectively |
| `0009_sessions.sql` | `sessions` (hashed opaque session ids, absolute and idle expiry, revocation) |
| `0010_outbox_purge.sql` | `purge_processed_outbox(cutoff, max_rows)`, a `SECURITY DEFINER` function that deletes only processed outbox rows; `iacp_app` gets `EXECUTE` on it and still no `DELETE` on `outbox` |
| `0011_stack_health.sql` | `stacks.health` (`unknown`, `healthy`, `degraded`), set from the post-apply smoke test |
| `0013_drift_reports.sql` | `drift_result`, `drift_status`, `drift_reports` (one row per drift.yml run and stack, CHECKs tie result, status, counts and resolution together) and `stacks.health` widened with `drifted` ([ADR-0013](../docs/adr/0013-drift-in-stack-health.md)); `iacp_app` gets SELECT/INSERT and UPDATE on the resolution columns only, no DELETE |
| `0014_budget_admin.sql` | `budgets`: `updated_by`, `updated_at`, `override_reason`, `override_expires_at`, `override_max_delta_usd` (set or null with `override_by`; a half-made override from 0007 is cleared), `single_change_cap_usd`, `actual_usd`, `actual_recorded_at`; limit, cap and threshold bounds; `stacks.committed_monthly_usd` and `committed_at` |

### Database roles

The API never serves requests as the schema owner:

- `IACP_MIGRATE_DATABASE_URL` (owner) applies migrations and, when `IACP_APP_DB_PASSWORD` is set,
  gives `iacp_app` a login with that password. The connection is closed before serving.
- `IACP_DATABASE_URL` (`iacp_app`) serves requests. It cannot update or delete audit rows,
  disable triggers, set `session_replication_role`, or run DDL. If it points at an owner or
  superuser, the API logs a warning at startup.

In `IACP_AUTH_MODE=dev` the app password defaults to `iacp-app-local-only`, which matches the
default runtime URL, so a local `go run` works against the compose Postgres with no extra setup.

Tables for later milestones (plan summaries, costs, budget overrides, policy exceptions, stack
dependencies) arrive with their features.

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `IACP_AUTH_MODE` | none: **required** | `dev` (every user call is the local user) or `github` (OAuth login and sessions, below). The API refuses to start without it |
| `IACP_GITHUB_CLIENT_ID`, `IACP_GITHUB_CLIENT_SECRET`, `IACP_GITHUB_CALLBACK_URL`, `IACP_GITHUB_ORG` | unset | GitHub OAuth app, its registered callback (`https://<portal>/api/iacp/v1/auth/callback`; http only for loopback) and the organisation whose active members may sign in. All four are required together in `github` mode, and a partial set fails startup in any mode |
| `IACP_AUTH_ADMIN_TEAM` | `iacp-admins` | GitHub team slug whose members become platform admins |
| `IACP_AUTH_REDIRECT_ALLOWLIST` | unset | comma-separated https origins a login may return to (`return_to`); relative paths are always allowed |
| `IACP_AUTH_DEFAULT_REDIRECT` | `/` | landing path when `return_to` is missing or not allowed |
| `IACP_AUTH_SESSION_TTL`, `IACP_AUTH_SESSION_IDLE_TTL` | `8h`, `1h` | absolute and sliding idle session lifetimes |
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
| `IACP_OIDC_REPLAY_STORE` | `postgres` | where used token ids (`jti`) are remembered: `postgres` (table `oidc_jti`, correct with any number of replicas) or `memory` (bounded per-process cache, 10,000 ids; single instance only, warns at startup) |
| `IACP_METRICS_TOKEN` | unset | bearer token for non-loopback scrapers of `/metrics`, at least 32 characters, must differ from `IACP_INTERNAL_TOKEN`; unset means loopback only |
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
| `IACP_TTL_MIN_HOURS`, `IACP_TTL_MAX_HOURS`, `IACP_TTL_DEFAULT_HOURS` | `1`, `720`, `72` | bounds of an ephemeral stack's TTL in hours (a request's `ephemeral.ttl_hours`, and each extension); omitted `ttl_hours` takes the default; the environment's `max_ttl_hours` is a further ceiling |
| `IACP_TTL_MAX_EXTENSION_HOURS` | `168` | most hours extensions may add to one stack in total |
| `IACP_TTL_RETRY_BACKOFF_HOURS`, `IACP_TTL_MAX_ATTEMPTS` | `24`, `3` | after a failed, rejected or cancelled TTL destroy, wait this many hours before the next one (0 disables); after this many unsuccessful attempts the stack is blocked (`ttl.blocked` audit, 0 means no limit). `IACP_TTL_MAX_HOURS` is capped at 8760 |
| `IACP_QUEUE_RETENTION` | `168h` | how long processed outbox jobs and webhook delivery ids are kept before the hourly sweeper deletes them (at least `1h`); pending and dead jobs are never deleted |
| `IACP_TRUSTED_PROXIES` | unset (trust nobody) | comma-separated CIDRs or addresses of reverse proxies whose `X-Forwarded-For` is believed; see "Client address" below |

The publisher settings fail closed: `github` with any App setting missing, App settings with
another publisher, `local` without `IACP_LOCAL_GIT_REPO`, or an unknown value stop the API at startup.

## Run

```bash
docker compose up -d postgres            # from the repo root
cd backend && IACP_AUTH_MODE=dev go run ./cmd/api

curl -s localhost:8080/healthz           # liveness
curl -s localhost:8080/readyz            # database reachable
curl -s localhost:8080/metrics           # Prometheus text format (loopback only without a token)
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
| GET | `/metrics` | Prometheus exposition; loopback peers without proxy headers, or `Authorization: Bearer $IACP_METRICS_TOKEN` (`403 metrics_forbidden` otherwise) |
| GET | `/v1/internal/drift/stacks?limit=` | machine auth (`drift` class: `drift.yml` on main, `schedule`/`workflow_dispatch`); the stacks to check: settled, not locked, least recently checked first, at most 256 |
| POST | `/v1/internal/drift` | machine auth (`drift` class); one drift check of one stack from `drift.yml`: stores a `drift_reports` row, sets `stacks.health`, audits it; `409 stack_not_checkable` for a stack that is mid-change, `403 run_mismatch` / `workflow_not_allowed`; a replay of the same run returns the stored report (`replayed: true`) |
| GET | `/v1/internal/budget-context?stack_path=` | machine auth (`budget_context` class); conftest `--data` document for the stack (below) |
| GET, PUT | `/v1/teams/{slug}/budgets[/{month}]` | admin only: `GET` lists, `PUT /v1/teams/{slug}/budgets/{month}` sets a monthly budget (below) |
| POST | `/v1/internal/budget-actuals`, `/v1/internal/budget-committed` | shared internal token only (`budget_actuals` class, no workflow); record `actual_usd` and a stack's committed monthly cost |
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
| GET | `/v1/internal/requests/by-path?stack_path=&head_sha=` | machine auth (`request_lookup` class: `apply.yml` on main, `push`/`workflow_dispatch`); the request holding the stack lock with `fencing_token` and `approved_plan_hash`; `404 stack_not_found` / `no_active_request`, `409 request_not_approved` / `merge_not_recorded` (retryable) / `merge_commit_mismatch` (docs/03 §6.9) |
| POST | `/v1/requests` | `kind` create, update or destroy (update and destroy name the stack by `stack_id`; destroy also needs `confirm_stack_name` and a `justification`); upgrade, reconcile and rollback are `422 unsupported_kind`; developer role in the stack's team (maintainer for a production destroy; `403` otherwise); `409 stack_busy` / `stack_destroyed` / `destroy_pending` when the stack does not take the kind; `404` for a stack in another team; an update that drops resources needs `allow_removals` (`422 resources_removed`); requires `Idempotency-Key` (remembered 24 h); `202`; replays set `Idempotent-Replayed: true` |
| GET | `/v1/requests?team=&status=&kind=&mine=true&limit=&cursor=` | keyset pagination, `next_cursor` |
| GET | `/v1/requests/{id}` | includes the normalised `spec`, `allowed_events`, `github {branch, pr_number, pr_url}` once published and `failure_reason` |
| GET | `/v1/requests/{id}/events` | Server-Sent Events: `snapshot`, then `status` per change (event id = audit seq, resume with `Last-Event-ID`), `: heartbeat` every 15 s, `end` when terminal (also on a resume at the terminal seq), closed after 5 min or on server shutdown; ended when its session is revoked; at most 5 streams per user and 500 in total (`429`); `404` for a caller with no role in the team |
| GET | `/v1/internal/ttl/due` | machine auth (`ttl` class: `ttl-reaper.yml` on main, `schedule`/`workflow_dispatch`); `{"warnings":[…], "teardowns":[…]}` of stacks due now (never production) |
| POST | `/v1/internal/ttl/{id}/warn` | machine auth (`ttl`); records the 24 h warning once (`200 {"warned":true}`, or `false` with a `reason` when not due) |
| POST | `/v1/internal/ttl/{id}/teardown` | machine auth (`ttl`); opens a destroy request as the platform bot for an expired stack: `202 {"status":"created","request_id","short_id"}`, `200 {"status":"already_pending"}`, `409 ttl_not_due` / `not_ephemeral` / `production_not_ephemeral` / `stack_busy` |
| POST | `/v1/stacks/{id}/ttl/extend` | `{"hours"}`; developer role in the stack's team (`stack.extend_ttl`); `200 {"stack_id","expires_at","extended_hours","remaining_extension_hours"}`; `409 ttl_extension_cap` / `ttl_environment_max` / `destroy_pending` / `not_ephemeral`; `404` for a stack in another team |
| GET | `/v1/stacks?team=&env=&status=&limit=&cursor=` | stacks of the caller's teams (platform admins: all), newest first, keyset pagination |
| GET | `/v1/stacks/{id}` | `status`, `health`, `outputs`, `latest_request` and links; `404` when the caller may not see it |
| GET | `/v1/stacks/{id}/drift?limit=` | drift history of a stack the caller may see, newest first; viewer |
| POST | `/v1/drift/{id}/resolve` | `{"action":"reconcile"\|"codify"\|"ignore","reason"}`: reconcile opens an update request for the stack through `requests.Service` (developer), codify records the decision only (developer; the update is manual follow-up), ignore needs a `reason` and the maintainer role; audited as `drift.resolved` |
| POST | `/v1/webhooks/github` | GitHub App webhook, authenticated by `X-Hub-Signature-256` only; `202 {"accepted","duplicate","enqueued"}`, `401 invalid_signature` / `webhook_not_configured`, `400` for a bad delivery id, event header or payload |
| GET | `/v1/audit/verify?from_seq=&to_seq=` | recomputes the hash chain, at most 10,000 events per call (`has_more`) |

Errors are RFC 9457 `application/problem+json` with `code`, `request_id` and `errors[].pointer`.
`X-Request-Id` is echoed (or generated) and recorded as the audit `correlation_id`.

Authentication goes through `httpapi.Authenticator`, and it fails closed:

- User routes (`/v1/...`) use the authenticator selected by `IACP_AUTH_MODE`: `dev`, or `github`
  (`httpapi.SessionAuthenticator`: the `iacp_session` cookie plus `X-CSRF-Token` on unsafe methods).
  See "GitHub login, sessions and RBAC" below.
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
  pipeline workflows; the budget context `pr-validate.yml` and `apply.yml` via `pull_request`,
  `push` or `workflow_dispatch`; the TTL routes (`/v1/internal/ttl/*`) only `ttl-reaper.yml` on
  main and the drift routes only `drift.yml` on main, both via `schedule` or `workflow_dispatch`.
  A pull-request token therefore only reaches `/v1/runs` and the read-only budget context.
- `POST /v1/runs` then checks provenance per pipeline: the report's `run_id`/`run_attempt` and
  `head_sha` must be the token's run and `sha` (`403 run_mismatch`); `validate` must come from
  `pr-validate.yml` on `pull_request`, `apply` from `apply.yml` on main via `push` /
  `workflow_dispatch`, `drift` and `ttl_destroy` from `drift.yml` / `ttl-reaper.yml` on main via
  `schedule` / `workflow_dispatch`. `pull_request_target` and `workflow_run` never qualify.
- Replay (`auth.ReplayGuard`, after every check above): a token's `jti` is claimed until `exp` plus
  the leeway, and a second use in that window gets the generic 401. Workflows must request a
  fresh OIDC token for every call. The default store is the `oidc_jti` table, whose primary key
  makes the claim atomic across replicas; `IACP_OIDC_REPLAY_STORE=memory` keeps a bounded cache
  in the process instead (expired ids dropped first, then the soonest-expiring id evicted when
  full), which only protects one instance. A claim only deletes an expired row for its own `jti`;
  a background sweeper (`auth.RunReplaySweeper`, every 5 min) purges the rest in batches of
  1,000 through a `ctid` subquery. A store outage still refuses the token, but as
  `503 replay_store_unavailable` with `Retry-After`, logged as `authentication dependency
  unavailable`, so it is distinguishable from a replay (401).
- Calls with the shared token are not workflow-scoped and skip these OIDC checks.
- 401 bodies are generic; the reason is only logged.

GitHub OAuth sessions (WBS 4.4) plug in at the same seam without handler changes.

## GitHub login, sessions and RBAC (WBS 4.4, 8.6 core)

Packages: `internal/oauth` (flow, PKCE, redirect policy, `GitHubIdentity` port and the
`GitHubClient` adapter), `internal/session` (hashed sessions, CSRF token), `internal/teamsync`
(role mapping and the login-time sync), `internal/rbac` (the pure permission matrix),
`httpapi` (`SessionAuthenticator`, `AuthHandler`, `PolicyTable`, denial auditing).

| Method | Path | Notes |
|--------|------|-------|
| GET | `/v1/auth/login?return_to=` | `302` to GitHub; sets `iacp_oauth_state`. `github` mode only |
| GET | `/v1/auth/callback` | finishes the login and sets `iacp_session`; errors redirect with `?login_error=` |
| POST | `/v1/auth/logout` | session + `X-CSRF-Token`; `204` |
| GET | `/v1/me` | user, teams and roles, `csrf_token`; also in `dev` mode |

- The GitHub adapter talks only to `github.com` and `api.github.com` (https), never builds a URL
  from response data, refuses redirects to other hosts (and cross-host redirects of any kind),
  has a 10 s timeout, caps responses at 1 MiB, and keeps the code, token and client secret out
  of errors and logs. Tests run it against an `httptest` server through `WithEndpoints`.
- The session id is 256 random bits; only its SHA-256 is stored. `iacp_session` is HttpOnly,
  SameSite=Lax, host-only and Secure unless the callback URL is http on loopback.
- `dev` mode is unchanged: no cookie or token, and the fixed user passes the route policies as a
  platform admin (its request-create role still comes from `memberships`).
- Route policies live in `httpapi.DefaultPolicies`. A user route without an entry is refused
  with 403 and fails `TestEveryUserRouteHasAPolicy` (a `chi.Walk` over the router), so new
  routes must add themselves (for example `GET /v1/stacks` and `GET /v1/requests/{id}/events`
  use `ScopeAnyTeam` with `ActionRead`). Probes, the public login routes, machine routes and
  webhooks are exempt.
- Read scoping: request list and get are limited to the caller's teams (`identity.Visibility`,
  `requests.Access`); other read routes should reuse `UserStore.VisibleTeams` and
  `Visibility.SQLArgs`. A request outside the caller's teams is 404.
- Cookies are `__Host-` prefixed over https. `POST /v1/auth/logout` is public: with a dead
  session it clears the cookie and answers 204. Login starts are limited to 30 per minute per
  remote address and `rbac.denied` audit events to 20 per user per minute (per process).
- Team mapping, last-admin protection, and the OAuth app settings are in docs/07 section 7.
  Registering the OAuth app is WBS 2.5 and needs the repository owner.
- Not built: periodic or webhook team sync (needs an org credential, WBS 4.8; roles go stale
  for up to the session lifetime, see docs/07), `/auth/refresh`, personal API tokens, a
  trusted-proxy setting for per-client rate limiting behind the portal.

## Metrics

`/metrics` is served on the API listener from a private registry (`observability.Metrics`):

- `iacp_http_requests_total{method,route,status}` and `iacp_http_request_duration_seconds{method,route}`.
  `route` is the chi route pattern read after routing (`/v1/requests/{id}`), never the raw path;
  unmatched paths share `route="unmatched"`, so scanners cannot grow the series count.
- `iacp_db_pool_connections{state=acquired|idle|total}` and `iacp_db_pool_max_connections`, read
  from `pgxpool.Stat` at scrape time; plus the standard Go runtime and process collectors.

Access: the endpoint shares the public listener, so it is closed by default. A direct loopback
peer (a sidecar scraper in the same task or pod) is allowed; any request carrying `Forwarded`,
`X-Forwarded-For` or `X-Real-Ip` is never treated as loopback, because a local reverse proxy would
otherwise make every client look local. Other scrapers send `IACP_METRICS_TOKEN`, a separate secret
from `IACP_INTERNAL_TOKEN` (which can take stack locks). A wrong token is refused even from loopback.
Request methods outside GET, HEAD, POST, PUT, PATCH, DELETE and OPTIONS are labelled `OTHER`
(`observability.MethodLabel`), since net/http accepts any token as a method.

**Deployment assumption: loopback means "this host".** The loopback rule is only safe when no
same-host proxy relays outside traffic to the API from 127.0.0.1 without adding a forwarding
header. A service-mesh or ECS Service Connect sidecar, or a local nginx with `proxy_pass` and no
`X-Forwarded-For`, would make every external caller look local. In such a deployment set
`IACP_METRICS_TOKEN`, make the proxy add `X-Forwarded-For` (which disables the loopback rule), or
keep `/metrics` off the proxied listener. A separate internal metrics listener is the long-term fix.

## Budget context (docs/07 §6.1)

`GET /v1/internal/budget-context?stack_path=environments/<env>/<team>/<stack>` (alias `stack=`)
returns `{"iacp": {"context": {…}}}`, ready for `conftest --data`, where the policies read it as
`data.iacp.context`: `environment`, `team`, `stack`, `stack_path`, `allowed_regions` (from
`environments`), `budget` (`month`, `limit_usd`, `spent_usd`, `actual_usd`, `hard_block`, `scope`; the
environment-specific row wins over a team-wide one; `null` when no budget is set), `budget_override`
(`granted_by`, `reason`, `expires_at`, or `null`), `exceptions` (always `[]` until policy exceptions are stored),
`yanked_module_versions` (`module`, `version`, `tag`; every version of a yanked module too) and
`now`. Everything is derived from the path, so it works for a stack a PR is about to create.
`422 invalid_stack_path`, `404 environment_not_found`.

## TTL and ephemeral stacks (docs/06 §6, WBS 8.4)

`internal/ttl` holds the rules and the use cases; `requests` opens the destroy request.

- **Model.** A create with `"ephemeral": {"ttl_hours": N}` (0 or omitted: `IACP_TTL_DEFAULT_HOURS`) on a
  non-production environment stores `stacks.is_ephemeral`, `ttl_hours` and `expires_at`. Production never
  allows it: `ttl.EnvRule.AllowsEphemeral` is false for it, and migration `0012` has a trigger and a CHECK
  that keep `environments.allow_ephemeral` false there. The spec stored on the request records the TTL
  actually applied, so a retried publish renders the same thing.
- **Pure core** (`ttl.go`, no I/O): `Policy.Resolve`, `ExpiresAt`, `PhaseAt` (none, warn from 24 h before,
  teardown from expiry), `Eligible`, `Policy.Extend`, `Classify`, `WarningComment`.
- **Services** (`ttl.Service`, ports `Repository`/`Store`, PostgreSQL adapter): `Extend` (role check in
  the stack's team, cap, audit `ttl.extended`, clears the warning), `Due`, `Warn` (audit `ttl.warned`,
  `ttl_warned_at`, `ttl.warning` outbox job when comments are enabled), `Teardown`.
- **Backoff** (`ttl.Policy.Backoff`, pure): `OpenTTLDestroy` and the due list skip a stack whose last TTL destroy ended unsuccessfully within the backoff window (`409 ttl_backoff`) and block it after the attempt limit (`409 ttl_blocked`, audit `ttl.blocked` once per expiry). Blocked stacks are returned in a separate `blocked` list by `GET /v1/internal/ttl/due`, never among the teardowns, and candidates are paged (200 per page, 5000 scanned at most) before the filter, so neither the tool's per-run cap nor the row limit can be used up by stacks that cannot be torn down.
- **Teardown** calls `requests.Service.OpenTTLDestroy` as the `iacp-bot` system user. In one transaction,
  under the stack row lock, it re-checks ephemeral, not production and past `expires_at`, skips a stack with a
  destroy already open, refuses a busy stack, then records the destroy request (justification `ttl expired`,
  stack lock or queue, publish job) and the audit event `ttl.expired`.
- **PR comment.** `publish.Commenter` is an optional publisher capability; the GitHub App implements it
  (`POST /issues/{n}/comments`, sent once), the local publisher does not, and then warnings are audit-only
  (logged at startup). `ttl.WarningNotifier` is the outbox handler for `ttl.warning`.
- The caller is the hourly `.github/workflows/ttl-reaper.yml` through `tools/ttl_reaper`; there is no
  in-process scheduler, so a stopped API simply catches up on the next run.

## Budgets (docs/06 §10, docs/07 §6.7)

`PUT /v1/teams/{slug}/budgets/{month}` (`month` = `YYYY-MM`) and `GET /v1/teams/{slug}/budgets[/{month}]`
are admin only. `DefaultPolicies` requires the admin role in some team (`budget.set`) and
`budgets.AdminService` requires it in the team named in the path (platform admins count
everywhere); a route with no policy entry is a `403`. `budgets.NewWrite` validates everything in
one pure pass and reports every problem as a field error: `limit_usd` 0 to 1,000,000 with at most
two decimals, `currency` empty or `USD`, `environment` one of the three or omitted (team-wide),
`alert_thresholds` 1 to 200 (default 50, 80, 100), an optional `single_change_cap_usd` (none unless set), and an
`override` (`reason` 20 to 500 characters, `expires_at` within 31 days, a required `max_delta_usd`
bound) that waives a hard limit or the cap outside production only and is refused on a
production-scoped budget. The budget context also sums `stacks.committed_monthly_usd` over the
team's other active stacks (`current_committed_usd`, `Repository.CommittedUSD`); nothing records it
yet (ADR-0014), so until then the limit is effectively per stack. Writes and their
audit events (`budget.set`, `budget.override.granted`, `budget.override.revoked`) share one
transaction. `POST /v1/internal/budget-actuals` takes `{team, environment?, month, actual_usd}` and
accepts only the shared internal token: no OIDC workflow is in its route class, so a pull-request
token cannot move the spend the budget gate reads. Cost Explorer is not wired (not available under
LocalStack), so nothing calls it yet. Field-level API: [03 §6.7](../docs/03-data-model-and-api.md#67-budgets-and-exceptions).

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
- Release checks the holder and token (`409 stale_fencing_token` otherwise; `200` with
  `already_released: true` when the request is already terminal, so apply.yml's own release step
  stays green after the API released the lease with the final transition), deletes the lease and,
  in the same transaction, grants the oldest waiter a new token and re-points the rest of the queue
  at it. `lock.acquired`, `lock.queued` and `lock.released` are written to the audit log; a waiter
  polling again behind the same holder writes nothing.
- `POST /v1/runs` checks `fencing_token` against the current lease (`FOR SHARE`) for `validate`,
  `apply` and `ttl_destroy` reports, so a late callback from a superseded run is rejected. It
  locks the `stacks` row (`FOR UPDATE`) first, the same order as lock operations, so reports
  cannot deadlock with release (`TestRecordAndReleaseDoNotDeadlock`) and the reports of one stack
  apply one at a time. A report for a stage the run already recorded replaces that entry, so
  retries are idempotent; a run keeps at most 32 stages.

## Apply reports drive request and stack status (WBS 5.3)

`apply` reports (`apply.yml`: stages `plan`, `apply` or `destroy`, `smoke`, `summary`, `release`)
now move the request and its stack in the report's transaction. `runs.Progress` (pure) turns the
run's stages into an `requests.ApplyProgress`; `requests.DecideApply` (pure) turns that and the
request and stack status into events and a stack change; `requests.Service.ApplyIn` applies them
with `Request.Fire`, audits each (`request.transitioned`, `stack.status_changed`,
`stack.destroyed`) and, for a terminal request, releases the lock and promotes the next waiter
through the same `commit` path as every other terminal transition (the promotion hook schedules
its publish job).

| Report | Effect |
|--------|--------|
| first accepted apply report of an `approved` request | `approved → applying` (`apply_started`); stack → `provisioning` / `updating` / `destroying` |
| `summary` passed, or failed only because `smoke` failed (mutating stage passed) | run `succeeded`; request `applied`; stack `active` (`destroyed` + `destroyed_at` after a destroy), outputs, `last_applied_sha`, `health` (`healthy`, `degraded`, `unknown`) |
| `plan`, `apply`/`destroy` or `summary` failed or cancelled | run `failed`/`cancelled`; request `failed` with `failure_reason` (`plan_failed`, `apply_failed`, `apply_incomplete`, `apply_cancelled`) and `failure_detail`; stack `failed` |
| `smoke` failed | does not finish the run; the summary decides (above) |
| `release` failed | stored in the run, audited as `request.lock_release_failed` with the lock's current state; never changes the request |
| `validate` pipeline | stored only; a passed validation is not an approval |

A request that is not `approved`, `applying` or terminal answers `409 request_not_applying` and
nothing is recorded. For a terminal request the API has already released the lease, so a report
carrying the request's own token (a replayed `summary`, a failed `release`) is accepted only for a
run already recorded for that request, audited and ignored (`request.run_report_ignored`), without
rewriting the finished run's stages (the advisory `release` stage excepted); any other token, or a
new run id or attempt, is `409 stale_fencing_token`. All
existing checks stay: fencing token against the current lease, `head_sha` equal to the token's
`sha`, per-pipeline workflow file and event allow-lists, and run id and attempt equality.

Known limits: automatic retry of a transient failure (`apply_retry`, at most 2) needs a re-dispatch
the platform does not have yet, so a transient failure fails the request like any other; the
`ttl_destroy` and `drift` pipelines are stored but do not move requests or stacks yet (WBS 7.x,
8.x); a run whose workflow dies before sending `summary` leaves the request `applying` and holding the lock; nothing acts on lease expiry yet, so an operator must intervene (follow-up, WBS 8.7).

## Client address and trusted proxies

Rate limiting of `GET /v1/auth/login` and the `actor_ip` of audit events use the client address.
By default that is the TCP peer. With `IACP_TRUSTED_PROXIES` set, and only when the immediate peer
is inside one of the listed CIDRs, the address is the right-most `X-Forwarded-For` entry that is not
itself a trusted proxy (each hop appends the address it received from, so entries further left are
whatever the client claimed). A header that is missing, made only of trusted hops or contains a
non-address entry falls back to the peer. Without the setting the portal proxy's address is the key,
so the limiter is effectively one global bucket. `GET /metrics` keeps its own, stricter rule
(`X-Forwarded-For` disables the loopback exemption).

Portal side (not part of this change): `portal/`'s proxy must forward the browser's address as
`X-Forwarded-For` (append the connecting address to any incoming value rather than replacing it)
for the login route and the audited routes, and its header allow-list must let that header through.
Until then the limiter and the audit trail see the proxy.

## Retention

A sweeper (`internal/retention`, started in `main` like the OIDC replay sweeper) runs hourly and
deletes webhook delivery ids and processed outbox jobs older than `IACP_QUEUE_RETENTION`, in
batches of 1,000. `iacp_app` still cannot `DELETE` from `outbox`: the purge goes through the
`purge_processed_outbox` function (migration `0010`, `SECURITY DEFINER`, only rows with
`processed_at` set, cutoff clamped in SQL to at least one hour old), so a compromised API cannot erase pending or dead jobs.

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
  `DispatchWorkflow`, `ClosePullRequest`, `DeleteBranch`, `PullRequestHead`. Commits replace the
  stack root as a whole (`Commit.Root`): files under it that a render no longer produces are
  deleted, except `publish.KeepFiles` that the generator does not render but the stack commits
  (`.terraform.lock.hcl`, which CI checks with `terraform init -lockfile=readonly`). Every call except
  dispatch is safe to retry: the branch is created or force-moved, and opening a PR whose head
  already has one returns it.
- **`githubapp`**: RS256 App JWT (iat −60 s, exp +9 min) exchanged for an installation token
  scoped to `IACP_GITHUB_REPOSITORY` with `contents`, `pull_requests` and `actions` write only,
  cached until 5 minutes before expiry and refreshed once on a 401. Files are committed atomically
  with the git data API (blobs, one tree on the base tree with null-sha deletions, a commit, then
  the ref; the ref is force-moved only when GitHub says it already exists). Stale files are found by
  walking down to the stack root and listing only that subtree recursively, so the monorepo size
  does not matter; a truncated subtree listing fails the commit.
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
  outcome is in the audit payload). Before withdrawing, the request is re-read: if a PR is
  recorded after all (for example a lost reply from `RecordPublished`), nothing is touched.
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
  head (published commit, then each accepted push). A push (`new_commit`, the only event whose
  replay could clear an approval) must start from the known head; otherwise the Applier reads the
  real head through `PullRequestHead` and, if it moved, applies one push from the known head to
  it, so a lost or reordered `synchronize` cannot leave the head behind. A replayed old push finds
  the head unchanged and is only audited as `stale_head`. Closes and merges are not head-checked:
  they are status-guarded and `rejected` is terminal, and dropping them would leave the request
  holding its stack lock. Events not allowed in the current status are audited as
  `request.github_event_ignored`. An event that arrives before the PR is recorded is retried
  through the outbox for about 13.5 minutes (`outbox.RetryLonger`, 10 attempts) instead of being
  dropped.
- **Queue**: the `outbox` table (migration 0008) and an in-process worker goroutine
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
