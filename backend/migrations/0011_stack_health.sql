-- stacks.health (WBS 5.3): what the post-apply smoke test says about a stack, separate from
-- its lifecycle status. A failed smoke test does not fail the apply (docs/05 step 12): the
-- stack is `active` with health = 'degraded' and the team is told. `unknown` means no
-- check ran (a new stack, a destroy, or nothing to check).

-- +goose Up
ALTER TABLE stacks
    ADD COLUMN health text NOT NULL DEFAULT 'unknown' CHECK (health IN ('unknown', 'healthy', 'degraded'));

-- +goose Down
ALTER TABLE stacks DROP COLUMN health;
