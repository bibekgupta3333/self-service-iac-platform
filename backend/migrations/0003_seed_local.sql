-- Reference data needed in every installation.
--   * iacp-bot: the system user that owns platform-initiated actions (docs/03 §1).
--   * development: the only environment while the platform is local-first (ADR-0009).
--     000000000000 is the account id LocalStack reports; it is replaced when the
--     environment moves to a real AWS account.

-- +goose Up
INSERT INTO users (github_user_id, github_login, display_name, is_system)
VALUES (0, 'iacp-bot', 'Platform bot', true)
ON CONFLICT (github_login) DO NOTHING;

INSERT INTO environments (name, aws_account_id, default_region, allowed_regions, requires_approval,
                          min_approver_role, allow_ephemeral, max_ttl_hours)
VALUES ('development', '000000000000', 'us-east-1', ARRAY['us-east-1'], false, 'developer', true, 168)
ON CONFLICT (name) DO NOTHING;

-- +goose Down
DELETE FROM environments WHERE name = 'development' AND aws_account_id = '000000000000';
DELETE FROM users WHERE github_login = 'iacp-bot' AND is_system;
