-- budget administration (WBS 8.5): who last changed a budget, a bounded admin override with
-- its reason and expiry, the single-change cap, the recorded actual spend, and the per-stack
-- committed monthly cost the team-wide total is summed from.
--
-- override_by already exists (0007) but nothing has ever written it, so any row that has it
-- set is a half-made override: the backfill below clears it, which lets the completeness
-- constraint be added without NOT VALID. The override now also carries a reason, an expiry and
-- a bound (override_max_delta_usd), set or cleared together.
--
-- actual_usd is recorded by POST /v1/internal/budget-actuals and stacks.committed_monthly_usd
-- by POST /v1/internal/budget-committed (docs/06 section 10). Cost Explorer is not reachable
-- under LocalStack (ADR-0009) and apply.yml does not price a plan yet, so nothing fills either
-- automatically; docs/adr/0014 records the deferral.
--
-- Privileges: 0004's default grant already gives iacp_app DML on budgets and stacks.

-- +goose Up
UPDATE budgets SET override_by = NULL WHERE override_by IS NOT NULL;

ALTER TABLE budgets
    ADD COLUMN updated_by              uuid REFERENCES users(id),
    ADD COLUMN updated_at              timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN override_reason         text,
    ADD COLUMN override_expires_at     timestamptz,
    ADD COLUMN override_max_delta_usd  numeric(12,2),
    ADD COLUMN single_change_cap_usd   numeric(12,2) NOT NULL DEFAULT 5000,
    ADD COLUMN actual_usd              numeric(12,2),
    ADD COLUMN actual_recorded_at      timestamptz,
    ADD CONSTRAINT budgets_override_complete CHECK (
        (override_by IS NULL) = (override_reason IS NULL)
        AND (override_by IS NULL) = (override_expires_at IS NULL)
        AND (override_by IS NULL) = (override_max_delta_usd IS NULL)
        AND (override_reason IS NULL OR length(override_reason) >= 20)
        AND (override_max_delta_usd IS NULL OR override_max_delta_usd > 0)),
    ADD CONSTRAINT budgets_actual_nonnegative CHECK (actual_usd IS NULL OR actual_usd >= 0),
    ADD CONSTRAINT budgets_limit_bounded CHECK (limit_usd <= 1000000),
    ADD CONSTRAINT budgets_cap_bounded CHECK (single_change_cap_usd >= 0 AND single_change_cap_usd <= 1000000),
    ADD CONSTRAINT budgets_thresholds_range CHECK (1 <= ALL (alert_thresholds) AND 200 >= ALL (alert_thresholds));

ALTER TABLE stacks
    ADD COLUMN committed_monthly_usd numeric(12,2) CHECK (committed_monthly_usd IS NULL OR committed_monthly_usd >= 0),
    ADD COLUMN committed_at          timestamptz;

-- +goose Down
ALTER TABLE stacks
    DROP COLUMN committed_at,
    DROP COLUMN committed_monthly_usd;

ALTER TABLE budgets
    DROP CONSTRAINT budgets_thresholds_range,
    DROP CONSTRAINT budgets_cap_bounded,
    DROP CONSTRAINT budgets_limit_bounded,
    DROP CONSTRAINT budgets_actual_nonnegative,
    DROP CONSTRAINT budgets_override_complete,
    DROP COLUMN actual_recorded_at,
    DROP COLUMN actual_usd,
    DROP COLUMN single_change_cap_usd,
    DROP COLUMN override_max_delta_usd,
    DROP COLUMN override_expires_at,
    DROP COLUMN override_reason,
    DROP COLUMN updated_at,
    DROP COLUMN updated_by;
