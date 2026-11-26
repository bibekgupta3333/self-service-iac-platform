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
  requests/              state machine, Request entity, spec validation, RequestService, Postgres repo
  locks/                 per-stack platform lock: pure Decide/NextInQueue/CheckToken, Service, Postgres repo
  auth/                  GitHub Actions OIDC: claim allow-lists (pure), JWKSCache, Verifier, jti ReplayGuard
  budgets/               policy context for conftest: pure ParseStackPath / BuildContext, Service, Postgres repo
  runs/                  POST /v1/runs reports: pure Validate/CheckProvenance/RunStatus, Service, Postgres repo
  generator/             pure Terraform renderer + BackendStrategy / ProviderStrategy, golden files
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

### Database roles

The API never serves requests as the schema owner:

- `IACP_MIGRATE_DATABASE_URL` (owner) applies migrations and, when `IACP_APP_DB_PASSWORD` is set,
  gives `iacp_app` a login with that password. The connection is closed before serving.
- `IACP_DATABASE_URL` (`iacp_app`) serves requests. It cannot update or delete audit rows,
  disable triggers, set `session_replication_role`, or run DDL. If it points at an owner or
  superuser, the API logs a warning at startup.

In `IACP_AUTH_MODE=dev` the app password defaults to `iacp-app-local-only`, which matches the
default runtime URL, so a local `go run` works against the compose Postgres with no extra setup.

Tables for later milestones (plan summaries, costs, drift, budget overrides, policy exceptions, stack
dependencies, webhook deliveries, outbox) arrive with their features.

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
| GET | `/v1/internal/budget-context?stack_path=` | machine auth (`budget_context` class); conftest `--data` document for the stack (below) |
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
| GET | `/v1/requests/{id}` | includes the normalised `spec` and `allowed_events` |
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
  pipeline workflows; the budget context `pr-validate.yml` and `apply.yml` via `pull_request`,
  `push` or `workflow_dispatch`. A pull-request token therefore only reaches `/v1/runs` and the
  read-only budget context.
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
  full), which only protects one instance. Store errors fail closed (401).
- Calls with the shared token are not workflow-scoped and skip these OIDC checks.
- 401 bodies are generic; the reason is only logged.

GitHub OAuth sessions (WBS 4.4) plug in at the same seam without handler changes.

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

## Budget context (docs/07 §6.1)

`GET /v1/internal/budget-context?stack_path=environments/<env>/<team>/<stack>` (alias `stack=`)
returns `{"iacp": {"context": {…}}}`, ready for `conftest --data`, where the policies read it as
`data.iacp.context`: `environment`, `team`, `stack`, `stack_path`, `allowed_regions` (from
`environments`), `budget` (`month`, `limit_usd`, `spent_usd`, `hard_block`, `scope`; the
environment-specific row wins over a team-wide one; `null` when no budget is set), `budget_override`
(always `null` until WBS 8.5), `exceptions` (always `[]` until policy exceptions are stored),
`yanked_module_versions` (`module`, `version`, `tag`; every version of a yanked module too) and
`now`. Everything is derived from the path, so it works for a stack a PR is about to create.
`422 invalid_stack_path`, `404 environment_not_found`.

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
