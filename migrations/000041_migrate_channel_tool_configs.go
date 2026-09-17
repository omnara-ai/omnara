package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

func newChannelToolConfigMigration() *goose.Migration {
	return goose.NewGoMigration(41, &goose.GoFunc{RunTx: upMigrateChannelToolConfigs}, nil)
}

// Migration 39 permits configs without authored source. Keep this row shape
// local to the channel cutover so earlier, released migrations stay frozen.
type storedChannelToolConfig struct {
	id                      string
	source                  sql.NullString
	sourceFormat            sql.NullString
	sourceHash              sql.NullString
	definition              []byte
	compiledDefinition      []byte
	effectiveDefinitionHash string
}

// The offline Slack cutover deliberately migrates saved configs in place. Their
// IDs remain valid for profiles, input history and unfinished model contexts.
// Replacement channel tools come from live bindings, never config declarations.
// Like migration 26, this is a transactional exception to config immutability;
// runtime readers retain no compatibility rule for the retired builtin names.
// Source JSON/YAML formatting may normalize; only the retired declarations
// change semantically. Do not recompile names against current resource identities.
func upMigrateChannelToolConfigs(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock channel tool configs: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id::text, source, source_format, source_hash,
    definition::text, compiled_definition::text, effective_definition_hash
FROM agent_configs
WHERE compiled_definition -> 'tools' ?| ARRAY['send_integration_message', 'set_integration_target']
ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list channel tool configs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var configs []storedChannelToolConfig
	for rows.Next() {
		var config storedChannelToolConfig
		if err := rows.Scan(&config.id, &config.source, &config.sourceFormat, &config.sourceHash,
			&config.definition, &config.compiledDefinition, &config.effectiveDefinitionHash); err != nil {
			return fmt.Errorf("scan channel tool config: %w", err)
		}
		migrated, changed, err := migrateChannelToolConfig(config)
		if err != nil {
			return fmt.Errorf("migrate channel tool config %s: %w", config.id, err)
		}
		if changed {
			configs = append(configs, migrated)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate channel tool configs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(configs) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
		return fmt.Errorf("disable config immutability for channel cutover: %w", err)
	}
	for _, config := range configs {
		// Existing uniqueness constraints deliberately reject converging configs.
		// Rehearse and resolve those cases explicitly instead of merging history.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET source = $2, source_hash = $3,
    definition = $4::jsonb, compiled_definition = $5::jsonb, effective_definition_hash = $6
WHERE id = $1::uuid`, config.id, config.source, config.sourceHash,
			config.definition, config.compiledDefinition, config.effectiveDefinitionHash); err != nil {
			return fmt.Errorf("update channel tool config %s: %w", config.id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`); err != nil {
		return fmt.Errorf("restore config immutability after channel cutover: %w", err)
	}
	return nil
}

func migrateChannelToolConfig(config storedChannelToolConfig) (storedChannelToolConfig, bool, error) {
	if config.source.Valid != config.sourceFormat.Valid || config.source.Valid != config.sourceHash.Valid {
		return config, false, errors.New("source, source format and source hash must be present together")
	}
	if config.source.Valid && config.sourceHash.String != hashBytes([]byte(config.source.String)) {
		return config, false, errors.New("source hash does not match source")
	}
	compiledHash, err := channelToolCompiledHash(config.compiledDefinition)
	if err != nil {
		return config, false, err
	}
	if config.effectiveDefinitionHash != compiledHash {
		return config, false, errors.New("effective definition hash does not match compiled definition")
	}
	compiled, names, err := removeChannelToolJSON(config.compiledDefinition)
	if err != nil || len(names) == 0 {
		return config, false, err
	}
	definition, definitionNames, err := removeChannelToolJSON(config.definition)
	if err != nil {
		return config, false, err
	}
	if !slices.Equal(names, definitionNames) {
		return config, false, errors.New("retired builtin declarations disagree across source and definitions")
	}
	var source []byte
	if config.source.Valid {
		var sourceNames []string
		switch config.sourceFormat.String {
		case "json":
			source, sourceNames, err = removeChannelToolJSON([]byte(config.source.String))
		case "yaml":
			source, sourceNames, err = removeChannelToolYAML([]byte(config.source.String))
		default:
			err = errors.New("unsupported channel tool config source format")
		}
		if err != nil {
			return config, false, err
		}
		if !slices.Equal(names, sourceNames) {
			return config, false, errors.New("retired builtin declarations disagree across source and definitions")
		}
	}
	compiledHash, err = channelToolCompiledHash(compiled)
	if err != nil {
		return config, false, err
	}
	if config.source.Valid {
		config.source.String, config.sourceHash.String = string(source), hashBytes(source)
	}
	config.definition, config.compiledDefinition = definition, compiled
	config.effectiveDefinitionHash = compiledHash
	return config, true, nil
}

// Freeze the compiler's hash algorithm here. PostgreSQL jsonb may expand numeric
// exponents, so hashing its text with json.Number would differ from the ordinary
// runtime reader. The transform itself preserves numbers using json.Number.
func channelToolCompiledHash(raw []byte) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashBytes(canonical), nil
}

func removeChannelToolDeclarations(value any) ([]string, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("channel tool config must be an object")
	}
	value, present := root["tools"]
	if !present {
		return nil, nil
	}
	tools, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("channel tool config tools must be an object")
	}
	var removed []string
	for _, name := range []string{"send_integration_message", "set_integration_target"} {
		value, present := tools[name]
		if !present {
			continue
		}
		tool, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("retired tool %s must be an object", name)
		}
		if tool["type"] == "custom" {
			continue
		}
		if kind := tool["type"]; kind != nil && kind != "" && kind != "built_in" {
			return nil, fmt.Errorf("retired tool %s has an unsupported type", name)
		}
		if _, hasSchema := tool["input_schema"]; hasSchema {
			return nil, fmt.Errorf("retired builtin %s unexpectedly defines an input schema", name)
		}
		delete(tools, name)
		removed = append(removed, name)
	}
	if len(removed) > 0 && len(tools) == 0 {
		delete(root, "tools")
	}
	return removed, nil
}

func removeChannelToolJSON(raw []byte) ([]byte, []string, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	names, err := removeChannelToolDeclarations(value)
	if err != nil {
		return nil, nil, err
	}
	if len(names) == 0 {
		return raw, nil, nil
	}
	migrated, err := json.Marshal(value)
	return migrated, names, err
}

func removeChannelToolYAML(raw []byte) ([]byte, []string, error) {
	expected, err := decodeAgentConfigNameMigrationYAML(raw)
	if err != nil {
		return nil, nil, err
	}
	names, err := removeChannelToolDeclarations(expected)
	if err != nil || len(names) == 0 {
		return raw, names, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, nil, err
	}
	root := document.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "tools" {
			continue
		}
		tools := root.Content[i+1]
		if tools.Kind != yaml.MappingNode {
			return nil, nil, errors.New("YAML tools must be a direct mapping for channel cutover")
		}
		for j := 0; j < len(tools.Content); {
			if slices.Contains(names, tools.Content[j].Value) {
				tools.Content = slices.Delete(tools.Content, j, j+2)
			} else {
				j += 2
			}
		}
		if len(tools.Content) == 0 {
			root.Content = slices.Delete(root.Content, i, i+2)
		}
		break
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}
	actual, err := decodeAgentConfigNameMigrationYAML(output.Bytes())
	if err != nil || !reflect.DeepEqual(expected, actual) {
		return nil, nil, errors.New("removing retired tools changed other YAML source values")
	}
	return output.Bytes(), names, nil
}
