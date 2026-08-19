-- +goose Up

ALTER TABLE configured_models ADD COLUMN management_kind text;

UPDATE configured_models configured_model
SET management_kind = provider_config.management_kind
FROM model_provider_configs provider_config
WHERE provider_config.org_id = configured_model.org_id
  AND provider_config.id = configured_model.model_provider_config_id;

ALTER TABLE configured_models ALTER COLUMN management_kind SET NOT NULL;
ALTER TABLE configured_models ADD CONSTRAINT configured_models_management_kind_check
    CHECK (management_kind IN ('tenant', 'cluster'));

-- +goose StatementBegin
CREATE FUNCTION configured_models_reject_authority_change() RETURNS trigger AS $$
BEGIN
    IF NEW.management_kind IS DISTINCT FROM OLD.management_kind THEN
        RAISE EXCEPTION 'configured model authority is immutable'
            USING ERRCODE = '25006';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER configured_models_authority_immutable
    BEFORE UPDATE OF management_kind ON configured_models
    FOR EACH ROW
    EXECUTE FUNCTION configured_models_reject_authority_change();
