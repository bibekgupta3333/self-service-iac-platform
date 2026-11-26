-- pipeline_runs: one GitHub Actions workflow run reported through POST /v1/runs
-- (docs/03-data-model-and-api.md §2 and §6.9). Stage reports are appended to `stages`.
--
-- Privileges: 0004 gives iacp_app DML on new tables by default. Run reports are only
-- inserted and updated, so DELETE is revoked here (least privilege).

-- +goose Up
CREATE TYPE pipeline_kind   AS ENUM ('validate', 'apply', 'drift', 'ttl_destroy', 'module_release');
CREATE TYPE pipeline_status AS ENUM ('queued', 'in_progress', 'succeeded', 'failed', 'cancelled', 'timed_out');

CREATE TABLE pipeline_runs (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id         uuid            REFERENCES requests(id) ON DELETE CASCADE,
    stack_id           uuid            NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,  -- part of the run key; NULLs would never conflict
    kind               pipeline_kind   NOT NULL,
    status             pipeline_status NOT NULL DEFAULT 'queued',
    github_run_id      bigint          NOT NULL CHECK (github_run_id > 0),
    github_run_attempt int             NOT NULL DEFAULT 1 CHECK (github_run_attempt > 0),
    github_job_id      bigint,
    workflow_ref       text            NOT NULL,
    head_sha           char(40)        NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}$'),
    stages             jsonb           NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(stages) = 'array'),
    log_url            text,
    started_at         timestamptz,
    finished_at        timestamptz,
    created_at         timestamptz     NOT NULL DEFAULT now(),
    UNIQUE (github_run_id, github_run_attempt, stack_id)
);
CREATE INDEX pipeline_runs_request_idx ON pipeline_runs (request_id, created_at DESC);
CREATE INDEX pipeline_runs_active_idx  ON pipeline_runs (status) WHERE status IN ('queued', 'in_progress');

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        REVOKE ALL ON pipeline_runs FROM iacp_app;
        GRANT SELECT, INSERT, UPDATE ON pipeline_runs TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE pipeline_runs;
DROP TYPE pipeline_status;
DROP TYPE pipeline_kind;
