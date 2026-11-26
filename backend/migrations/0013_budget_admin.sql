-- budget administration (WBS 8.5): who last changed a budget, an admin override with its
-- reason and expiry, and the recorded actual spend.
--
-- override_by already exists (0007); the override now also carries a reason and an
-- expiry, and the three columns are set or cleared together. actual_usd is recorded by
-- POST /v1/internal/budget-actuals (docs/06 section 10); Cost Explorer itself is not
-- reachable under LocalStack (ADR-0009), so nothing fills it automatically yet.
--
-- Privileges: 0004's default grant already gives iacp_app DML on budgets.

-- +goose Up
ALTER TABLE budgets
    ADD COLUMN updated_by          uuid REFERENCES users(id),
    ADD COLUMN updated_at          timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN override_reason     text,
    ADD COLUMN override_expires_at timestamptz,
    ADD COLUMN actual_usd          numeric(12,2),
    ADD COLUMN actual_recorded_at  timestamptz,
    ADD CONSTRAINT budgets_override_complete CHECK (
        (override_by IS NULL) = (override_reason IS NULL)
        AND (override_by IS NULL) = (override_expires_at IS NULL)
        AND (override_reason IS NULL OR length(override_reason) >= 20)),
    ADD CONSTRAINT budgets_actual_nonnegative CHECK (actual_usd IS NULL OR actual_usd >= 0),
    ADD CONSTRAINT budgets_limit_bounded CHECK (limit_usd <= 1000000),
    ADD CONSTRAINT budgets_thresholds_range CHECK (1 <= ALL (alert_thresholds) AND 200 >= ALL (alert_thresholds));

-- +goose Down
ALTER TABLE budgets
    DROP CONSTRAINT budgets_thresholds_range,
    DROP CONSTRAINT budgets_limit_bounded,
    DROP CONSTRAINT budgets_actual_nonnegative,
    DROP CONSTRAINT budgets_override_complete,
    DROP COLUMN actual_recorded_at,
    DROP COLUMN actual_usd,
    DROP COLUMN override_expires_at,
    DROP COLUMN override_reason,
    DROP COLUMN updated_at,
    DROP COLUMN updated_by;
