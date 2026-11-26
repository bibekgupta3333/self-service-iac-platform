-- budgets: a team's monthly budget, optionally per environment (docs/03 §2, docs/06 §10).
-- Read by GET /v1/internal/budget-context (WBS 6.4) for the conftest --data context.
-- spent_usd is the committed-cost estimate; it stays 0 until cost estimates and Cost
-- Explorer actuals are recorded (WBS 8.5). budget_overrides arrives with 8.5.
--
-- Privileges: 0004's default grant gives iacp_app DML here; admins set and replace
-- budgets through the API (PUT /v1/teams/{slug}/budgets/{month}).

-- +goose Up
CREATE TABLE budgets (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id          uuid          NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    environment_id   uuid          REFERENCES environments(id),        -- NULL = all environments
    month            date          NOT NULL CHECK (date_trunc('month', month) = month),
    limit_usd        numeric(12,2) NOT NULL CHECK (limit_usd >= 0),
    spent_usd        numeric(12,2) NOT NULL DEFAULT 0 CHECK (spent_usd >= 0),
    alert_thresholds int[]         NOT NULL DEFAULT '{50,80,100}',
    hard_block       boolean       NOT NULL DEFAULT true,
    override_by      uuid          REFERENCES users(id),
    created_by       uuid          NOT NULL REFERENCES users(id),
    created_at       timestamptz   NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (team_id, environment_id, month)
);
CREATE INDEX budgets_team_month_idx ON budgets (team_id, month);

-- +goose Down
DROP TABLE budgets;
