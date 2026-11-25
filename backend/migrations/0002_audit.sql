-- Append-only, hash-chained audit log (docs/03-data-model-and-api.md §3).
--
-- The hash is computed by the Go audit writer (internal/audit, pure function ChainHash):
--   hash = sha256(prev_hash || canonical JSON of the event)
-- The database enforces the invariants the application cannot be trusted to keep:
--   * linkage: prev_hash must equal the hash of the current chain head (serialised by an
--     advisory lock, so seq order == chain order);
--   * immutability: UPDATE, DELETE and TRUNCATE are rejected by triggers;
--   * privileges: the application role may only INSERT and SELECT.

-- +goose Up
CREATE TABLE audit_events (
    seq            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id             uuid        NOT NULL UNIQUE,
    occurred_at    timestamptz NOT NULL,
    actor_type     text        NOT NULL CHECK (actor_type IN ('user', 'system', 'github_actions', 'github_webhook')),
    actor_id       text        NOT NULL,
    actor_ip       inet,
    action         text        NOT NULL,
    subject_type   text        NOT NULL,
    subject_id     text        NOT NULL,
    team_id        uuid,
    correlation_id text,
    payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    prev_hash      bytea       NOT NULL CHECK (length(prev_hash) = 32),
    hash           bytea       NOT NULL UNIQUE CHECK (length(hash) = 32)
);
CREATE INDEX audit_events_subject_idx ON audit_events (subject_type, subject_id, seq DESC);
CREATE INDEX audit_events_team_idx    ON audit_events (team_id, seq DESC);
CREATE INDEX audit_events_action_idx  ON audit_events (action, occurred_at DESC);

-- +goose StatementBegin
CREATE FUNCTION audit_events_link_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    head bytea;
BEGIN
    PERFORM pg_advisory_xact_lock(727274);   -- 'audit' chain lock, shared with the Go writer
    SELECT hash INTO head FROM audit_events ORDER BY seq DESC LIMIT 1;
    IF NEW.prev_hash IS DISTINCT FROM COALESCE(head, decode(repeat('00', 32), 'hex')) THEN
        RAISE EXCEPTION 'audit_events: prev_hash does not match chain head'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER audit_events_link_trg BEFORE INSERT ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_link_check();

-- +goose StatementBegin
CREATE FUNCTION audit_events_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only' USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd

CREATE TRIGGER audit_events_no_update BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
CREATE TRIGGER audit_events_no_truncate BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_immutable();

-- Application role: INSERT and SELECT only. Created when the migrating role is allowed to.
-- +goose StatementBegin
DO $$
BEGIN
    -- Roles are cluster-wide: two databases migrating at once can both try to create it.
    BEGIN
        CREATE ROLE iacp_app NOLOGIN;
    EXCEPTION
        WHEN duplicate_object OR unique_violation THEN
            NULL;  -- created by another database or a concurrent migration
        WHEN insufficient_privilege THEN
            RAISE NOTICE 'skipping iacp_app role creation: insufficient privilege';
    END;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        REVOKE ALL ON audit_events FROM iacp_app;
        GRANT INSERT, SELECT ON audit_events TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE audit_events;
DROP FUNCTION audit_events_immutable();
DROP FUNCTION audit_events_link_check();
