-- Browser sessions and OAuth login state (WBS 4.4, docs/03 section 6.1).
--   * sessions: only the SHA-256 of the opaque session id is stored, so a database leak does
--     not yield usable cookies. expires_at is the absolute lifetime; the idle window is
--     last_seen_at plus the configured idle TTL. Logout and disablement set revoked_at.
--   * oauth_login_states: one row per login attempt (state, PKCE verifier, where to return).
--     A row is consumed (deleted) by the callback, so a state is single-use.
-- Rows are runtime data: iacp_app gets the default DML grant (0004). Neither table is audit
-- evidence; login, logout and role changes are appended to audit_events by the application.

-- +goose Up
CREATE TABLE sessions (
    id_hash      bytea       PRIMARY KEY CHECK (length(id_hash) = 32),
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX sessions_user_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

CREATE TABLE oauth_login_states (
    state_hash    bytea       PRIMARY KEY CHECK (length(state_hash) = 32),
    code_verifier text        NOT NULL,
    return_to     text        NOT NULL,
    created_at    timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL
);
CREATE INDEX oauth_login_states_expires_idx ON oauth_login_states (expires_at);

-- +goose Down
DROP TABLE oauth_login_states;
DROP TABLE sessions;
