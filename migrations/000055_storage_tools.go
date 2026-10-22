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
	"unicode/utf8"

	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

func newStorageToolsMigration() *goose.Migration {
	return goose.NewGoMigration(55, &goose.GoFunc{RunDB: upStorageTools}, nil)
}

var storageToolRenames = [][2]string{
	{"read_file", "storage_read"},
	{"write_file", "storage_write"},
	{"list_files", "storage_list"},
	{"search_files", "storage_search"},
	{"upload_file", "storage_upload"},
	{"download_file", "storage_download"},
}

// upStorageTools renames the file tools in stored configs, then builds the memory read-tracking
// index concurrently, which cannot run inside the rename transaction.
func upStorageTools(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := renameStorageTools(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Memory file read tracking looks up storage_read, storage_write, storage_upload and storage_download
	// calls by path. Model-supplied paths are unbounded, so index their hash to stay within the btree
	// row size limit.
	for _, statement := range []string{
		`DROP INDEX CONCURRENTLY IF EXISTS tool_calls_memory_file_idx`,
		`CREATE INDEX CONCURRENTLY tool_calls_memory_file_idx
			ON tool_calls(agent_id, md5(input->>'path'))
			WHERE type = 'built_in'
			  AND name IN ('storage_read', 'storage_write', 'storage_upload', 'storage_download')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

type storageToolConfig struct {
	id, projectID, hash        string
	source, format, sourceHash sql.NullString
	compiled                   []byte
}

type storageToolConfigKey struct {
	projectID, hash    string
	format, sourceHash sql.NullString
}

func renameStorageTools(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, project_id::text, source, source_format, source_hash,
		       compiled_definition::text, effective_definition_hash
		FROM agent_configs ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var updates []storageToolConfig
	keys := make(map[storageToolConfigKey]string)
	for rows.Next() {
		var config storageToolConfig
		if err := rows.Scan(&config.id, &config.projectID, &config.source, &config.format,
			&config.sourceHash, &config.compiled, &config.hash); err != nil {
			return err
		}
		migrated, changed, err := migrateStorageToolConfig(config)
		if err != nil {
			return fmt.Errorf("migrate storage tools in config %s: %w", config.id, err)
		}
		key := storageToolConfigKey{migrated.projectID, migrated.hash, migrated.format, migrated.sourceHash}
		if previousID, exists := keys[key]; exists {
			return fmt.Errorf("storage tool migration would make configs %s and %s duplicates", previousID, config.id)
		}
		keys[key] = config.id
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
		if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET source=$2, source_hash=$3,
			compiled_definition=$4::jsonb, effective_definition_hash=$5 WHERE id=$1::uuid`,
			config.id, config.source, config.sourceHash, config.compiled, config.hash); err != nil {
			return fmt.Errorf(
				"update storage tools in config %s (resolve duplicate configs before retrying if necessary): %w",
				config.id, err,
			)
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
	return err
}

// migrateStorageToolConfig renames the tools in a config's compiled definition and, when present,
// its source. Compiled configs can include default storage tools that their source omits.
func migrateStorageToolConfig(config storageToolConfig) (storageToolConfig, bool, error) {
	compiled, compiledChanged, err := renameStorageToolsJSON(config.compiled)
	if err != nil {
		return config, false, err
	}
	var source []byte
	var sourceChanged bool
	if config.source.Valid {
		switch config.format.String {
		case "json":
			source, sourceChanged, err = renameStorageToolsJSONSource([]byte(config.source.String))
		case "yaml":
			source, sourceChanged, err = renameStorageToolsYAML([]byte(config.source.String))
		default:
			err = fmt.Errorf("unsupported source format %q", config.format.String)
		}
		if err != nil {
			return config, false, err
		}
	}
	if !compiledChanged && !sourceChanged {
		return config, false, nil
	}
	oldHash, err := explicitDefaultToolsConfigHash(config.compiled)
	if err != nil {
		return config, false, err
	}
	if oldHash != config.hash ||
		(config.source.Valid && hashBytes([]byte(config.source.String)) != config.sourceHash.String) {
		return config, false, errors.New("stored config hashes do not match content")
	}
	config.compiled = compiled
	if config.hash, err = explicitDefaultToolsConfigHash(compiled); err != nil {
		return config, false, err
	}
	if sourceChanged {
		config.source.String, config.sourceHash.String = string(source), hashBytes(source)
	}
	return config, true, nil
}

func renameStorageToolsJSON(raw []byte) ([]byte, bool, error) {
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
	for _, names := range storageToolRenames {
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

type storageToolKeyEdit struct {
	start, end int
	name       string
}

// Keep saved JSON text distinct even when two sources decode to the same value.
// Only compiled JSON may be reserialized; source hashes cover the original text.
func renameStorageToolsJSONSource(raw []byte) ([]byte, bool, error) {
	expected, changed, err := renameStorageToolsJSON(raw)
	if err != nil || !changed {
		return raw, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return nil, false, err
	}
	var edits []storageToolKeyEdit
	foundTools := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, false, err
		}
		if key == "tools" {
			if foundTools {
				return nil, false, errors.New("config has duplicate tools fields")
			}
			foundTools = true
			edits, err = storageToolJSONKeyEdits(decoder, raw)
			if err != nil {
				return nil, false, err
			}
		} else {
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return nil, false, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false, err
	}
	encoded := applyStorageToolKeyEdits(raw, edits)
	after, err := decodeAgentConfigNameMigrationJSON(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("parse migrated source: %w", err)
	}
	want, err := decodeAgentConfigNameMigrationJSON(expected)
	if err != nil {
		return nil, false, err
	}
	if !reflect.DeepEqual(after, want) {
		return nil, false, errors.New("renaming storage tools changed other source values")
	}
	return encoded, true, nil
}

func storageToolJSONKeyEdits(decoder *json.Decoder, raw []byte) ([]storageToolKeyEdit, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('{') {
		return nil, errors.New("tools must be an object")
	}
	present := make(map[string]bool)
	var edits []storageToolKeyEdit
	for decoder.More() {
		start := int(decoder.InputOffset())
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("tool key must be a string")
		}
		if present[name] {
			return nil, fmt.Errorf("tool %s is set more than once", name)
		}
		present[name] = true
		end := int(decoder.InputOffset())
		for _, names := range storageToolRenames {
			if name == names[0] {
				// InputOffset starts before the comma/whitespace preceding this key.
				quote := bytes.IndexByte(raw[start:end], '"')
				if quote < 0 {
					return nil, errors.New("tool key has no opening quote")
				}
				edits = append(edits, storageToolKeyEdit{start: start + quote + 1, end: end - 1, name: names[1]})
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	for _, names := range storageToolRenames {
		if present[names[0]] && present[names[1]] {
			return nil, fmt.Errorf("tools %s and %s are both set", names[0], names[1])
		}
	}
	return edits, nil
}

func applyStorageToolKeyEdits(raw []byte, edits []storageToolKeyEdit) []byte {
	encoded := slices.Clone(raw)
	for _, edit := range slices.Backward(edits) {
		encoded = slices.Concat(encoded[:edit.start], []byte(edit.name), encoded[edit.end:])
	}
	return encoded
}

// renameStorageToolsYAML rewrites only the tool keys in place, because re-encoding the document can
// change other values, such as folded block scalars with more-indented lines.
func renameStorageToolsYAML(raw []byte) ([]byte, bool, error) {
	beforeValue, err := decodeAgentConfigNameMigrationYAML(raw)
	if err != nil {
		return nil, false, err
	}
	before, ok := beforeValue.(map[string]any)
	if !ok {
		return nil, false, errors.New("config must be an object")
	}
	expectedTools, _ := before["tools"].(map[string]any)
	changed := false
	for _, names := range storageToolRenames {
		if value, exists := expectedTools[names[0]]; exists {
			if _, exists := expectedTools[names[1]]; exists {
				return nil, false, fmt.Errorf("tools %s and %s are both set", names[0], names[1])
			}
			expectedTools[names[1]] = value
			delete(expectedTools, names[0])
			changed = true
		}
	}
	if !changed {
		return raw, false, nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, false, err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("config must be an object")
	}
	root := document.Content[0]
	var tools *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "tools" {
			tools = root.Content[i+1]
		}
	}
	if tools == nil {
		return nil, false, errors.New("legacy tool keys inherited through YAML merges are not supported")
	}
	if tools.Kind != yaml.MappingNode {
		return nil, false, errors.New("tools must be a mapping")
	}
	present := make(map[string]bool, len(tools.Content)/2)
	for i := 0; i < len(tools.Content); i += 2 {
		present[tools.Content[i].Value] = true
	}
	var edits []storageToolKeyEdit
	for i := 0; i < len(tools.Content); i += 2 {
		key := tools.Content[i]
		for _, names := range storageToolRenames {
			if key.Value != names[0] {
				continue
			}
			if present[names[1]] {
				return nil, false, fmt.Errorf("tools %s and %s are both set", names[0], names[1])
			}
			start, err := yamlNodeOffset(raw, key)
			if err != nil {
				return nil, false, err
			}
			if key.Style == yaml.DoubleQuotedStyle || key.Style == yaml.SingleQuotedStyle {
				start++
			}
			end := start + len(names[0])
			if end > len(raw) || string(raw[start:end]) != names[0] {
				return nil, false, fmt.Errorf("tool key %s is not written literally", names[0])
			}
			edits = append(edits, storageToolKeyEdit{start: start, end: end, name: names[1]})
		}
	}
	if len(edits) == 0 {
		return nil, false, errors.New("legacy tool keys inherited through YAML merges are not supported")
	}
	encoded := applyStorageToolKeyEdits(raw, edits)
	after, err := decodeAgentConfigNameMigrationYAML(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("parse migrated source: %w", err)
	}
	if !reflect.DeepEqual(after, before) {
		return nil, false, errors.New("renaming storage tools changed other source values")
	}
	return encoded, true, nil
}

// yamlNodeOffset converts a node's 1-based line and character column to a byte offset.
func yamlNodeOffset(raw []byte, node *yaml.Node) (int, error) {
	offset := 0
	for line := 1; line < node.Line; line++ {
		next := bytes.IndexByte(raw[offset:], '\n')
		if next < 0 {
			return 0, errors.New("yaml node line is out of range")
		}
		offset += next + 1
	}
	for column := 1; column < node.Column; column++ {
		_, size := utf8.DecodeRune(raw[offset:])
		if size == 0 || raw[offset] == '\n' {
			return 0, errors.New("yaml node column is out of range")
		}
		offset += size
	}
	return offset, nil
}
