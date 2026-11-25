-- Core schema for the control plane (docs/03-data-model-and-api.md §2).
-- Tables for later milestones (pipeline_runs, plan_summaries, cost_estimates, drift_reports,
-- budgets, policy_exceptions, stack_dependencies, webhook_deliveries, outbox) arrive with the
-- features that use them.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS citext;

-- ---------- enums ----------
CREATE TYPE team_role      AS ENUM ('viewer', 'developer', 'maintainer', 'admin');
CREATE TYPE env_name       AS ENUM ('development', 'staging', 'production');
CREATE TYPE module_status  AS ENUM ('active', 'deprecated', 'yanked');
CREATE TYPE stack_status   AS ENUM ('provisioning', 'active', 'drifted', 'updating', 'destroying', 'destroyed', 'failed');
CREATE TYPE request_kind   AS ENUM ('create', 'update', 'upgrade', 'destroy', 'reconcile', 'rollback');
CREATE TYPE request_status AS ENUM ('draft', 'pr_open', 'validating', 'plan_ready', 'approved', 'applying',
                                    'applied', 'failed', 'rejected', 'cancelled');

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
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    github_user_id    bigint      NOT NULL UNIQUE,
    github_login      citext      NOT NULL UNIQUE,
    email             citext,
    display_name      text,
    is_platform_admin boolean     NOT NULL DEFAULT false,
    is_system         boolean     NOT NULL DEFAULT false,
    last_login_at     timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    disabled_at       timestamptz
);

CREATE TABLE memberships (
    team_id   uuid        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id   uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role      team_role   NOT NULL,
    source    text        NOT NULL DEFAULT 'github_sync' CHECK (source IN ('github_sync', 'manual')),
    synced_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id);

-- ---------- catalog ----------
CREATE TABLE modules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text          NOT NULL UNIQUE CHECK (name ~ '^[a-z][a-z0-9-]{1,40}$'),
    display_name text          NOT NULL,
    description  text          NOT NULL,
    category     text          NOT NULL,
    owners       text[]        NOT NULL,
    repo_path    text          NOT NULL,
    status       module_status NOT NULL DEFAULT 'active',
    created_at   timestamptz   NOT NULL DEFAULT now(),
    updated_at   timestamptz   NOT NULL DEFAULT now()
);

CREATE TABLE module_versions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    module_id          uuid          NOT NULL REFERENCES modules(id) ON DELETE RESTRICT,
    version            text          NOT NULL CHECK (version ~ '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$'),
    major              int           NOT NULL,
    minor              int           NOT NULL,
    patch              int           NOT NULL,
    git_tag            text          NOT NULL UNIQUE,
    git_sha            char(40)      NOT NULL,
    manifest           jsonb         NOT NULL,
    inputs_schema      jsonb         NOT NULL,
    outputs            jsonb         NOT NULL,
    cost_hints         jsonb,
    tf_version_range   text          NOT NULL,
    aws_provider_range text          NOT NULL,
    changelog_md       text,
    status             module_status NOT NULL DEFAULT 'active',
    deprecation_note   text,
    released_by        text          NOT NULL,
    released_at        timestamptz   NOT NULL DEFAULT now(),
    UNIQUE (module_id, version),
    CHECK (status <> 'deprecated' OR deprecation_note IS NOT NULL)
);
CREATE INDEX module_versions_semver_idx ON module_versions (module_id, major DESC, minor DESC, patch DESC);

CREATE TABLE environments (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              env_name    NOT NULL UNIQUE,
    aws_account_id    char(12)    NOT NULL UNIQUE,
    default_region    text        NOT NULL DEFAULT 'us-east-1',
    allowed_regions   text[]      NOT NULL,
    requires_approval boolean     NOT NULL DEFAULT false,
    min_approver_role team_role   NOT NULL DEFAULT 'developer',
    allow_ephemeral   boolean     NOT NULL DEFAULT true,
    max_ttl_hours     int         CHECK (max_ttl_hours IS NULL OR max_ttl_hours > 0)
);

