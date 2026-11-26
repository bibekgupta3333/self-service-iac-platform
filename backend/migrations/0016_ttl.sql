-- TTL for ephemeral stacks (docs/06 §6, WBS 8.4).
--   * stacks.ttl_hours: the lifetime asked for at request time (NULL for a permanent stack).
--   * stacks.ttl_extended_hours: total hours added by extensions, capped by the service.
-- is_ephemeral, expires_at and ttl_warned_at already exist (0001).
--
-- A production stack can never be ephemeral. environments.allow_ephemeral defaults to true,
-- so rather than make every future INSERT of the production row spell out false, a trigger
-- forces it to false for that environment and a CHECK states the invariant.

-- +goose Up
ALTER TABLE stacks
    ADD COLUMN ttl_hours          int CHECK (ttl_hours IS NULL OR ttl_hours > 0),
    ADD COLUMN ttl_extended_hours int NOT NULL DEFAULT 0 CHECK (ttl_extended_hours >= 0),
    ADD CONSTRAINT stacks_ttl_ephemeral_chk CHECK (is_ephemeral OR (ttl_hours IS NULL AND ttl_extended_hours = 0));

UPDATE stacks
   SET ttl_hours = greatest(1, round(extract(epoch FROM expires_at - created_at) / 3600)::int)
 WHERE is_ephemeral;

-- +goose StatementBegin
CREATE FUNCTION environments_no_ephemeral_production() RETURNS trigger AS $$
BEGIN
    IF NEW.name = 'production' THEN
        NEW.allow_ephemeral := false;
        NEW.max_ttl_hours := NULL;
    END IF;
    RETURN NEW;
END
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER environments_no_ephemeral_production
    BEFORE INSERT OR UPDATE ON environments
    FOR EACH ROW EXECUTE FUNCTION environments_no_ephemeral_production();

UPDATE environments SET allow_ephemeral = false WHERE name = 'production';
ALTER TABLE environments
    ADD CONSTRAINT environments_production_not_ephemeral_chk CHECK (name <> 'production' OR NOT allow_ephemeral);

-- +goose Down
ALTER TABLE environments DROP CONSTRAINT environments_production_not_ephemeral_chk;
DROP TRIGGER environments_no_ephemeral_production ON environments;
DROP FUNCTION environments_no_ephemeral_production();
ALTER TABLE stacks
    DROP CONSTRAINT stacks_ttl_ephemeral_chk,
    DROP COLUMN ttl_extended_hours,
    DROP COLUMN ttl_hours;
