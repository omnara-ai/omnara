-- +goose Up

ALTER TABLE machines
    ADD COLUMN delete_reason_code text,
    ADD COLUMN delete_reason_message text NOT NULL DEFAULT '';

UPDATE machines
SET delete_reason_code = lifecycle_reason_code,
    delete_reason_message = lifecycle_reason_message
WHERE source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state = 'deleting'
  AND lifecycle_reason_code NOT IN ('', 'deleting_retry', 'delete_failed_retry');

UPDATE machines
SET delete_reason_code = 'unknown'
WHERE source_kind = 'pool'
  AND deleted_at IS NULL
  AND lifecycle_state IN ('deleting', 'delete_failed')
  AND delete_reason_code IS NULL;

ALTER TABLE machines
    ADD CONSTRAINT machines_delete_reason_check CHECK (
        lifecycle_state NOT IN ('deleting', 'delete_failed')
        OR (delete_reason_code IS NOT NULL AND delete_reason_code <> '')
    ) NOT VALID;

-- +goose StatementBegin
CREATE FUNCTION machines_record_delete_reason() RETURNS trigger AS $$
BEGIN
    NEW.delete_reason_code := NEW.lifecycle_reason_code;
    NEW.delete_reason_message := NEW.lifecycle_reason_message;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER machines_record_delete_reason
    BEFORE UPDATE OF lifecycle_state ON machines
    FOR EACH ROW
    WHEN (
        OLD.lifecycle_state NOT IN ('deleting', 'delete_failed', 'deleted')
        AND NEW.lifecycle_state IN ('deleting', 'deleted')
    )
    EXECUTE FUNCTION machines_record_delete_reason();
