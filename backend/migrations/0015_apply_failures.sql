-- apply_failures: the classified failures of apply.yml runs and the automatic retries they
-- scheduled (docs/06-lifecycle-management.md §5, WBS 8.7).
--
-- One row per failed run of a request: the first failed stage a run reports is classified
-- (failure_class, reason) and recovery records what it decided. The unique key on the run
-- makes a replayed report, or a later failed stage of the same run, a no-op. A transient
-- failure with retry budget left carries retry_number (requests.retry_count after the
-- retry) and a dispatch_state that the dispatcher moves scheduled → dispatching →
-- dispatched | dispatch_failed | skipped; a claimed dispatch is never sent twice.
--
-- Privileges: iacp_app inserts and updates (dispatch bookkeeping); rows are never deleted.

-- +goose Up
CREATE TYPE failure_class AS ENUM ('transient', 'config', 'infra');

CREATE TABLE apply_failures (
    id                  uuid          PRIMARY KEY,
    request_id          uuid          NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
    stack_id            uuid          NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    github_run_id       bigint        NOT NULL CHECK (github_run_id > 0),
    github_run_attempt  int           NOT NULL CHECK (github_run_attempt > 0),
    stage               text          NOT NULL CHECK (stage ~ '^[a-z][a-z0-9_-]{0,39}$'),
    terraform_exit_code int,
    failure_class       failure_class NOT NULL,
    reason              text          NOT NULL CHECK (reason ~ '^[a-z][a-z0-9_]{1,40}$'),
    decision            text          NOT NULL CHECK (decision ~ '^[a-z][a-z0-9_]{1,40}$'),
    retry_number        int           CHECK (retry_number BETWEEN 1 AND 5),
    retry_at            timestamptz,
    dispatch_state      text          CHECK (dispatch_state IN ('scheduled', 'dispatching', 'dispatched', 'dispatch_failed', 'skipped')),
    dispatch_detail     text          CHECK (length(dispatch_detail) <= 2000),
    dispatched_at       timestamptz,
    created_at          timestamptz   NOT NULL,
    updated_at          timestamptz   NOT NULL,
    UNIQUE (request_id, github_run_id, github_run_attempt),
    -- A retry has a number, a time and a dispatch state; a failure without a retry has none.
    CHECK ((retry_number IS NULL) = (dispatch_state IS NULL) AND (retry_number IS NULL) = (retry_at IS NULL))
);
-- One retry per number per request: retry_count can only move one step at a time.
CREATE UNIQUE INDEX apply_failures_retry_idx ON apply_failures (request_id, retry_number) WHERE retry_number IS NOT NULL;
CREATE INDEX apply_failures_stack_idx ON apply_failures (stack_id, created_at DESC);

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        REVOKE ALL ON apply_failures FROM iacp_app;
        GRANT SELECT, INSERT, UPDATE ON apply_failures TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE apply_failures;
DROP TYPE failure_class;
