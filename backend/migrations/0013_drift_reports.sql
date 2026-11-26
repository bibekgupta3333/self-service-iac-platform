-- drift_reports: the result of one nightly drift check of one stack (docs/03 §2 and §6.6,
-- docs/06 §4), reported by drift.yml through POST /v1/internal/drift (WBS 8.3).
--
-- Drift is recorded in stacks.health (0011), widened here with 'drifted': a drift scan only
-- ever moves health, never stacks.status, so it cannot disturb a request in flight
-- (ADR-0013).
--
-- Privileges: 0004's default grant gives iacp_app DML here. Findings are immutable once
-- written and reports are never deleted, so DELETE is revoked and UPDATE is limited to the
-- resolution columns (least privilege).

-- +goose Up
CREATE TYPE drift_result AS ENUM ('clean', 'drifted', 'error');
CREATE TYPE drift_status AS ENUM ('open', 'reconciled', 'ignored', 'codified');

ALTER TABLE stacks DROP CONSTRAINT stacks_health_check;
ALTER TABLE stacks ADD CONSTRAINT stacks_health_check
    CHECK (health IN ('unknown', 'healthy', 'degraded', 'drifted'));

CREATE TABLE drift_reports (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    stack_id              uuid         NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
    pipeline_run_id       uuid         NOT NULL UNIQUE REFERENCES pipeline_runs(id) ON DELETE RESTRICT,  -- the drift.yml run: one report per run and stack
    result                drift_result NOT NULL,
    terraform_exit_code   smallint     NOT NULL CHECK (terraform_exit_code IN (0, 1, 2)),
    resources_drifted     int          NOT NULL DEFAULT 0 CHECK (resources_drifted >= 0),
    resources_updated     int          NOT NULL DEFAULT 0 CHECK (resources_updated >= 0),
    resources_deleted     int          NOT NULL DEFAULT 0 CHECK (resources_deleted >= 0),
    changed               jsonb        NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(changed) = 'array'),  -- [{address, type, actions}], bounded by the API
    plan_hash             char(64)     CHECK (plan_hash IS NULL OR plan_hash ~ '^[0-9a-f]{64}$'),
    log_url               text,
    error_message         text,
    status                drift_status,                                  -- resolution; NULL unless result = 'drifted'
    detected_at           timestamptz  NOT NULL DEFAULT now(),
    resolved_by           uuid         REFERENCES users(id),             -- NULL when a clean scan closed it
    resolved_at           timestamptz,
    resolution_reason     text         CHECK (resolution_reason IS NULL OR length(resolution_reason) <= 500),
    resolution_request_id uuid         REFERENCES requests(id),          -- the update request a reconcile opened
    CHECK ((result = 'drifted') = (status IS NOT NULL)),
    CHECK ((status IS NULL OR status = 'open') = (resolved_at IS NULL)),
    CHECK (result <> 'drifted' OR resources_drifted > 0),
    CHECK (result = 'drifted' OR resources_drifted = 0),
    CHECK (status IS DISTINCT FROM 'ignored' OR length(btrim(coalesce(resolution_reason, ''))) > 0)
);
CREATE INDEX drift_reports_stack_idx ON drift_reports (stack_id, detected_at DESC, id DESC);
CREATE INDEX drift_reports_open_idx  ON drift_reports (stack_id) WHERE status = 'open';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        REVOKE ALL ON drift_reports FROM iacp_app;
        GRANT SELECT, INSERT ON drift_reports TO iacp_app;
        GRANT UPDATE (status, resolved_by, resolved_at, resolution_reason, resolution_request_id)
            ON drift_reports TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE drift_reports;
UPDATE stacks SET health = 'unknown' WHERE health = 'drifted';
ALTER TABLE stacks DROP CONSTRAINT stacks_health_check;
ALTER TABLE stacks ADD CONSTRAINT stacks_health_check
    CHECK (health IN ('unknown', 'healthy', 'degraded'));
DROP TYPE drift_status;
DROP TYPE drift_result;
