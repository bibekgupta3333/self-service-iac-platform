-- Production environment enablement (WBS 8.8, docs/07 §6.8 and §7, ADR-0015).
--
--   * Seeds the staging and production environment rows next to development (0003), so
--     requests can target them. While the platform is local-first (ADR-0009) every
--     environment runs on LocalStack; the account ids are placeholders, unique as the
--     column requires, and are replaced when an environment moves to a real AWS account.
--   * Production always requires approval by at least a maintainer: a trigger forces
--     requires_approval and min_approver_role on the production row (like the 0012 trigger
--     for allow_ephemeral), and a CHECK states the invariant. The approvals themselves are
--     enforced by GitHub (CODEOWNERS, the production Environment) and the pre-apply gate in
--     apply.yml (policies/opa/prod_approval.rego); these columns describe the rule.
--
-- Existing rows are left alone (ON CONFLICT DO NOTHING) apart from the production
-- invariants, which the trigger and the UPDATE below enforce.

-- +goose Up
INSERT INTO environments (name, aws_account_id, default_region, allowed_regions, requires_approval,
                          min_approver_role, allow_ephemeral, max_ttl_hours)
VALUES ('staging', '000000000001', 'us-east-1', ARRAY['us-east-1'], true, 'developer', true, 168)
ON CONFLICT (name) DO NOTHING;

INSERT INTO environments (name, aws_account_id, default_region, allowed_regions, requires_approval,
                          min_approver_role, allow_ephemeral, max_ttl_hours)
VALUES ('production', '000000000002', 'us-east-1', ARRAY['us-east-1'], true, 'maintainer', false, NULL)
ON CONFLICT (name) DO NOTHING;

-- +goose StatementBegin
CREATE FUNCTION environments_production_approval() RETURNS trigger AS $$
BEGIN
    IF NEW.name = 'production' THEN
        NEW.requires_approval := true;
        IF NEW.min_approver_role NOT IN ('maintainer', 'admin') THEN
            NEW.min_approver_role := 'maintainer';
        END IF;
    END IF;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER environments_production_approval
    BEFORE INSERT OR UPDATE ON environments
    FOR EACH ROW EXECUTE FUNCTION environments_production_approval();

UPDATE environments SET requires_approval = true WHERE name = 'production';
ALTER TABLE environments
    ADD CONSTRAINT environments_production_approval_chk
    CHECK (name <> 'production' OR (requires_approval AND min_approver_role IN ('maintainer', 'admin')));

-- +goose Down
ALTER TABLE environments DROP CONSTRAINT environments_production_approval_chk;
DROP TRIGGER environments_production_approval ON environments;
DROP FUNCTION environments_production_approval();
DELETE FROM environments e
 WHERE (e.name, e.aws_account_id) IN (('staging', '000000000001'), ('production', '000000000002'))
   AND NOT EXISTS (SELECT 1 FROM stacks s WHERE s.environment_id = e.id);
