-- oidc_jti: GitHub Actions OIDC token ids already used, shared by every API replica so a
-- token replayed against another replica is still rejected (docs/03 §6.9, WBS 4.10).
-- A row lives until the token's exp plus the verifier leeway; rows past expires_at are
-- purged on the next claim.
--
-- Privileges: 0004 gives iacp_app full DML on new tables by default. The replay store
-- only inserts, purges expired rows and reads expires_at in the purge predicate, so
-- UPDATE is revoked (least privilege): a recorded id can never be moved forward or back.

-- +goose Up
CREATE TABLE oidc_jti (
    jti        text        PRIMARY KEY CHECK (length(jti) BETWEEN 1 AND 128),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oidc_jti_expiry_idx ON oidc_jti (expires_at);

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        REVOKE ALL ON oidc_jti FROM iacp_app;
        GRANT SELECT, INSERT, DELETE ON oidc_jti TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE oidc_jti;
