package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

const (
	AgentConfigNameMigrationFile    = "000026_migrate_agent_config_names.go"
	AgentConfigNameMigrationVersion = 26
)

// Goose discovers Go migrations through registration; dbmigrate uses the scoped constructor below.
func init() {
	goose.AddMigrationContext(upMigrateAgentConfigNames, nil)
}

func NewAgentConfigNameMigration() *goose.Migration {
	migration := goose.NewGoMigration(
		AgentConfigNameMigrationVersion,
		&goose.GoFunc{RunTx: upMigrateAgentConfigNames},
		nil,
	)
	migration.Source = AgentConfigNameMigrationFile
	return migration
}

type storedAgentConfig struct {
	id                      string
	source                  string
	sourceFormat            string
	sourceHash              string
	definition              []byte
	compiledDefinition      []byte
	effectiveDefinitionHash string
}

func upMigrateAgentConfigNames(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock stored agent configs: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id::text,
			source,
			source_format,
			source_hash,
			definition::text,
			compiled_definition::text,
			effective_definition_hash
		FROM agent_configs
		WHERE definition ? 'name'
		   OR compiled_definition ? 'name'
		   OR (source_format = 'yaml' AND source ~ E'(^|\\n)name:[^\\r\\n]*(\\r?\\n|$)')
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list legacy stored agent configs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	migratedConfigs := make([]storedAgentConfig, 0)
	for rows.Next() {
		var config storedAgentConfig
		if err := rows.Scan(
			&config.id,
			&config.source,
			&config.sourceFormat,
			&config.sourceHash,
			&config.definition,
			&config.compiledDefinition,
			&config.effectiveDefinitionHash,
		); err != nil {
			return fmt.Errorf("scan legacy stored agent config: %w", err)
		}
		migrated, err := migrateStoredAgentConfig(config)
		if err != nil {
			return fmt.Errorf("migrate stored agent config %s: %w", config.id, err)
		}
		migratedConfigs = append(migratedConfigs, migrated)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy stored agent configs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy stored agent configs: %w", err)
	}

	if len(migratedConfigs) > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
			return fmt.Errorf("disable stored agent config immutability: %w", err)
		}
		for _, config := range migratedConfigs {
			if _, err := tx.ExecContext(ctx, `
				UPDATE agent_configs
				SET source = $2,
					source_hash = $3,
					definition = $4::jsonb,
					compiled_definition = $5::jsonb,
					effective_definition_hash = $6
				WHERE id = $1::uuid`,
				config.id,
				config.source,
				config.sourceHash,
				config.definition,
				config.compiledDefinition,
				config.effectiveDefinitionHash,
			); err != nil {
				return fmt.Errorf("update stored agent config %s: %w", config.id, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`); err != nil {
			return fmt.Errorf("restore stored agent config immutability: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		ALTER TABLE agent_configs ALTER COLUMN source DROP DEFAULT;
		ALTER TABLE agent_configs ADD CONSTRAINT agent_configs_source_required CHECK (source <> '');
	`); err != nil {
		return fmt.Errorf("enforce stored agent config source: %w", err)
	}
	return nil
}

func migrateStoredAgentConfig(config storedAgentConfig) (storedAgentConfig, error) {
	if config.sourceHash != hashBytes([]byte(config.source)) {
		return storedAgentConfig{}, errors.New("source hash does not match source")
	}
	currentCompiled, err := canonicalJSON(config.compiledDefinition)
	if err != nil {
		return storedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}
	if config.effectiveDefinitionHash != hashBytes(currentCompiled) {
		return storedAgentConfig{}, errors.New("effective definition hash does not match compiled definition")
	}
	migratedSource, sourceChanged, err := migrateAgentConfigSource(
		config.sourceFormat,
		[]byte(config.source),
	)
	if err != nil {
		return storedAgentConfig{}, fmt.Errorf("source: %w", err)
	}
	migratedDefinition, definitionChanged, err := removeTopLevelJSONName(config.definition)
	if err != nil {
		return storedAgentConfig{}, fmt.Errorf("definition: %w", err)
	}
	migratedCompiled, compiledChanged, err := removeTopLevelJSONName(config.compiledDefinition)
	if err != nil {
		return storedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}
	if !sourceChanged || !definitionChanged || !compiledChanged {
		return storedAgentConfig{}, errors.New("legacy top-level name markers are inconsistent")
	}
	definitionCanonical, err := canonicalJSON(migratedDefinition)
	if err != nil {
		return storedAgentConfig{}, fmt.Errorf("definition: %w", err)
	}
	compiledCanonical, err := canonicalJSON(migratedCompiled)
	if err != nil {
		return storedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}

	config.source = string(migratedSource)
	config.sourceHash = hashBytes(migratedSource)
	config.definition = definitionCanonical
	config.compiledDefinition = compiledCanonical
	config.effectiveDefinitionHash = hashBytes(compiledCanonical)
	return config, nil
}

func migrateAgentConfigSource(
	format string,
	raw []byte,
) ([]byte, bool, error) {
	if format != "yaml" {
		return nil, false, fmt.Errorf("unsupported legacy agent config source format %q", format)
	}
	beforeValue, err := decodeAgentConfigNameMigrationYAML(raw)
	if err != nil {
		return nil, false, err
	}
	before, ok := beforeValue.(map[string]any)
	if !ok {
		return nil, false, errors.New("agent config source must be an object")
	}
	legacyName, hasLegacyName := before["name"]
	if !hasLegacyName {
		return raw, false, nil
	}
	if _, ok := legacyName.(string); !ok {
		return nil, false, errors.New("top-level name must be a string")
	}
	delete(before, "name")

	migrated, err := removeSimpleTopLevelYAMLName(raw)
	if err != nil {
		return nil, false, err
	}
	afterValue, err := decodeAgentConfigNameMigrationYAML(migrated)
	if err != nil {
		return nil, false, fmt.Errorf("parse migrated source: %w", err)
	}
	after, ok := afterValue.(map[string]any)
	if !ok {
		return nil, false, errors.New("migrated agent config source must be an object")
	}
	if !reflect.DeepEqual(after, before) {
		return nil, false, errors.New("removing top-level name changed other source values")
	}
	return migrated, true, nil
}

func removeSimpleTopLevelYAMLName(raw []byte) ([]byte, error) {
	nameStart := -1
	nameEnd := -1
	for start := 0; start < len(raw); {
		end := len(raw)
		if offset := bytes.IndexByte(raw[start:], '\n'); offset >= 0 {
			end = start + offset + 1
		}
		line := bytes.TrimSuffix(raw[start:end], []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if bytes.HasPrefix(line, []byte("name:")) {
			if nameStart >= 0 {
				return nil, errors.New("top-level name must not be repeated")
			}
			nameStart, nameEnd = start, end
		}
		start = end
	}
	if nameStart < 0 {
		return nil, errors.New("top-level name must be a standalone scalar line")
	}
	migrated := make([]byte, 0, len(raw)-(nameEnd-nameStart))
	migrated = append(migrated, raw[:nameStart]...)
	migrated = append(migrated, raw[nameEnd:]...)
	return migrated, nil
}

func decodeAgentConfigNameMigrationJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse agent config JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse agent config JSON: trailing value")
		}
		return nil, fmt.Errorf("parse agent config JSON: %w", err)
	}
	return value, nil
}

func decodeAgentConfigNameMigrationYAML(raw []byte) (any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse agent config YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse agent config YAML: trailing document")
		}
		return nil, fmt.Errorf("parse agent config YAML: %w", err)
	}
	return value, nil
}

func removeTopLevelJSONName(raw []byte) ([]byte, bool, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, false, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, false, errors.New("value must be an object")
	}
	if _, ok := root["name"]; !ok {
		return raw, false, nil
	}
	delete(root, "name")
	migrated, err := json.Marshal(root)
	if err != nil {
		return nil, false, fmt.Errorf("encode JSON: %w", err)
	}
	return migrated, true, nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical JSON: %w", err)
	}
	return canonical, nil
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
