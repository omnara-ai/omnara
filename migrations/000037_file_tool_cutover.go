package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

func newFileToolCutoverMigration() *goose.Migration {
	return goose.NewGoMigration(37, &goose.GoFunc{RunTx: upFileToolCutover}, nil)
}

var fileToolRenames = [][2]string{
	{"upload_artifact", "upload_file"},
	{"download_artifact", "download_file"},
}

func upFileToolCutover(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, project_id::text, source, source_format, source_hash,
		       definition::text, compiled_definition::text, effective_definition_hash
		FROM agent_configs ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var updates []storedAgentConfig
	seen := make(map[[4]string]string)
	for rows.Next() {
		var config storedAgentConfig
		var projectID string
		if err := rows.Scan(&config.id, &projectID, &config.source, &config.sourceFormat,
			&config.sourceHash, &config.definition, &config.compiledDefinition,
			&config.effectiveDefinitionHash); err != nil {
			return err
		}
		migrated, changed, err := migrateFileToolConfig(config)
		if err != nil {
			return fmt.Errorf("migrate file tools in config %s: %w", config.id, err)
		}
		key := [4]string{projectID, migrated.effectiveDefinitionHash, migrated.sourceFormat, migrated.sourceHash}
		if previous, found := seen[key]; found {
			return fmt.Errorf(
				"file tool migration makes configs %s and %s identical; resolve the collision before retrying",
				previous, config.id,
			)
		}
		seen[key] = config.id
		if changed {
			updates = append(updates, migrated)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
		return err
	}
	for _, config := range updates {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_configs SET source = $2, source_hash = $3, definition = $4::jsonb,
			    compiled_definition = $5::jsonb, effective_definition_hash = $6
			WHERE id = $1::uuid`, config.id, config.source, config.sourceHash,
			config.definition, config.compiledDefinition, config.effectiveDefinitionHash); err != nil {
			return fmt.Errorf("update file tools in config %s: %w", config.id, err)
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
	return err
}

func migrateFileToolConfig(config storedAgentConfig) (storedAgentConfig, bool, error) {
	definition, definitionChanged, err := renameFileToolsJSON(config.definition)
	if err != nil {
		return config, false, err
	}
	compiled, compiledChanged, err := renameFileToolsJSON(config.compiledDefinition)
	if err != nil {
		return config, false, err
	}
	var source []byte
	var sourceChanged bool
	switch config.sourceFormat {
	case "json":
		source, sourceChanged, err = renameFileToolsJSON([]byte(config.source))
	case "yaml":
		source, sourceChanged, err = renameFileToolsYAML([]byte(config.source))
	default:
		err = fmt.Errorf("unsupported source format %q", config.sourceFormat)
	}
	if err != nil {
		return config, false, err
	}
	if !definitionChanged && !compiledChanged && !sourceChanged {
		return config, false, nil
	}
	if !definitionChanged || !compiledChanged || !sourceChanged {
		return config, false, errors.New("legacy file tool names differ between config representations")
	}
	canonical, err := canonicalJSON(config.compiledDefinition)
	if err != nil {
		return config, false, err
	}
	if hashBytes([]byte(config.source)) != config.sourceHash || hashBytes(canonical) != config.effectiveDefinitionHash {
		return config, false, errors.New("stored config hashes do not match content")
	}
	config.source = string(source)
	config.sourceHash = hashBytes(source)
	config.definition = definition
	config.compiledDefinition = compiled
	config.effectiveDefinitionHash = hashBytes(compiled)
	return config, true, nil
}

func renameFileToolsJSON(raw []byte) ([]byte, bool, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, false, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, false, errors.New("config must be an object")
	}
	tools, _ := root["tools"].(map[string]any)
	changed := false
	for _, names := range fileToolRenames {
		value, exists := tools[names[0]]
		if !exists {
			continue
		}
		if current, exists := tools[names[1]]; exists && !reflect.DeepEqual(current, value) {
			return nil, false, fmt.Errorf("tools %s and %s have different settings", names[0], names[1])
		}
		tools[names[1]] = value
		delete(tools, names[0])
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := json.Marshal(root)
	return encoded, true, err
}

func renameFileToolsYAML(raw []byte) ([]byte, bool, error) {
	beforeValue, err := decodeAgentConfigNameMigrationYAML(raw)
	if err != nil {
		return nil, false, err
	}
	before, ok := beforeValue.(map[string]any)
	if !ok {
		return nil, false, errors.New("config must be an object")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, false, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("config must be an object")
	}
	root := document.Content[0]
	if fileToolYAMLHasReferences(root) {
		var value map[string]any
		if err := root.Decode(&value); err != nil {
			return nil, false, err
		}
		if err := root.Encode(value); err != nil {
			return nil, false, err
		}
	}
	var tools *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "tools" {
			tools = root.Content[i+1]
		}
	}
	if tools == nil {
		return raw, false, nil
	}
	if tools.Kind != yaml.MappingNode {
		return nil, false, errors.New("tools must be a mapping")
	}
	changed := false
	for _, names := range fileToolRenames {
		oldIndex, newIndex := -1, -1
		for i := 0; i < len(tools.Content); i += 2 {
			switch tools.Content[i].Value {
			case names[0]:
				oldIndex = i
			case names[1]:
				newIndex = i
			}
		}
		if oldIndex < 0 {
			continue
		}
		if newIndex >= 0 {
			var oldValue, newValue any
			if err := tools.Content[oldIndex+1].Decode(&oldValue); err != nil {
				return nil, false, err
			}
			if err := tools.Content[newIndex+1].Decode(&newValue); err != nil {
				return nil, false, err
			}
			if !reflect.DeepEqual(oldValue, newValue) {
				return nil, false, fmt.Errorf("tools %s and %s have different settings", names[0], names[1])
			}
			tools.Content = append(tools.Content[:oldIndex], tools.Content[oldIndex+2:]...)
		} else {
			tools.Content[oldIndex].Value = names[1]
		}
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, false, err
	}
	if err := encoder.Close(); err != nil {
		return nil, false, err
	}
	expectedTools, _ := before["tools"].(map[string]any)
	for _, names := range fileToolRenames {
		if value, exists := expectedTools[names[0]]; exists {
			expectedTools[names[1]] = value
			delete(expectedTools, names[0])
		}
	}
	after, err := decodeAgentConfigNameMigrationYAML(encoded.Bytes())
	if err != nil {
		return nil, false, fmt.Errorf("parse migrated source: %w", err)
	}
	if !reflect.DeepEqual(after, before) {
		return nil, false, errors.New("renaming file tools changed other source values")
	}
	return encoded.Bytes(), true, nil
}

func fileToolYAMLHasReferences(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return true
	}
	for _, child := range node.Content {
		if fileToolYAMLHasReferences(child) {
			return true
		}
	}
	return false
}
