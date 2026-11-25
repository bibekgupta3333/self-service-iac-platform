-- Runtime privileges for iacp_app, the role the API connects as (IACP_DATABASE_URL).
-- Migrations run as the schema owner (IACP_MIGRATE_DATABASE_URL). The app gets DML on
-- platform tables but only INSERT and SELECT on audit_events, and nothing on the goose
-- version table, so the append-only guarantee holds even if the app is compromised.
-- The role is created NOLOGIN; the operator (or the API in dev mode) gives it a password.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    BEGIN
        CREATE ROLE iacp_app NOLOGIN;
    EXCEPTION
        WHEN duplicate_object OR unique_violation THEN NULL;
        WHEN insufficient_privilege THEN
            RAISE NOTICE 'skipping iacp_app role creation: insufficient privilege';
    END;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        RAISE NOTICE 'iacp_app does not exist; runtime grants skipped';
        RETURN;
    END IF;

    EXECUTE format('GRANT CONNECT ON DATABASE %I TO iacp_app', current_database());
    GRANT USAGE ON SCHEMA public TO iacp_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO iacp_app;
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO iacp_app;

    REVOKE ALL ON audit_events FROM iacp_app;
    GRANT SELECT, INSERT ON audit_events TO iacp_app;
    REVOKE ALL ON goose_db_version FROM iacp_app;

    -- Tables added by later migrations get the same DML grant. A future append-only table
    -- must REVOKE UPDATE, DELETE explicitly, as audit_events does above.
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO iacp_app;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO iacp_app;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM iacp_app;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM iacp_app;
        REVOKE ALL ON ALL TABLES IN SCHEMA public FROM iacp_app;
        REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM iacp_app;
        GRANT SELECT, INSERT ON audit_events TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd
