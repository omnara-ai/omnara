-- +goose Up

ALTER TABLE org_managed_work_admission
    ADD COLUMN admission_changed_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    ADD COLUMN delete_machines_after_minutes integer
        CHECK (delete_machines_after_minutes IS NULL OR delete_machines_after_minutes >= 1);

-- +goose StatementBegin
CREATE FUNCTION org_managed_work_admission_set_changed_at() RETURNS trigger AS $$
BEGIN
    NEW.admission_changed_at := statement_timestamp();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER org_managed_work_admission_changed_at
    BEFORE UPDATE OF new_managed_work_allowed ON org_managed_work_admission
    FOR EACH ROW
    WHEN (OLD.new_managed_work_allowed IS DISTINCT FROM NEW.new_managed_work_allowed)
    EXECUTE FUNCTION org_managed_work_admission_set_changed_at();
