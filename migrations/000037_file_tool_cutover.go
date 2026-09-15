package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"unicode/utf8"

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
		if _, exists := tools[names[1]]; exists {
			return nil, false, fmt.Errorf(
				"tools contains both %s and %s; resolve the duplicate before migrating", names[0], names[1],
			)
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
	expectedTools, _ := before["tools"].(map[string]any)
	changed := false
	for _, names := range fileToolRenames {
		value, exists := expectedTools[names[0]]
		if !exists {
			continue
		}
		if _, exists := expectedTools[names[1]]; exists {
			return nil, false, fmt.Errorf(
				"tools contains both %s and %s; resolve the duplicate before migrating", names[0], names[1],
			)
		}
		expectedTools[names[1]] = value
		delete(expectedTools, names[0])
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, false, err
	}
	tools := fileToolYAMLMappingValue(document.Content[0], "tools")
	var edits []fileToolYAMLEdit
	seen := make(map[*yaml.Node]bool)
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node == nil || seen[node] {
			return nil
		}
		seen[node] = true
		switch node.Kind {
		case yaml.AliasNode:
			return visit(node.Alias)
		case yaml.SequenceNode:
			for _, child := range node.Content {
				if err := visit(child); err != nil {
					return err
				}
			}
		case yaml.MappingNode:
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i]
				if key.Tag == "!!merge" {
					if err := visit(node.Content[i+1]); err != nil {
						return err
					}
					continue
				}
				for _, names := range fileToolRenames {
					if key.Value != names[0] {
						continue
					}
					edit, err := fileToolYAMLKeyEdit(raw, key, names[1])
					if err != nil {
						return err
					}
					edits = append(edits, edit)
				}
			}
		default:
			return errors.New("tools must be a mapping")
		}
		return nil
	}
	if err := visit(tools); err != nil {
		return nil, false, err
	}

	// Source positions belong to the original document. Edit from the end so
	// earlier offsets stay valid; never re-encode unrelated YAML scalars.
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	migrated := bytes.Clone(raw)
	previousStart := len(raw)
	for _, edit := range edits {
		if edit.start < 0 || edit.end > previousStart || edit.start >= edit.end {
			return nil, false, errors.New("overlapping or invalid YAML tool edits")
		}
		migrated = append(append(append([]byte{}, migrated[:edit.start]...), edit.text...), migrated[edit.end:]...)
		previousStart = edit.start
	}
	after, err := decodeAgentConfigNameMigrationYAML(migrated)
	if err != nil {
		return nil, false, fmt.Errorf("parse migrated source: %w", err)
	}
	// Shared anchors can expose a tool key elsewhere in the document. Keep
	// this guard even for byte edits: an unrelated value must never change.
	if !reflect.DeepEqual(after, before) {
		return nil, false, errors.New("renaming file tools changed other source values")
	}
	return migrated, true, nil
}

// Explicit entries override merged entries; the first map in a merge sequence
// wins. Decoding above has already rejected invalid or cyclic YAML references.
func fileToolYAMLMappingValue(node *yaml.Node, name string) *yaml.Node {
	switch node.Kind {
	case yaml.AliasNode:
		return fileToolYAMLMappingValue(node.Alias, name)
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if value := fileToolYAMLMappingValue(child, name); value != nil {
				return value
			}
		}
	case yaml.MappingNode:
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value == name {
				return node.Content[i+1]
			}
		}
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Tag == "!!merge" {
				if value := fileToolYAMLMappingValue(node.Content[i+1], name); value != nil {
					return value
				}
			}
		}
	default:
		return nil
	}
	return nil
}

type fileToolYAMLEdit struct {
	start, end int
	text       string
}

func fileToolYAMLOffset(raw []byte, node *yaml.Node) (int, error) {
	line, column := 1, 1
	// The reader consumes an initial UTF-8 BOM before counting columns.
	initial := 0
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		initial = 3
	}
	for offset := initial; offset < len(raw); {
		if line == node.Line && column == node.Column {
			return offset, nil
		}
		r, size := utf8.DecodeRune(raw[offset:])
		offset += size
		switch r {
		case '\r':
			if offset < len(raw) && raw[offset] == '\n' {
				offset++
			}
			line, column = line+1, 1
		case '\n', '\u0085', '\u2028', '\u2029':
			line, column = line+1, 1
		default:
			column++
		}
	}
	return 0, fmt.Errorf("cannot locate YAML node at %d:%d", node.Line, node.Column)
}

func fileToolYAMLKeyEdit(raw []byte, key *yaml.Node, name string) (fileToolYAMLEdit, error) {
	start, err := fileToolYAMLOffset(raw, key)
	if err != nil {
		return fileToolYAMLEdit{}, err
	}
	end := start + len(key.Value)
	text := name
	if raw[start] == '\'' || raw[start] == '"' {
		quote := raw[start]
		end = start + 1
		for end < len(raw) {
			if raw[end] == quote {
				end++
				if quote == '\'' && end < len(raw) && raw[end] == quote {
					end++
					continue
				}
				break
			}
			if quote == '"' && raw[end] == '\\' {
				end++
			}
			end++
		}
		text = string(quote) + name + string(quote)
	}
	if end > len(raw) {
		return fileToolYAMLEdit{}, errors.New("unterminated YAML tool key")
	}
	var original string
	if err := yaml.Unmarshal(raw[start:end], &original); err != nil || original != key.Value {
		return fileToolYAMLEdit{}, fmt.Errorf("unsupported YAML tool key at %d:%d", key.Line, key.Column)
	}
	return fileToolYAMLEdit{start: start, end: end, text: text}, nil
}