-- ---------- stacks ----------
CREATE TABLE stacks (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id                 uuid         NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    environment_id          uuid         NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
    name                    text         NOT NULL CHECK (name ~ '^[a-z][a-z0-9-]{1,40}$'),
    path                    text         NOT NULL UNIQUE,
    state_key               text         NOT NULL UNIQUE,
    region                  text         NOT NULL,
    status                  stack_status NOT NULL DEFAULT 'provisioning',
    is_ephemeral            boolean      NOT NULL DEFAULT false,
    expires_at              timestamptz,
    ttl_warned_at           timestamptz,
    outputs                 jsonb        NOT NULL DEFAULT '{}'::jsonb,
    last_applied_sha        char(40),
    last_applied_request_id uuid,
    last_drift_check_at     timestamptz,
    tags                    jsonb        NOT NULL DEFAULT '{}'::jsonb,
    created_by              uuid         NOT NULL REFERENCES users(id),
    created_at              timestamptz  NOT NULL DEFAULT now(),
    updated_at              timestamptz  NOT NULL DEFAULT now(),
    destroyed_at            timestamptz,
    UNIQUE (environment_id, team_id, name),
    CHECK (NOT is_ephemeral OR expires_at IS NOT NULL),
    CHECK (status <> 'destroyed' OR destroyed_at IS NOT NULL)
);
CREATE INDEX stacks_team_status_idx ON stacks (team_id, status);
CREATE INDEX stacks_expiry_idx      ON stacks (expires_at) WHERE is_ephemeral AND status NOT IN ('destroying', 'destroyed');
CREATE INDEX stacks_drift_due_idx   ON stacks (last_drift_check_at NULLS FIRST) WHERE status IN ('active', 'drifted');

CREATE TABLE stack_resources (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stack_id          uuid        NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    instance_name     text        NOT NULL CHECK (instance_name ~ '^[a-z][a-z0-9_]{0,62}$'),
    module_version_id uuid        NOT NULL REFERENCES module_versions(id) ON DELETE RESTRICT,
    inputs            jsonb       NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    removed_at        timestamptz,
    UNIQUE (stack_id, instance_name)
);
CREATE INDEX stack_resources_module_version_idx ON stack_resources (module_version_id) WHERE removed_at IS NULL;

-- ---------- requests ----------
CREATE TABLE requests (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    short_id           text           NOT NULL UNIQUE,
    stack_id           uuid           NOT NULL REFERENCES stacks(id) ON DELETE RESTRICT,
    kind               request_kind   NOT NULL,
    status             request_status NOT NULL DEFAULT 'draft',
    spec               jsonb          NOT NULL,
    previous_spec      jsonb,
    rendered_tree_sha  char(40),
    justification      text,
    queued_behind      uuid           REFERENCES requests(id),
    fencing_token      bigint,
    github_branch      text,
    github_pr_number   int,
    github_pr_url      text,
    merge_commit_sha   char(40),
    approved_plan_hash char(64),
    approved_by        uuid           REFERENCES users(id),
    approved_at        timestamptz,
    failure_reason     text,
    failure_detail     jsonb,
    retry_count        int            NOT NULL DEFAULT 0 CHECK (retry_count BETWEEN 0 AND 5),
    created_by         uuid           NOT NULL REFERENCES users(id),
    created_at         timestamptz    NOT NULL DEFAULT now(),
    updated_at         timestamptz    NOT NULL DEFAULT now(),
    completed_at       timestamptz,
    CHECK ((status IN ('applied', 'failed', 'rejected', 'cancelled')) = (completed_at IS NOT NULL)),
    CHECK (status NOT IN ('approved', 'applying', 'applied') OR approved_plan_hash IS NOT NULL)
);
CREATE INDEX requests_stack_created_idx ON requests (stack_id, created_at DESC);
CREATE INDEX requests_open_idx          ON requests (status) WHERE completed_at IS NULL;
CREATE INDEX requests_creator_idx       ON requests (created_by, created_at DESC);
CREATE INDEX requests_created_idx       ON requests (created_at DESC, id DESC);
CREATE UNIQUE INDEX requests_pr_idx     ON requests (github_pr_number) WHERE github_pr_number IS NOT NULL;

-- ---------- locks ----------
CREATE SEQUENCE lock_fencing_seq;
CREATE TABLE locks (
    stack_id      uuid        PRIMARY KEY REFERENCES stacks(id) ON DELETE CASCADE,
    request_id    uuid        NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    holder        text        NOT NULL,
    fencing_token bigint      NOT NULL DEFAULT nextval('lock_fencing_seq'),
    acquired_at   timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    CHECK (expires_at > acquired_at)
);
CREATE INDEX locks_expiry_idx ON locks (expires_at);

-- ---------- idempotency ----------
CREATE TABLE idempotency_keys (
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key           text        NOT NULL CHECK (length(key) BETWEEN 8 AND 128),
    request_hash  char(64)    NOT NULL,
    response_code int,
    response_body jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE locks;
DROP SEQUENCE lock_fencing_seq;
DROP TABLE requests;
DROP TABLE stack_resources;
DROP TABLE stacks;
DROP TABLE environments;
DROP TABLE module_versions;
DROP TABLE modules;
DROP TABLE memberships;
DROP TABLE users;
DROP TABLE teams;
DROP TYPE request_status;
DROP TYPE request_kind;
DROP TYPE stack_status;
DROP TYPE module_status;
DROP TYPE env_name;
DROP TYPE team_role;
