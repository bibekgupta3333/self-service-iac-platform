-- GitHub webhook delivery de-duplication and the outbox the in-process worker drains
-- (docs/03-data-model-and-api.md §2, WBS 4.8/4.9).
--
-- outbox doubles as the job queue while the platform is local-first (ADR-0009): the
-- request service enqueues "request.publish" in the request-creation transaction, the
-- webhook ingester enqueues "github.event" commands, and the worker claims rows with
-- FOR UPDATE SKIP LOCKED. Publishing rows to SQS instead is a later adapter.
--
-- Privileges (least privilege for iacp_app):
--   webhook_deliveries: SELECT, INSERT, and DELETE for the 7-day purge; never UPDATE.
--   outbox:             SELECT, INSERT, UPDATE (claim, complete, retry); never DELETE.

-- +goose Up
CREATE TABLE webhook_deliveries (
    delivery_id uuid        PRIMARY KEY,                 -- X-GitHub-Delivery
    event       text        NOT NULL CHECK (length(event) BETWEEN 1 AND 64),
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_received_idx ON webhook_deliveries (received_at);

CREATE TABLE outbox (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    topic        text        NOT NULL CHECK (topic ~ '^[a-z][a-z0-9_.]{1,63}$'),
    aggregate_id uuid,                                    -- request id when known
    dedupe_key   text        CHECK (length(dedupe_key) <= 200),
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),      -- not claimed before (backoff)
    locked_until timestamptz,                             -- lease of the claiming worker
    attempts     int         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error   text,
    processed_at timestamptz,                             -- handled successfully
    dead_at      timestamptz,                             -- gave up after the last attempt
    CHECK (processed_at IS NULL OR dead_at IS NULL)
);
CREATE INDEX outbox_pending_idx ON outbox (available_at, id) WHERE processed_at IS NULL AND dead_at IS NULL;
-- At most one pending job per dedupe key (e.g. one publish per request).
CREATE UNIQUE INDEX outbox_dedupe_idx ON outbox (dedupe_key)
    WHERE dedupe_key IS NOT NULL AND processed_at IS NULL AND dead_at IS NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        REVOKE ALL ON webhook_deliveries FROM iacp_app;
        GRANT SELECT, INSERT, DELETE ON webhook_deliveries TO iacp_app;
        REVOKE ALL ON outbox FROM iacp_app;
        GRANT SELECT, INSERT, UPDATE ON outbox TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE outbox;
DROP TABLE webhook_deliveries;
