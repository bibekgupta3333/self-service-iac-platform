-- Retention for the outbox (WBS 4.9 follow-up).
--
-- iacp_app has no DELETE on outbox (0008): a compromised API must not be able to erase
-- pending or dead jobs. Retention of finished rows therefore goes through a SECURITY
-- DEFINER function owned by the migration role. It can only delete rows that were handled
-- successfully (processed_at set) and are older than the cutoff, in bounded batches, and
-- it never touches pending or dead jobs, which operators still need to see. The cutoff is
-- clamped in SQL to at least one hour in the past, so a caller cannot pass a future cutoff
-- to drop everything processed at once.
--
-- webhook_deliveries already grants DELETE (0008), so its purge needs nothing here.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION purge_processed_outbox(cutoff timestamptz, max_rows int)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
    WITH doomed AS (
        SELECT id FROM outbox
        WHERE processed_at IS NOT NULL
          AND processed_at < least(cutoff, now() - interval '1 hour')
        ORDER BY id
        LIMIT greatest(max_rows, 0)
        FOR UPDATE SKIP LOCKED
    ), gone AS (
        DELETE FROM outbox WHERE id IN (SELECT id FROM doomed) RETURNING 1
    )
    SELECT count(*) FROM gone
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION purge_processed_outbox(timestamptz, int) FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iacp_app') THEN
        GRANT EXECUTE ON FUNCTION purge_processed_outbox(timestamptz, int) TO iacp_app;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION purge_processed_outbox(timestamptz, int);
