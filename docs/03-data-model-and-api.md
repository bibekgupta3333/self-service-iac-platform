# 03 — Data Model and API

> Purpose: specify the platform's PostgreSQL schema, the request and stack state machines, the REST API contract, and the Go package layout of `backend/`.

## Contents

1. [Entity overview](#1-entity-overview)
2. [PostgreSQL DDL](#2-postgresql-ddl)
3. [Hash-chained audit log](#3-hash-chained-audit-log)
4. [State machines](#4-state-machines)
5. [API conventions](#5-api-conventions)
6. [REST API reference](#6-rest-api-reference)
7. [Error model](#7-error-model)
8. [Go package layout](#8-go-package-layout)

Related: [01-system-architecture.md](01-system-architecture.md) · [04-module-registry.md](04-module-registry.md) · [05-automation-pipeline.md](05-automation-pipeline.md) · [06-lifecycle-management.md](06-lifecycle-management.md) · [07-security-and-policy.md](07-security-and-policy.md)

---

## 1. Entity overview

![Data model](diagrams/images/data-model.png)

| Table | Purpose | Key relationships |
|---|---|---|
| `teams` | Tenant unit; mirrors a GitHub team. | has many memberships, stacks, budgets |
| `users` | GitHub-authenticated humans (and the `iacp-bot` system user). | memberships |
| `memberships` | user × team × role (`viewer`/`developer`/`maintainer`/`admin`). | |
| `modules` | Catalog package (e.g. `s3`). | has many module_versions |
| `module_versions` | Immutable released version with manifest, input schema, outputs, git tag/SHA. | referenced by stack_resources |
| `environments` | `development`/`staging`/`production` (+ AWS account, region, approval rules). | stacks |
| `stacks` | One Terraform root module at `environments/<env>/<team>/<stack>/`. | belongs to team, environment; has many stack_resources, requests |
| `stack_resources` | Module instance inside a stack (name, module version, inputs). | stack, module_version |
| `stack_dependencies` | DAG edges between stacks (output injection). | stack → stack |
| `requests` | Change request: create/update/upgrade/destroy/reconcile. | stack, user, pipeline_runs |
| `pipeline_runs` | One GitHub Actions workflow run (validate/apply/drift/ttl). | request (nullable for drift), stack |
| `plan_summaries` | Parsed plan: add/change/destroy counts, resource changes, plan hash, policy results. | pipeline_run |
| `cost_estimates` | Infracost result: monthly before/after/delta. | pipeline_run |
| `drift_reports` | Nightly drift detection result per stack. | stack, pipeline_run |
| `budgets` | Team monthly budget (`limit_usd`, `spent_usd`, `override_by`). | team, users (override_by) |
| `budget_overrides` | Admin override for a specific request (max delta, reason, expiry). | budget, request |
| `policy_exceptions` | Time-boxed waiver of a policy rule for a stack/team. | team, stack |
| `locks` | Platform-level per-stack mutation lock with fencing token. | stack, request |
| `audit_events` | Append-only, hash-chained log of every action. | actor, subject |
| `webhook_deliveries` | De-duplication of GitHub webhook deliveries. | |
| `idempotency_keys` | Stored responses for idempotent POSTs. | user |
| `outbox` | Transactional outbox for side effects published to SQS. | request / stack |

## 2. PostgreSQL DDL

Migrations live in `backend/migrations/` (goose, `NNNN_name.sql`); queries in `backend/queries/*.sql` compiled with sqlc. PostgreSQL 16.

```sql
-- 0001_init.sql
-- gen_random_uuid() is built in (PostgreSQL 13+); audit hashes are computed in Go, so no pgcrypto.
CREATE EXTENSION IF NOT EXISTS citext;

-- ---------- enums ----------
CREATE TYPE team_role         AS ENUM ('viewer', 'developer', 'maintainer', 'admin');
CREATE TYPE env_name          AS ENUM ('development', 'staging', 'production');
CREATE TYPE module_status     AS ENUM ('active', 'deprecated', 'yanked');
CREATE TYPE stack_status      AS ENUM ('provisioning', 'active', 'drifted', 'updating', 'destroying', 'destroyed', 'failed');
CREATE TYPE request_kind      AS ENUM ('create', 'update', 'upgrade', 'destroy', 'reconcile', 'rollback');
CREATE TYPE request_status    AS ENUM ('draft', 'pr_open', 'validating', 'plan_ready', 'approved', 'applying',
                                       'applied', 'failed', 'rejected', 'cancelled');
CREATE TYPE pipeline_kind     AS ENUM ('validate', 'apply', 'drift', 'ttl_destroy', 'module_release');
CREATE TYPE pipeline_status   AS ENUM ('queued', 'in_progress', 'succeeded', 'failed', 'cancelled', 'timed_out');
CREATE TYPE drift_status      AS ENUM ('clean', 'drifted', 'error');

-- ---------- identity ----------
CREATE TABLE teams (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug             text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9-]{1,38}$'),
    name             text        NOT NULL,
    github_team_id   bigint      NOT NULL UNIQUE,
    github_team_slug text        NOT NULL,
    cost_center      text        NOT NULL,
    slack_channel    text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    github_user_id  bigint      NOT NULL UNIQUE,
    github_login    citext      NOT NULL UNIQUE,
    email           citext,
    display_name    text,
    is_platform_admin boolean   NOT NULL DEFAULT false,
    is_system       boolean     NOT NULL DEFAULT false,
    last_login_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    disabled_at     timestamptz
);

CREATE TABLE memberships (
    team_id     uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role        team_role   NOT NULL,
    source      text        NOT NULL DEFAULT 'github_sync' CHECK (source IN ('github_sync', 'manual')),
    synced_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id);

-- ---------- catalog ----------
CREATE TABLE modules (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text        NOT NULL UNIQUE CHECK (name ~ '^[a-z][a-z0-9-]{1,40}$'),
    display_name   text        NOT NULL,
    description    text        NOT NULL,
    category       text        NOT NULL,                 -- storage, messaging, compute, data, network, observability
    owners         text[]      NOT NULL,                 -- GitHub team slugs
    repo_path      text        NOT NULL,                 -- modules/s3
    status         module_status NOT NULL DEFAULT 'active',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE module_versions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    module_id         uuid        NOT NULL REFERENCES modules(id) ON DELETE RESTRICT,
    version           text        NOT NULL CHECK (version ~ '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$'),
    major             int         NOT NULL,
    minor             int         NOT NULL,
    patch             int         NOT NULL,
    git_tag           text        NOT NULL UNIQUE,       -- modules/s3/v1.2.0
    git_sha           char(40)    NOT NULL,
    manifest          jsonb       NOT NULL,              -- full module.yaml as JSON
    inputs_schema     jsonb       NOT NULL,              -- JSON Schema 2020-12
    outputs           jsonb       NOT NULL,              -- [{name, description, sensitive}]
    cost_hints        jsonb,
    tf_version_range  text        NOT NULL,              -- ">= 1.10, < 2.0"
    aws_provider_range text       NOT NULL,              -- "~> 5.80"
    changelog_md      text,
    status            module_status NOT NULL DEFAULT 'active',
    deprecation_note  text,
    released_by       text        NOT NULL,              -- github login of tagger
    released_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (module_id, version),
    CHECK (status <> 'deprecated' OR deprecation_note IS NOT NULL)
);
CREATE INDEX module_versions_semver_idx ON module_versions (module_id, major DESC, minor DESC, patch DESC);

CREATE TABLE environments (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name               env_name    NOT NULL UNIQUE,
    aws_account_id     char(12)    NOT NULL UNIQUE,
    default_region     text        NOT NULL DEFAULT 'us-east-1',
    allowed_regions    text[]      NOT NULL,
    requires_approval  boolean     NOT NULL DEFAULT false,
    min_approver_role  team_role   NOT NULL DEFAULT 'developer',
    allow_ephemeral    boolean     NOT NULL DEFAULT true,
    max_ttl_hours      int         CHECK (max_ttl_hours IS NULL OR max_ttl_hours > 0)
);

-- ---------- stacks ----------
CREATE TABLE stacks (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id          uuid        NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    environment_id   uuid        NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
    name             text        NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{1,40}$'),
    path             text        NOT NULL UNIQUE,        -- environments/development/payments/order-events
    state_key        text        NOT NULL UNIQUE,        -- development/payments/order-events/terraform.tfstate
    region           text        NOT NULL,
    status           stack_status NOT NULL DEFAULT 'provisioning',
    is_ephemeral     boolean     NOT NULL DEFAULT false,
    expires_at       timestamptz,
    ttl_warned_at    timestamptz,
    outputs          jsonb       NOT NULL DEFAULT '{}'::jsonb,   -- non-sensitive outputs only
    last_applied_sha char(40),
    last_applied_request_id uuid,
    last_drift_check_at timestamptz,
    tags             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_by       uuid        NOT NULL REFERENCES users(id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    destroyed_at     timestamptz,
    UNIQUE (environment_id, team_id, name),
    CHECK (NOT is_ephemeral OR expires_at IS NOT NULL),
    CHECK (status <> 'destroyed' OR destroyed_at IS NOT NULL)
);
CREATE INDEX stacks_team_status_idx  ON stacks (team_id, status);
CREATE INDEX stacks_expiry_idx       ON stacks (expires_at) WHERE is_ephemeral AND status NOT IN ('destroying', 'destroyed');
CREATE INDEX stacks_drift_due_idx    ON stacks (last_drift_check_at NULLS FIRST) WHERE status IN ('active', 'drifted');

CREATE TABLE stack_resources (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stack_id           uuid        NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    instance_name      text        NOT NULL CHECK (instance_name ~ '^[a-z][a-z0-9_]{0,62}$'),  -- HCL identifier
    module_version_id  uuid        NOT NULL REFERENCES module_versions(id) ON DELETE RESTRICT,
    inputs             jsonb       NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    removed_at         timestamptz,
    UNIQUE (stack_id, instance_name)
);
CREATE INDEX stack_resources_module_version_idx ON stack_resources (module_version_id) WHERE removed_at IS NULL;

CREATE TABLE stack_dependencies (
    stack_id            uuid NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    depends_on_stack_id uuid NOT NULL REFERENCES stacks(id) ON DELETE RESTRICT,
    output_bindings     jsonb NOT NULL,     -- [{"from":"orders_queue.queue_arn","to":"order_processor.sqs_event_source.queue_arn"}]
    PRIMARY KEY (stack_id, depends_on_stack_id),
    CHECK (stack_id <> depends_on_stack_id)
);
CREATE INDEX stack_dependencies_upstream_idx ON stack_dependencies (depends_on_stack_id);

-- ---------- requests & pipelines ----------
CREATE TABLE requests (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    short_id           text        NOT NULL UNIQUE,              -- req-7k3p9x (branch name)
    stack_id           uuid        NOT NULL REFERENCES stacks(id) ON DELETE RESTRICT,
    kind               request_kind NOT NULL,
    status             request_status NOT NULL DEFAULT 'draft',
    spec               jsonb       NOT NULL,                     -- desired StackSpec after this request
    previous_spec      jsonb,                                    -- spec before (for diff/rollback)
    rendered_tree_sha  char(40),                                 -- git tree SHA of generated files
    justification      text,
    queued_behind      uuid        REFERENCES requests(id),
    fencing_token      bigint,
    github_branch      text,
    github_pr_number   int,
    github_pr_url      text,
    merge_commit_sha   char(40),
    approved_plan_hash char(64),                                 -- sha256 of normalised plan JSON
    approved_by        uuid        REFERENCES users(id),
    approved_at        timestamptz,
    failure_reason     text,
    failure_detail     jsonb,
    retry_count        int         NOT NULL DEFAULT 0 CHECK (retry_count BETWEEN 0 AND 5),
    created_by         uuid        NOT NULL REFERENCES users(id),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    completed_at       timestamptz,
    CHECK ((status IN ('applied', 'failed', 'rejected', 'cancelled')) = (completed_at IS NOT NULL)),
    CHECK (status NOT IN ('approved', 'applying', 'applied') OR approved_plan_hash IS NOT NULL)
);
CREATE INDEX requests_stack_created_idx ON requests (stack_id, created_at DESC);
CREATE INDEX requests_open_idx          ON requests (status) WHERE completed_at IS NULL;
CREATE INDEX requests_creator_idx       ON requests (created_by, created_at DESC);
CREATE UNIQUE INDEX requests_pr_idx     ON requests (github_pr_number) WHERE github_pr_number IS NOT NULL;

CREATE TABLE pipeline_runs (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id         uuid        REFERENCES requests(id) ON DELETE CASCADE,
    stack_id           uuid        NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,  -- part of the run key
    kind               pipeline_kind NOT NULL,
    status             pipeline_status NOT NULL DEFAULT 'queued',
    github_run_id      bigint      NOT NULL,
    github_run_attempt int         NOT NULL DEFAULT 1,
    github_job_id      bigint,
    workflow_ref       text        NOT NULL,          -- acme/self-service-iac-platform/.github/workflows/pr-validate.yml@refs/pull/812/merge
    head_sha           char(40)    NOT NULL,
    stages             jsonb       NOT NULL DEFAULT '[]'::jsonb,  -- [{"name":"checkov","status":"passed","duration_ms":8123}]
    log_url            text,
    started_at         timestamptz,
    finished_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (github_run_id, github_run_attempt, stack_id)
);
CREATE INDEX pipeline_runs_request_idx ON pipeline_runs (request_id, created_at DESC);
CREATE INDEX pipeline_runs_active_idx  ON pipeline_runs (status) WHERE status IN ('queued', 'in_progress');

CREATE TABLE plan_summaries (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_run_id   uuid        NOT NULL UNIQUE REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    plan_hash         char(64)    NOT NULL,
    add_count         int         NOT NULL CHECK (add_count >= 0),
    change_count      int         NOT NULL CHECK (change_count >= 0),
    destroy_count     int         NOT NULL CHECK (destroy_count >= 0),
    replace_count     int         NOT NULL DEFAULT 0,
    resource_changes  jsonb       NOT NULL,      -- [{"address":"module.orders_queue.aws_sqs_queue.this","actions":["create"]}]
    policy_results    jsonb       NOT NULL,      -- {"opa":{"deny":[],"warn":[]},"checkov":{"failed":0,"passed":42},"tflint":{...}}
    policy_passed     boolean     NOT NULL,
    plan_artifact_uri text        NOT NULL,      -- s3://iacp-plan-artifacts-111111111111/<run>/plan.json
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cost_estimates (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_run_id      uuid          NOT NULL UNIQUE REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    currency             char(3)       NOT NULL DEFAULT 'USD',
    monthly_before       numeric(12,2) NOT NULL,
    monthly_after        numeric(12,2) NOT NULL,
    monthly_delta        numeric(12,2) GENERATED ALWAYS AS (monthly_after - monthly_before) STORED,
    breakdown            jsonb         NOT NULL,     -- per-resource from infracost JSON
    budget_check         jsonb         NOT NULL,     -- {"budget":2000,"committed":1450.10,"remaining":549.90,"decision":"allow"}
    created_at           timestamptz   NOT NULL DEFAULT now()
);

CREATE TABLE drift_reports (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stack_id         uuid        NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    pipeline_run_id  uuid        NOT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT,  -- the drift.yml run
    status           drift_status NOT NULL,
    changed          jsonb       NOT NULL DEFAULT '[]'::jsonb,   -- [{address, actions, attributes}]
    resolved_by_request_id uuid  REFERENCES requests(id),
    resolution       text        CHECK (resolution IN ('reconciled', 'codified', 'ignored')),
    detected_at      timestamptz NOT NULL DEFAULT now(),
    resolved_at      timestamptz
);
CREATE INDEX drift_reports_stack_idx ON drift_reports (stack_id, detected_at DESC);
CREATE INDEX drift_reports_open_idx  ON drift_reports (status) WHERE status = 'drifted' AND resolved_at IS NULL;

-- ---------- budgets & exceptions ----------
CREATE TABLE budgets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id         uuid          NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    environment_id  uuid          REFERENCES environments(id),        -- NULL = all environments
    month           date          NOT NULL CHECK (date_trunc('month', month) = month),
    limit_usd       numeric(12,2) NOT NULL CHECK (limit_usd >= 0),
    spent_usd       numeric(12,2) NOT NULL DEFAULT 0,                -- committed estimates + CUR actuals, refreshed daily
    alert_thresholds int[]        NOT NULL DEFAULT '{50,80,100}',
    hard_block      boolean       NOT NULL DEFAULT true,
    override_by     uuid          REFERENCES users(id),             -- admin who granted the most recent override (detail in budget_overrides)
    created_by      uuid          NOT NULL REFERENCES users(id),
    created_at      timestamptz   NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (team_id, environment_id, month)
);

CREATE TABLE budget_overrides (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_id    uuid          NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    request_id   uuid          NOT NULL UNIQUE REFERENCES requests(id) ON DELETE CASCADE,
    max_delta_usd numeric(12,2) NOT NULL CHECK (max_delta_usd > 0),
    reason       text          NOT NULL CHECK (length(reason) >= 20),
    granted_by   uuid          NOT NULL REFERENCES users(id),
    granted_at   timestamptz   NOT NULL DEFAULT now(),
    expires_at   timestamptz   NOT NULL
);
-- On insert, the service also sets budgets.override_by = granted_by (same transaction) for quick display.

CREATE TABLE policy_exceptions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id      text        NOT NULL,                -- iacp.s3.no_public | CKV_AWS_18
    team_id      uuid        NOT NULL REFERENCES teams(id),
    stack_id     uuid        REFERENCES stacks(id),   -- NULL = team-wide (rare)
    resource_address text,                            -- optional narrowing
    reason       text        NOT NULL,
    ticket_url   text,
    requested_by uuid        NOT NULL REFERENCES users(id),
    approved_by  uuid        REFERENCES users(id),
    status       text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'revoked')),
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at <= created_at + interval '90 days'),
    CHECK (status <> 'approved' OR (approved_by IS NOT NULL AND approved_by <> requested_by))
);
CREATE INDEX policy_exceptions_active_idx ON policy_exceptions (team_id, rule_id) WHERE status = 'approved';

-- ---------- locks ----------
CREATE SEQUENCE lock_fencing_seq;
-- 1:1 with stacks: at most one lock row per stack.
CREATE TABLE locks (
    stack_id          uuid        PRIMARY KEY REFERENCES stacks(id) ON DELETE CASCADE,
    request_id        uuid        NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    holder            text        NOT NULL,            -- "request:req-7k3p9x" | "run:11873399021" (apply job holding it)
    fencing_token     bigint      NOT NULL DEFAULT nextval('lock_fencing_seq'),
    acquired_at       timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,            -- lease
    CHECK (expires_at > acquired_at)
);
CREATE INDEX locks_expiry_idx ON locks (expires_at);

-- ---------- infra tables ----------
CREATE TABLE webhook_deliveries (
    delivery_id  uuid PRIMARY KEY,           -- X-GitHub-Delivery
    event        text        NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (received_at);  -- purge > 7 days

CREATE TABLE idempotency_keys (
    user_id        uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key            text        NOT NULL CHECK (length(key) BETWEEN 8 AND 128),
    request_hash   char(64)    NOT NULL,     -- sha256(method + path + body)
    response_code  int,
    response_body  jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);       -- purge > 24 h

-- Transactional outbox: side effects (GitHub writes, notifications, schedules) written in the
-- same transaction as the state change, then published to SQS by the worker.
CREATE TABLE outbox (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic         text        NOT NULL,          -- github.open_pr, github.dispatch, notify.slack, scheduler.ttl …
    aggregate_id  uuid        NOT NULL,          -- request or stack id (ordering key)
    payload       jsonb       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    published_at  timestamptz,
    attempts      int         NOT NULL DEFAULT 0
);
CREATE INDEX outbox_unpublished_idx ON outbox (id) WHERE published_at IS NULL;
```

`request_kind` extends the four user-facing kinds (create/update/upgrade/destroy) with two platform-initiated kinds: `reconcile` (drift remediation) and `rollback` (revert PR to the last applied spec). Both follow the same state machine.

**Lock acquisition** (inside the request-creation transaction; `backend/internal/locks`):

```sql
-- 1. Serialise every lock operation on the stack.
SELECT team_id FROM stacks WHERE id = $1 FOR UPDATE;
-- 2. Read the lease and the queue; the pure locks.Decide picks granted / held / queued.
-- 3. Grant (fresh lease, or take over an expired one):
INSERT INTO locks (stack_id, request_id, holder, acquired_at, expires_at)
VALUES ($1, $2, $3, $4, $4 + interval '7 days')
ON CONFLICT (stack_id) DO UPDATE
   SET request_id    = EXCLUDED.request_id,
       holder        = EXCLUDED.holder,
       fencing_token = nextval('lock_fencing_seq'),
       acquired_at   = EXCLUDED.acquired_at,
       expires_at    = EXCLUDED.expires_at
RETURNING request_id, fencing_token;
-- and record requests.fencing_token; or, when queued, set requests.queued_behind = holder.
```

A free or expired lock goes to the oldest queued request first, so a newcomer never jumps the queue. Release deletes the row and, in the same transaction, grants the oldest waiter a new token and points the remaining waiters at it.

## 3. Hash-chained audit log

```sql
-- 0002_audit.sql
CREATE TABLE audit_events (
    seq           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id            uuid        NOT NULL UNIQUE,       -- set by the writer (part of the hash)
    occurred_at   timestamptz NOT NULL,              -- set by the writer, microsecond precision
    actor_type    text        NOT NULL CHECK (actor_type IN ('user', 'system', 'github_actions', 'github_webhook')),
    actor_id      text        NOT NULL,              -- user uuid | "worker" | workflow_ref + run_id
    actor_ip      inet,
    action        text        NOT NULL,              -- request.created, request.approved, stack.destroyed, budget.override.granted …
    subject_type  text        NOT NULL,              -- request | stack | module_version | budget | policy_exception | membership
    subject_id    text        NOT NULL,
    team_id       uuid,
    correlation_id text,                             -- HTTP request id / GitHub run id
    payload       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    prev_hash     bytea       NOT NULL,
    hash          bytea       NOT NULL UNIQUE
);
CREATE INDEX audit_events_subject_idx ON audit_events (subject_type, subject_id, seq DESC);
CREATE INDEX audit_events_team_idx    ON audit_events (team_id, seq DESC);
CREATE INDEX audit_events_action_idx  ON audit_events (action, occurred_at DESC);

-- Chain: hash = sha256(prev_hash || canonical JSON of the event), computed by the Go writer
-- (backend/internal/audit, pure function ChainHash). The database enforces linkage: inside the
-- shared advisory lock, prev_hash must equal the hash of the current head, so seq order == chain order.
CREATE OR REPLACE FUNCTION audit_events_link_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    head bytea;
BEGIN
    PERFORM pg_advisory_xact_lock(727274);   -- 'audit' chain lock, also taken by the writer
    SELECT hash INTO head FROM audit_events ORDER BY seq DESC LIMIT 1;
    IF NEW.prev_hash IS DISTINCT FROM COALESCE(head, decode(repeat('00', 32), 'hex')) THEN
        RAISE EXCEPTION 'audit_events: prev_hash does not match chain head';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER audit_events_link_trg BEFORE INSERT ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_link_check();

-- Append-only: reject UPDATE/DELETE/TRUNCATE even for the table owner path used by the app.
CREATE OR REPLACE FUNCTION audit_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END $$;
CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_immutable();

-- Privileges: application role may only INSERT and SELECT (the migration creates iacp_app
-- when the migrating role is allowed to, and skips with a notice otherwise).
REVOKE ALL ON audit_events FROM iacp_app;
GRANT INSERT, SELECT ON audit_events TO iacp_app;
```

Notes:

- **Canonical form** (`audit.CanonicalJSON`): a JSON object with keys in alphabetical order: `action`, `actor_id`, `actor_ip` (canonical address text, empty when null), `actor_type`, `correlation_id`, `id`, `occurred_at` (`YYYY-MM-DDTHH:MM:SS.ffffffZ`), `payload`, `subject_id`, `subject_type`, `team_id` (empty string when null). The payload is decoded with exact numbers and re-encoded with sorted keys, and every number is rewritten as a plain decimal that keeps all significant digits (`1e2` becomes `100`, `2.50` becomes `2.5`). The writer stores those canonical bytes; they are a fixed point of jsonb's output, so the round trip does not change the hash, the verifier can recompute it from stored or exported rows, and changing any digit (even beyond float64 precision) is detected. A known-value test pins the format; changing it invalidates existing chains.
- Hashing in Go rather than in the trigger ([ADR-0010](adr/0010-audit-hash-chain-computed-in-go.md)) keeps the chain logic a pure, unit-testable function (`ChainHash`, `VerifyChain`) while the database still guarantees linkage and immutability. `GET /v1/audit/verify` recomputes a `seq` range of at most 10,000 events per call and returns `has_more` when the chain continues.
- **Roles:** the API connects as `iacp_app` (INSERT and SELECT on `audit_events`, DML elsewhere, nothing on `goose_db_version`); migrations run as the schema owner on a separate connection (migration `0004_app_role_grants.sql`). The app role cannot disable triggers or set `session_replication_role`.
- **Verification job** (worker, nightly): streams `audit_events` in `seq` order, recomputes the chain, emits metric `the platform/AuditChainValid` (alarm on 0).
- **Anchoring:** at 00:15 UTC the worker writes `{seq, hash, occurred_at}` of the chain head plus an NDJSON export of the previous day to `s3://iacp-audit-anchor-111111111111/yyyy/mm/dd/` (Object Lock compliance, 7 y). Any later tampering with Postgres (even by a superuser disabling triggers) is detectable against the anchors.
- The single advisory lock serialises audit inserts (~ tens/sec sustained); measured headroom is ample for 500 requests/day. If needed, partition chains per team.

## 4. State machines

![Request state machine](diagrams/images/state-machine-request.png)

### 4.1 Request

```mermaid
stateDiagram-v2
    [*] --> draft: POST /requests
    draft --> pr_open: PR created (lock held)
    draft --> cancelled: user cancels / queued too long
    pr_open --> validating: pr-validate.yml started
    validating --> plan_ready: all checks passed
    validating --> failed: lint / policy / budget / plan error
    plan_ready --> approved: required reviews + plan hash pinned
    plan_ready --> validating: new commit pushed (re-plan)
    plan_ready --> rejected: PR closed unmerged / review rejected
    pr_open --> rejected: PR closed unmerged
    pr_open --> cancelled: user cancels
    validating --> validating: new commit pushed (restart)
    validating --> rejected: PR closed unmerged
    plan_ready --> cancelled: user cancels
    approved --> plan_ready: review dismissed
    approved --> validating: new commit pushed (re-plan)
    approved --> rejected: PR closed unmerged
    approved --> applying: merged → apply.yml started
    applying --> applied: apply ok + outputs reported
    applying --> failed: non-transient error / plan mismatch
    applying --> applying: transient error auto-retry (≤2)
    failed --> [*]
    applied --> [*]
    rejected --> [*]
    cancelled --> [*]
```

| From | Event | To | Side effects |
|---|---|---|---|
| — | `POST /v1/requests` | `draft` | validate, acquire lock or queue, audit `request.created` |
| `draft` | PR opened by GitHub App | `pr_open` | store branch/PR, audit |
| `draft` | cancel / queued > 7 d | `cancelled` | dequeue |
| `pr_open`/`plan_ready` | `POST /requests/{id}/cancel` | `cancelled` | close PR, release lock |
| `pr_open` | `workflow_run` (pr-validate) in_progress | `validating` | create `pipeline_runs` |
| `validating` | callback `stage=summary, passed=true` | `plan_ready` | store plan summary + cost, PR comment |
| `validating` | callback `passed=false` | `failed` | store findings; close PR with comment (configurable); release lock |
| `plan_ready` | `pull_request_review` approved + required checks green | `approved` | `approved_plan_hash = plan_summaries.plan_hash`, audit |
| `plan_ready` | TTL-expiry `destroy` request in **non-prod**, checks green | `approved` | `iacp-approver` machine user (member of `@acme/self-service-iac-platform-approvers`; GitHub Apps cannot be code owners) submits the approving review and the worker merges; production is never auto-approved; audit `request.auto_approved` |
| `validating`/`plan_ready`/`approved` | `pull_request.synchronize` | `validating` | clear approval & pinned hash; restart validation |
| `approved` | `pull_request_review` dismissed | `plan_ready` | clear approval & pinned hash |
| `pr_open`/`validating`/`plan_ready`/`approved` | `pull_request.closed` (unmerged) | `rejected` | release lock |
| `approved` | `push` to main / `workflow_run` apply in_progress | `applying` | stack → `updating`/`provisioning`/`destroying` |
| `applying` | callback apply succeeded | `applied` | stack outputs, `last_applied_sha`; stack → `active`/`destroyed`; release lock; dispatch next queued |
| `applying` | callback apply failed (transient), `retry_count < 2` | `applying` | `retry_count += 1`; re-dispatch with backoff |
| `applying` | callback apply failed (non-transient, or transient with retries used up) | `failed` | stack → `failed`; notify; release lock |

Transitions are enforced in `internal/requests/statemachine.go` by a pure transition table, `Transition(from, event) (to, error)`, and `Next(snapshot, event)`, which also enforces the retry budget (`MaxApplyRetries = 2`, counted in `requests.retry_count`). The `Request` entity wraps them (`Fire`, and `Approve`, which also pins the plan hash); DB updates use `UPDATE … WHERE id=$1 AND status=$2` (optimistic check) so concurrent webhook + callback races resolve deterministically.

### 4.2 Stack

```mermaid
stateDiagram-v2
    [*] --> provisioning: create request applying
    provisioning --> active: apply ok
    provisioning --> failed: apply failed
    active --> updating: update/upgrade request applying
    updating --> active: apply ok
    updating --> failed: apply failed
    active --> drifted: drift detected
    drifted --> updating: reconcile / codify request applying
    drifted --> active: drift resolved (next scan clean)
    active --> destroying: destroy request applying (incl. TTL)
    drifted --> destroying
    failed --> updating: fix-forward / rollback request applying
    failed --> destroying
    destroying --> destroyed: destroy ok
    destroying --> failed: destroy failed
    destroyed --> [*]
```

## 5. API conventions

- Base URL `https://api.iacp.acme.internal/v1`. JSON only (`application/json; charset=utf-8`). OpenAPI 3.1 spec in `backend/api/openapi.yaml` is the source of truth; handlers and portal client are generated from it (`oapi-codegen`, `openapi-typescript`).
- **Auth:** browser sessions use `iacp_session` HttpOnly, Secure, SameSite=Lax cookie holding a short-lived JWT (EdDSA, 15 min) + refresh cookie (8 h). CLI/automation uses `Authorization: Bearer <PAT-style token>` scoped to a team and role (stored hashed). Actions callbacks use `Authorization: Bearer <GitHub OIDC JWT>`. CSRF: double-submit token header `X-CSRF-Token` for cookie-authenticated mutations.
- **IDs:** UUIDs; requests also have a human-friendly `short_id`.
- **Timestamps:** RFC 3339 UTC.
- **Pagination:** cursor-based. `?limit=50` (max 200) `&cursor=<opaque>`. Response `{"data":[…], "next_cursor":"eyJzZXEiOjEyMzR9"|null}`. Cursor = base64 of the last sort key; stable under inserts.
- **Filtering/sorting:** explicit query params per endpoint (`status`, `team`, `env`, `module`); `sort=-created_at`.
- **Idempotency:** all `POST` creating resources accept `Idempotency-Key` (required for `POST /requests`). Same key + same body hash within 24 h → the original resource is returned with header `Idempotent-Replayed: true` (the key row stores `{"request_id": …}`, so a replay shows the request's current state; a create that fails validation stores nothing, so the key can be retried); same key + different body → `422 idempotency_key_reuse`. Keys expire after 24 h: a claim deletes an expired row for the same key first, and `PurgeExpiredIdempotencyKeys` lets the worker sweep old rows.
- **Concurrency control:** resources return `ETag` (row version); `PATCH`/`DELETE` honour `If-Match` → `412` on mismatch.
- **Rate limits:** 600 req/min per user, 60 req/min for `POST /requests`; headers `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`.
- **Correlation:** `X-Request-Id` echoed/generated; appears in logs and audit events.
- **Versioning:** URI major version (`/v1`); additive changes only within a major.

## 6. REST API reference

RBAC column uses team roles (see [07-security-and-policy.md](07-security-and-policy.md#7-rbac-matrix)). "any" = any authenticated user.

### 6.1 Auth

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/auth/github/login` | Redirect to GitHub OAuth (state + PKCE). | public |
| GET | `/auth/github/callback` | Exchange code, upsert user, sync teams, set session cookies. | public |
| POST | `/auth/refresh` | Rotate access token. | session |
| POST | `/auth/logout` | Revoke refresh token. | session |
| GET | `/me` | Current user, teams, roles, permissions. | any |
| POST | `/tokens` / GET `/tokens` / DELETE `/tokens/{id}` | Personal API tokens (scoped, max 90 d). | any (own) |

`GET /v1/me`

```json
{
  "id": "4b1f2a0e-8a71-4b0c-9c1e-2f1d5e6a7b80",
  "github_login": "priya-s",
  "display_name": "Priya S.",
  "is_platform_admin": false,
  "teams": [
    { "slug": "payments", "role": "developer" },
    { "slug": "checkout", "role": "viewer" }
  ]
}
```

### 6.2 Catalog: modules and versions

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/catalog/modules?category=&status=` | List modules with latest version. | any |
| GET | `/catalog/modules/{name}` | Module detail, owners, README. | any |
| GET | `/catalog/modules/{name}/versions` | Versions (SemVer desc), status, release date. | any |
| GET | `/catalog/modules/{name}/versions/{version}` | Manifest, `inputs_schema`, outputs, cost hints. | any |
| GET | `/catalog/modules/{name}/upgrade-path?from=1.2.0&to=latest` | Intermediate versions, breaking flags, concatenated changelog. | any |
| GET | `/catalog/modules/{name}/usage` | Stacks using each version (fleet view). | maintainer of owning team / admin |
| POST | `/catalog/modules/{name}/versions/{version}/deprecate` | Mark deprecated with note. | module owner / admin |
| POST | `/internal/catalog/index` | Index a released version: `{module, version, git_tag, commit_sha}`; backend fetches `module.yaml` at that SHA (called by `module-release.yml`). Until the GitHub client lands, the parsed manifest is sent inline. A re-index that changes owners or other metadata writes a `module.updated` audit event. | OIDC JWT: `module-release.yml@refs/heads/main` (interim: shared bearer token `IACP_INTERNAL_TOKEN`, constant-time compare; no token configured means 401) |

`GET /v1/catalog/modules/sqs/versions/1.3.0`

```json
{
  "module": "sqs",
  "version": "1.3.0",
  "git_tag": "modules/sqs/v1.3.0",
  "git_sha": "9c1d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d",
  "status": "active",
  "source": "git::https://github.com/acme/self-service-iac-platform.git//modules/sqs?ref=modules/sqs/v1.3.0",
  "tf_version_range": ">= 1.10, < 2.0",
  "aws_provider_range": "~> 5.80",
  "inputs_schema": {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "required": ["name"],
    "additionalProperties": false,
    "properties": {
      "name": { "type": "string", "pattern": "^[a-z][a-z0-9-]{2,70}$" },
      "visibility_timeout_seconds": { "type": "integer", "minimum": 0, "maximum": 43200, "default": 30 },
      "message_retention_seconds": { "type": "integer", "minimum": 60, "maximum": 1209600, "default": 345600 },
      "dlq_max_receive_count": { "type": "integer", "minimum": 1, "maximum": 20, "default": 5 },
      "fifo": { "type": "boolean", "default": false }
    }
  },
  "outputs": [
    { "name": "queue_url", "sensitive": false },
    { "name": "queue_arn", "sensitive": false },
    { "name": "dlq_arn", "sensitive": false }
  ],
  "cost_hints": { "fixed_monthly_usd": 0, "usage_based": ["requests"] },
  "released_at": "2026-10-02T09:14:22Z"
}
```

### 6.3 Stacks

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/stacks?team=&env=&status=&module=` | List stacks. | viewer |
| GET | `/stacks/{id}` | Stack detail: resources, versions, outputs, status, lock holder, drift, TTL. | viewer |
| GET | `/stacks/{id}/requests` | Request history. | viewer |
| GET | `/stacks/{id}/outputs` | Non-sensitive outputs. | viewer |
| GET | `/stacks/{id}/files` | Current generated files (from `main`). | viewer |
| GET | `/stacks/{id}/upgrades` | Available module upgrades per resource. | viewer |
| PATCH | `/stacks/{id}` | Change metadata only (e.g. extend `expires_at` within env max). | developer (non-prod), maintainer (prod) |
| GET | `/stacks/{id}/lock` / DELETE `/stacks/{id}/lock` | Inspect / force-release platform lock. | viewer / admin |

Stacks are never created or destroyed directly; mutation is always a request.

### 6.4 Requests

| Method | Path | Description | RBAC |
|---|---|---|---|
| POST | `/requests` | Create request (`kind`: create, update, upgrade, destroy, reconcile, rollback). Requires `Idempotency-Key`. | developer (any env); maintainer to create `destroy` in production. Enforced now from `memberships` (platform admins count as admin); full RBAC middleware arrives with WBS 4.4 |
| GET | `/requests?team=&status=&kind=&mine=true` | List. | viewer |
| GET | `/requests/{id}` | Detail incl. PR, pipeline runs, plan, cost, timeline. | viewer |
| POST | `/requests/{id}/preview` | Dry-run render: returns generated files + diff without opening a PR. | developer |
| POST | `/requests/{id}/cancel` | Cancel (`draft`/`pr_open`/`plan_ready`); closes PR, releases lock. | creator or maintainer |
| POST | `/requests/{id}/replan` | Re-dispatch `pr-validate.yml` (e.g. after plan mismatch). | developer |
| POST | `/requests/{id}/retry-apply` | Re-dispatch `apply.yml` for a `failed` request whose merge is on `main`. | maintainer |
| GET | `/requests/{id}/events` | Server-Sent Events stream of status changes. | viewer |

**Create** — `POST /v1/requests`

```http
POST /v1/requests HTTP/1.1
Idempotency-Key: 6a1f0c7e-2b9d-4d3e-a1c2-9f8e7d6c5b4a
Content-Type: application/json
```

```json
{
  "kind": "create",
  "team": "payments",
  "environment": "development",
  "stack": "order-events",
  "region": "us-east-1",
  "ephemeral": { "ttl_hours": 72 },
  "justification": "Order events pipeline for FEAT-1182",
  "resources": [
    {
      "name": "events_bucket",
      "module": "s3",
      "version": "1.2.0",
      "inputs": { "name": "payments-order-events-dev", "versioning_enabled": true,
                  "lifecycle_rules": [{ "id": "expire-raw", "prefix": "raw/", "expiration_days": 30 }] }
    },
    {
      "name": "orders_queue",
      "module": "sqs",
      "version": "1.3.0",
      "inputs": { "name": "payments-order-events-dev", "visibility_timeout_seconds": 60 }
    },
    {
      "name": "order_processor",
      "module": "lambda",
      "version": "2.0.1",
      "inputs": {
        "name": "payments-order-processor-dev",
        "runtime": "provided.al2023",
        "handler": "bootstrap",
        "memory_size": 256,
        "timeout": 30,
        "sqs_event_source": { "queue_arn": { "$ref": "orders_queue.queue_arn" }, "batch_size": 10 },
        "s3_write_bucket_arns": [{ "$ref": "events_bucket.bucket_arn" }],
        "environment_variables": {
          "QUEUE_URL": { "$ref": "orders_queue.queue_url" },
          "BUCKET_NAME": { "$ref": "events_bucket.bucket_name" }
        }
      }
    }
  ],
  "depends_on": []
}
```

If the change spans several dependent stacks, the backend splits it into **sequential requests ordered topologically**: the response carries a `chain` of request IDs, and each downstream request stays `draft` (`queued_behind` = its upstream) until the upstream is `applied`, so the downstream `iacp.deps.auto.tfvars.json` is rendered with fresh outputs. `apply.yml` refuses a push touching two dependent stacks.

`202 Accepted` (PR creation is asynchronous):

```json
{
  "id": "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90",
  "short_id": "req-7k3p9x",
  "kind": "create",
  "status": "draft",
  "stack": { "id": "b2c1f0a9-3e4d-4c5b-8a7f-1e2d3c4b5a69", "path": "environments/development/payments/order-events", "status": "provisioning" },
  "queued_behind": null,
  "links": {
    "self": "/v1/requests/0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90",
    "events": "/v1/requests/0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90/events"
  },
  "created_at": "2026-11-24T10:02:11Z"
}
```

**Update** — same shape with `"kind": "update"`, `"stack_id"` instead of team/env/stack, and the full desired `resources` list (declarative; removed resources are destroyed and shown in the plan). Optional `"if_match_spec_version": 7` guards against concurrent edits.

**Upgrade** — `POST /v1/requests`

```json
{
  "kind": "upgrade",
  "stack_id": "b2c1f0a9-3e4d-4c5b-8a7f-1e2d3c4b5a69",
  "upgrades": [ { "resource": "events_bucket", "to_version": "1.4.0" } ],
  "input_migrations": "auto"
}
```

The API validates existing inputs against the target version's schema, applies manifest-declared input renames (`migrations` in `module.yaml`), and rejects with `422 upgrade_requires_input_changes` listing fields if manual edits are needed. Major upgrades require `"acknowledge_breaking": true`.

**Destroy** — `{"kind": "destroy", "stack_id": "…", "justification": "Feature retired", "confirm_stack_name": "order-events"}`. The generator writes a destroy marker (`iacp.stack.json` `"lifecycle": "destroy"`) and empties `main.tf` module blocks so the plan shows the destroys; production destroys require maintainer + `prevent_destroy`-protected resources need a prior update request to lift protection.

**Request detail** — `GET /v1/requests/{id}` (abridged)

```json
{
  "id": "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90",
  "short_id": "req-7k3p9x",
  "kind": "create",
  "status": "plan_ready",
  "stack": { "id": "b2c1f0a9-3e4d-4c5b-8a7f-1e2d3c4b5a69", "path": "environments/development/payments/order-events" },
  "github": { "branch": "iacp/req-7k3p9x", "pr_number": 812, "pr_url": "https://github.com/acme/self-service-iac-platform/pull/812" },
  "plan": {
    "plan_hash": "3f9a1c0b7e2d4f6a8b0c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f708",
    "add": 11, "change": 0, "destroy": 0, "replace": 0,
    "policy": { "passed": true, "deny": [], "warn": ["iacp.lambda.reserved_concurrency_recommended"] }
  },
  "cost": { "currency": "USD", "monthly_before": 0.0, "monthly_after": 4.10, "monthly_delta": 4.10,
            "budget": { "limit": 2000.0, "committed": 1450.10, "remaining": 549.90, "decision": "allow" } },
  "pipeline_runs": [
    { "kind": "validate", "status": "succeeded", "github_run_id": 11873399021,
      "stages": [ { "name": "fmt", "status": "passed" }, { "name": "validate", "status": "passed" },
                  { "name": "tflint", "status": "passed" }, { "name": "checkov", "status": "passed" },
                  { "name": "plan", "status": "passed" }, { "name": "opa", "status": "passed" },
                  { "name": "infracost", "status": "passed" } ] }
  ],
  "timeline": [
    { "at": "2026-11-24T10:02:11Z", "status": "draft" },
    { "at": "2026-11-24T10:02:14Z", "status": "pr_open" },
    { "at": "2026-11-24T10:02:31Z", "status": "validating" },
    { "at": "2026-11-24T10:06:02Z", "status": "plan_ready" }
  ]
}
```

### 6.5 Plans and costs

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/requests/{id}/plan` | Latest plan summary + resource changes. | viewer |
| GET | `/requests/{id}/plan/raw` | Pre-signed URL (5 min) to redacted plan JSON artifact. | developer |
| GET | `/requests/{id}/cost` | Cost estimate with per-resource breakdown. | viewer |
| GET | `/teams/{slug}/costs?month=2026-11` | Team cost: estimated committed + CUR actuals per stack. | viewer |

### 6.6 Drift

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/drift?team=&env=&status=drifted` | Open drift reports. | viewer |
| GET | `/stacks/{id}/drift` | Drift history for stack. | viewer |
| POST | `/stacks/{id}/drift/check` | Trigger on-demand drift check (`workflow_dispatch` drift.yml for one stack). | developer |
| POST | `/drift` | Drift result from `drift.yml` (one call per stack): `{stack_path, run_id, status: clean\|drifted\|error, changed[], plan_artifact_uri, logs_url}`. | OIDC JWT: `drift.yml@refs/heads/main` |
| POST | `/drift/{report_id}/resolve` | `{"resolution":"reconcile"}` → creates a reconcile request; `"codify"` → opens update request from detected values (where module inputs map); `"ignore"` (maintainer, with reason). | developer / maintainer |

### 6.7 Budgets and exceptions

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/teams/{slug}/budgets` | Budgets and utilisation. | viewer |
| PUT | `/teams/{slug}/budgets/{month}` | Set/replace monthly budget. | admin |
| POST | `/requests/{id}/budget-override` | `{"max_delta_usd": 900, "reason": "...", "expires_in_hours": 72}` → inserts `budget_overrides`, sets `budgets.override_by`; triggers re-plan. | admin |
| POST | `/policy-exceptions` | Request exception `{rule_id, stack_id, reason, ticket_url, expires_at}`. | maintainer |
| POST | `/policy-exceptions/{id}/approve` / `/reject` | Decision. | security approver (`security` GitHub team) |
| GET | `/policy-exceptions?status=approved&team=` | List. | viewer |

### 6.8 Audit

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/audit?team=&subject_type=&subject_id=&action=&from=&to=` | Query audit events (cursor on `seq`). | maintainer (own team), admin / security (all) |
| GET | `/audit/verify?from_seq=&to_seq=` | Recompute chain over range; returns `{valid, first_bad_seq}`. | admin / security |
| GET | `/audit/export?date=2026-11-23` | Pre-signed URL to anchored NDJSON export. | admin / security |

```json
{
  "data": [
    {
      "seq": 1048213,
      "occurred_at": "2026-11-24T10:41:07.512Z",
      "actor": { "type": "user", "id": "4b1f2a0e-8a71-4b0c-9c1e-2f1d5e6a7b80", "login": "marco-p" },
      "action": "request.approved",
      "subject": { "type": "request", "id": "0d7e3a52-6c11-4f0e-b9b6-5f2f3c8d1a90" },
      "team": "payments",
      "correlation_id": "gh-review-2484412231",
      "payload": { "pr_number": 812, "plan_hash": "3f9a1c0b…f708" },
      "hash": "b7e1…9a0c",
      "prev_hash": "11d4…77e2"
    }
  ],
  "next_cursor": "eyJzZXEiOjEwNDgyMTJ9"
}
```

### 6.9 Pipeline runs (Actions callbacks)

Called only by GitHub Actions jobs with `Authorization: Bearer <GitHub OIDC JWT>` (audience `https://api.iacp.acme.internal`). These are the only endpoints the execution plane uses to talk to the control plane.

| Method | Path | Called by | Purpose |
|---|---|---|---|
| POST | `/runs` | `pr-validate.yml`, `apply.yml`, `ttl-reaper.yml` | Report run status and stage results (validating, plan summary, policy, cost, apply outcome, outputs). Creates/updates `pipeline_runs`, `plan_summaries`, `cost_estimates`. |
| GET | `/internal/requests/by-path?stack_path=&head_sha=` | `apply.yml` (before `terraform init`) | Confirms the per-stack lock row for the merge commit; returns `request_short_id`, `approved_plan_hash`, `fencing_token`, approvals (for the pre-apply OPA gate). `409 stack_locked` → job waits/retries. |
| GET | `/internal/budget-context?stack_path=` | `pr-validate.yml`, `apply.yml` | OPA input: environment, team, allowed regions, budget (`limit_usd`, `spent_usd`, active override), approved policy exceptions. |
| POST | `/drift` | `drift.yml` | Drift result per stack (see [6.6](#66-drift)). |
| POST | `/internal/catalog/index` | `module-release.yml` | Index a released module version (see [6.2](#62-catalog-modules-and-versions)). |
| GET | `/internal/stacks/{id}/lock` | worker, `apply.yml` | Current platform lock: `stack_id`, `request_id`, `holder`, `fencing_token`, `acquired_at`, `expires_at`; `404` when unlocked. |
| POST | `/internal/stacks/{id}/lock` | worker | Acquire for `{"request_id"}`: `200 {"lock": "granted"\|"held", "queued_behind": null, "current": {…}}` or `202 {"lock": "queued", "queued_behind": "<holder request id>", "current": {…}}`. |
| POST | `/internal/stacks/{id}/lock/heartbeat` | worker, `apply.yml` | Renew the lease: `{"request_id", "fencing_token"}`; `409 stale_fencing_token` when superseded. |
| DELETE | `/internal/stacks/{id}/lock?request_id=&fencing_token=` | worker | Release (terminal request) and promote the oldest queued request: `200 {"released": {…}, "promoted": {…}\|null}`; `409 stale_fencing_token`. |

Machine endpoints accept the GitHub OIDC JWT, and, as a fallback while workflows migrate, the shared `IACP_INTERNAL_TOKEN`. Neither configured means every call is rejected.

Verification order: JWKS signature (RS256 only; https JWKS endpoint, redirects never followed) → `iss == https://token.actions.githubusercontent.com` → `aud` → `exp`/`nbf`/`iat` (30 s leeway) → `repository`, `repository_owner` and, when `IACP_OIDC_REPOSITORY_ID` is configured (required in production), `repository_id == 87654321` → `ref` and `job_workflow_ref` in the global allowlist (`403 workflow_not_allowed`) → the **route class** policy below → for `/runs`, the per-pipeline rules: the report's `run_id` + `run_attempt` and `head_sha` equal the token's `run_id`, `run_attempt` and `sha` (`403 run_mismatch`) → fencing token current. Replays rejected via `jti` cache (1 h) — not implemented yet.

Route classes (`backend/internal/auth/routes.go`, pure table enforced by middleware). A pull-request token (`pr-validate.yml` on `refs/pull/*/merge`, whose workflow file the PR author controls) can only reach `/runs`. Callers using the shared internal token are not workflow-scoped.

| Route class | Routes | Workflow files | Ref | `event_name` |
|---|---|---|---|---|
| locks | `/internal/stacks/{id}/lock*` | `apply.yml`, `ttl-reaper.yml` | `refs/heads/main` (ref and workflow definition) | `push`, `workflow_dispatch`, `schedule` |
| catalog_index | `/internal/catalog/index` | `module-release.yml` | `refs/heads/main` | `push`, `workflow_dispatch` |
| runs | `/runs` | `pr-validate.yml`, `apply.yml`, `drift.yml`, `ttl-reaper.yml` | per pipeline | `pull_request`, `push`, `workflow_dispatch`, `schedule` |

Per pipeline on `/runs`: `validate` ← `pr-validate.yml` on `pull_request`; `apply` ← `apply.yml` on main via `push`/`workflow_dispatch`; `drift` ← `drift.yml` on main via `schedule`/`workflow_dispatch`; `ttl_destroy` ← `ttl-reaper.yml` on main via `schedule`/`workflow_dispatch`. `pull_request_target` and `workflow_run` never qualify.

`GET /v1/internal/requests/by-path?stack_path=environments/production/payments/order-events&head_sha=a1b2c3d4e5f60718293a4b5c6d7e8f9012345678` (apply) → `200`

```json
{ "lock": "granted", "request_short_id": "req-7k3p9x", "fencing_token": 48214,
  "approved_plan_hash": "3f9a1c0b7e2d4f6a8b0c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f708",
  "author": "priya-s", "allow_stateful_destroy": false,
  "approvals": [ { "login": "marco-p", "role": "maintainer", "state": "APPROVED" } ] }
```

`POST /v1/runs` (PR validation summary)

```json
{
  "pipeline": "validate",
  "stack_path": "environments/development/payments/order-events",
  "request_short_id": "req-7k3p9x",
  "fencing_token": 48213,
  "github": { "run_id": 11873399021, "run_attempt": 1, "job_id": 33412988710, "head_sha": "e4c2a1f0b9d8c7e6f5a4b3c2d1e0f9a8b7c6d5e4" },
  "stage": "summary",
  "status": "passed",
  "plan": {
    "hash": "3f9a1c0b7e2d4f6a8b0c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f708",
    "counts": { "add": 11, "change": 0, "destroy": 0, "replace": 0 },
    "artifact_uri": "s3://iacp-plan-artifacts-111111111111/11873399021/plan.json",
    "resource_changes": [
      { "address": "module.orders_queue.aws_sqs_queue.this", "actions": ["create"] },
      { "address": "module.orders_queue.aws_sqs_queue.dlq", "actions": ["create"] }
    ]
  },
  "policy": {
    "opa": { "deny": [], "warn": ["iacp.lambda.reserved_concurrency_recommended"] },
    "checkov": { "passed": 42, "failed": 0, "skipped": 1 },
    "tflint": { "errors": 0, "warnings": 0 }
  },
  "cost": { "currency": "USD", "monthly_before": 0.0, "monthly_after": 4.10 }
}
```

Apply callback adds `"outputs": {...}` (sensitive outputs replaced with `"<sensitive>"`), `"terraform_exit_code"`, and on failure `"error": {"class": "transient|permanent", "message": "...", "log_excerpt": "..."}`.

Response `200 {"accepted": true, "run_id": "…", "run_status": "succeeded", "request_status": "plan_ready"}`; `409 stale_fencing_token` aborts the job.

Current implementation (WBS 4.10): `pipeline` is one of `validate`, `apply`, `drift`, `ttl_destroy`; `status` is `in_progress`, `passed`, `failed` or `cancelled`. `fencing_token` and `request_short_id` are required except for `drift`; `github.head_sha` must be the job's `github.sha`. Each report is recorded in `pipeline_runs.stages` (one row per run id, attempt and stack): a report for a stage already recorded replaces that entry, so retried calls are idempotent, and a run records at most 32 distinct stages (`422 too_many_stages`). A `passed` `summary` stage finishes the run as `succeeded`, a `failed`/`cancelled` stage finishes it as such, and a finished run keeps its status. The report transaction locks the `stacks` row (`FOR KEY SHARE`) before reading the lock, the same order as the lock operations, so reports never deadlock with release or promotion. `request_status` is the request's status as stored; the transitions driven by reports, `plan_summaries` and `cost_estimates` arrive with WBS 5.2/5.3.

### 6.10 GitHub webhooks

| Method | Path | Description |
|---|---|---|
| POST | `/webhooks/github` | GitHub App webhook. Verifies `X-Hub-Signature-256` (HMAC-SHA256, constant-time compare), dedupes `X-GitHub-Delivery`, enqueues to SQS, returns `202` in < 100 ms. |

Subscribed events and handling:

| Event | Action(s) | Effect |
|---|---|---|
| `pull_request` | opened, synchronize, closed, reopened | link PR ↔ request; synchronize → back to `validating`; closed unmerged → `rejected`; merged → await apply |
| `pull_request_review` | submitted, dismissed | approval → `approved` (if checks green); dismissed → back to `plan_ready` |
| `workflow_run` | requested, in_progress, completed | create/update `pipeline_runs`; failed without callback → reconcile from logs/artifacts |
| `check_run` | completed | required-check bookkeeping for status display |
| `push` | (main) | detect hand edits to `environments/**` not originating from the platform PRs → alert |
| `installation`, `membership`, `team` | * | refresh installation token cache; resync team memberships |

### 6.11 Teams and environments (admin)

| Method | Path | Description | RBAC |
|---|---|---|---|
| GET | `/teams` / `/teams/{slug}` | Teams + members (synced). | any |
| POST | `/teams/{slug}/sync` | Force GitHub team sync. | maintainer |
| GET | `/environments` | Environments, accounts, rules. | any |
| PATCH | `/environments/{name}` | Change allowed regions, max TTL, approval rules. | admin |

## 7. Error model

All errors use RFC 9457 Problem Details (`application/problem+json`) with the platform extensions:

```json
{
  "type": "https://iacp.acme.internal/errors/validation_failed",
  "title": "Request inputs failed schema validation",
  "status": 422,
  "code": "validation_failed",
  "detail": "2 inputs are invalid for module sqs@1.3.0",
  "instance": "/v1/requests",
  "request_id": "01JDF3X9K8M2N4P6Q8R0S2T4V6",
  "errors": [
    { "pointer": "/resources/1/inputs/visibility_timeout_seconds", "code": "maximum", "message": "must be <= 43200" },
    { "pointer": "/resources/1/inputs/colour", "code": "additionalProperties", "message": "unknown input 'colour'" }
  ]
}
```

| HTTP | `code` | When |
|---|---|---|
| 400 | `bad_request` | Malformed JSON, bad query param |
| 401 | `unauthenticated` | Missing/expired session or token; invalid OIDC JWT |
| 403 | `forbidden` | RBAC denies (e.g. developer approving prod) |
| 403 | `workflow_not_allowed` | OIDC `job_workflow_ref` not allowlisted for endpoint |
| 403 | `run_mismatch` | `POST /runs` report names a different `run_id`/`run_attempt` or `head_sha` than the caller's OIDC token |
| 422 | `too_many_stages` | `POST /runs` would record more than 32 distinct stages for one run |
| 404 | `not_found` | Resource missing or not visible to caller |
| 409 | `stack_locked` | Operation needs lock immediately (e.g. `preview` with `exclusive=true`); body includes `holder` and `request_id` |
| 409 | `invalid_transition` | e.g. cancel an `applying` request |
| 409 | `stale_fencing_token` | Callback from superseded run |
| 409 | `stack_exists` | Create for existing env/team/name |
| 412 | `precondition_failed` | `If-Match` mismatch |
| 422 | `validation_failed` | Schema/semantic validation |
| 422 | `module_version_unavailable` | Yanked version or incompatible TF/provider range |
| 422 | `upgrade_requires_input_changes` | See upgrade |
| 422 | `idempotency_key_reuse` | Same key, different body |
| 422 | `policy_preflight_denied` | Fast pre-checks (region, env TTL) failed before PR |
| 429 | `rate_limited` | With `Retry-After` |
| 502 | `github_unavailable` | Synchronous GitHub call failed (rare; most are async) |
| 503 | `maintenance` | Read-only mode / DB failover |

## 8. Go package layout

```
backend/
├── cmd/
│   └── api/                     # HTTP API (worker and CLI entrypoints arrive with WBS 4.8/4.9)
│       └── main.go
├── api/
│   └── openapi.yaml             # source of truth; oapi-codegen → internal/httpapi/gen
├── migrations/                  # goose SQL migrations (0001_init.sql, 0002_audit.sql, …)
├── queries/                     # sqlc query files (*.sql)
├── sqlc.yaml
├── internal/
│   ├── config/                  # env config, Secrets Manager loader
│   ├── db/                      # sqlc-generated code, pgxpool setup, tx helper (WithTx)
│   ├── httpapi/                 # chi router, middleware (auth, rbac, idempotency, ratelimit, problem+json), handlers
│   │   └── gen/                 # generated server interfaces/types
│   ├── auth/                    # GitHub OAuth, session JWT, API tokens, GitHub OIDC verifier (JWKS cache)
│   ├── rbac/                    # role/permission matrix, Authorize(ctx, subject, action, resource)
│   ├── catalog/                 # modules, versions, upgrade paths, manifest indexing
│   ├── requests/                # request service, state machine, validation, queueing
│   ├── stacks/                  # stack queries, outputs, DAG (topological sort, cycle detection)
│   ├── generator/               # pure renderer: StackSpec → map[path][]byte
│   │   ├── templates/           # *.tf.tmpl (embed.FS)
│   │   └── testdata/golden/     # golden files
│   ├── github/                  # GitHub App client (installation tokens, git data API, PRs, dispatch), rate-limit aware
│   ├── webhooks/                # signature verification, dedupe, event mapping → SQS
│   ├── runs/                    # POST /v1/runs: report validation, OIDC provenance, pipeline_runs
│   ├── callbacks/               # pipeline callback handlers (plan, cost, apply, drift)
│   ├── locks/                   # acquire/heartbeat/release, fencing, FIFO queue promotion
│   ├── budgets/                 # budgets, overrides, OPA budget context
│   ├── policyex/                # policy exceptions workflow
│   ├── drift/                   # drift scheduling (batch fan-out), report handling
│   ├── ttl/                     # hourly reaper tick: 24 h warnings, destroy requests on expiry, non-prod auto-approval
│   ├── audit/                   # writer (in-tx insert), verifier, S3 anchor/export
│   ├── notify/                  # Slack, email
│   ├── queue/                   # SQS producer/consumer, message envelopes, retry/backoff
│   ├── worker/                  # consumer loop, handlers registry, reconciler
│   └── observability/           # slog JSON logger, EMF metrics, OTel tracing
├── pkg/
│   └── iacpclient/           # generated Go client (used by CLI and tests)
├── Dockerfile                   # distroless static, non-root
└── Makefile
```

Conventions: handlers are thin (decode → authorize → call service → encode); services take `context.Context` and a `db.Querier`; every state-changing service call writes its audit event **in the same transaction** (`audit.Record(ctx, tx, evt)`); external side effects (GitHub writes, notifications) go through SQS (transactional outbox table `outbox` drained by the worker) to keep DB and GitHub consistent.
