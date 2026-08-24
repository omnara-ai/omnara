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
	"strings"
	"unicode/utf8"

	"github.com/pressly/goose/v3"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	AgentConfigNameMigrationFile          = "000026_migrate_agent_config_names.go"
	AgentConfigNameMigrationVersion       = 26
	agentConfigNameMigrationMaxCodePoints = 64
)

type agentConfigNameMigrationSourceFormat string

const (
	agentConfigNameMigrationSourceFormatJSON agentConfigNameMigrationSourceFormat = "json"
	agentConfigNameMigrationSourceFormatYAML agentConfigNameMigrationSourceFormat = "yaml"
)

// Goose's source validator requires init registration; dbmigrate separately uses the scoped provider below.
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
	projectID               string
	source                  string
	sourceFormat            string
	sourceHash              string
	definition              []byte
	compiledDefinition      []byte
	effectiveDefinitionHash string
}

type migratedAgentConfig struct {
	storedAgentConfig
	changed bool
}

func upMigrateAgentConfigNames(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock stored agent configs: %w", err)
	}
	configs, indexes, err := listAgentConfigMigrationKeys(ctx, tx)
	if err != nil {
		return err
	}
	legacyRows, err := tx.QueryContext(ctx, `
		SELECT
			id::text,
			project_id::text,
			source,
			source_format,
			source_hash,
			definition::text,
			compiled_definition::text,
			effective_definition_hash
		FROM agent_configs
		WHERE definition ? 'name' OR compiled_definition ? 'name'
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list legacy stored agent configs: %w", err)
	}
	defer func() { _ = legacyRows.Close() }()
	violations := make([]string, 0)
	for legacyRows.Next() {
		var config storedAgentConfig
		if err := legacyRows.Scan(
			&config.id,
			&config.projectID,
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
			violations = append(violations, config.id+" ("+err.Error()+")")
			continue
		}
		index, ok := indexes[config.id]
		if !ok {
			return fmt.Errorf("legacy stored agent config %s was missing from migration keys", config.id)
		}
		configs[index] = migrated
	}
	if err := legacyRows.Err(); err != nil {
		return fmt.Errorf("iterate legacy stored agent configs: %w", err)
	}
	if err := legacyRows.Close(); err != nil {
		return fmt.Errorf("close legacy stored agent configs: %w", err)
	}
	if len(violations) > 0 {
		return storedAgentConfigMigrationError(violations)
	}
	if err := validateMigratedAgentConfigKeys(configs); err != nil {
		return err
	}

	changed := make([]migratedAgentConfig, 0)
	for _, config := range configs {
		if config.changed {
			changed = append(changed, config)
		}
	}
	if len(changed) > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
			return fmt.Errorf("disable stored agent config immutability: %w", err)
		}
		for _, config := range changed {
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
				return fmt.Errorf("migrate stored agent config %s: %w", config.id, err)
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

func listAgentConfigMigrationKeys(
	ctx context.Context,
	tx *sql.Tx,
) ([]migratedAgentConfig, map[string]int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id::text,
			project_id::text,
			source,
			source_format,
			source_hash,
			effective_definition_hash,
			definition ? 'name' OR compiled_definition ? 'name'
		FROM agent_configs
		ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("list stored agent config migration keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	configs := make([]migratedAgentConfig, 0)
	indexes := make(map[string]int)
	violations := make([]string, 0)
	for rows.Next() {
		var config migratedAgentConfig
		var source string
		var storedHasLegacyName bool
		if err := rows.Scan(
			&config.id,
			&config.projectID,
			&source,
			&config.sourceFormat,
			&config.sourceHash,
			&config.effectiveDefinitionHash,
			&storedHasLegacyName,
		); err != nil {
			return nil, nil, fmt.Errorf("scan stored agent config migration key: %w", err)
		}
		root, err := agentConfigNameMigrationSourceObject(
			agentConfigNameMigrationSourceFormat(config.sourceFormat),
			[]byte(source),
		)
		if err == nil {
			err = validateAgentConfigNameMigrationSourceReferences(root)
		}
		if err == nil {
			_, sourceHasLegacyName := root["name"]
			if sourceHasLegacyName && !storedHasLegacyName {
				err = errors.New("source has a legacy top-level name missing from stored definitions")
			}
		}
		if err != nil {
			violations = append(violations, config.id+" (source: "+err.Error()+")")
		}
		indexes[config.id] = len(configs)
		configs = append(configs, config)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate stored agent config migration keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close stored agent config migration keys: %w", err)
	}
	if len(violations) > 0 {
		return nil, nil, storedAgentConfigMigrationError(violations)
	}
	return configs, indexes, nil
}

func migrateStoredAgentConfig(config storedAgentConfig) (migratedAgentConfig, error) {
	migrated := migratedAgentConfig{storedAgentConfig: config}
	if config.sourceHash != hashBytes([]byte(config.source)) {
		return migratedAgentConfig{}, errors.New("source hash does not match source")
	}
	currentCompiled, err := canonicalJSON(config.compiledDefinition)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}
	if config.effectiveDefinitionHash != hashBytes(currentCompiled) {
		return migratedAgentConfig{}, errors.New("effective definition hash does not match compiled definition")
	}
	migratedSource, sourceChanged, err := migrateAgentConfigSource(
		agentConfigNameMigrationSourceFormat(config.sourceFormat),
		[]byte(config.source),
	)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("source: %w", err)
	}
	migratedDefinition, definitionChanged, err := removeTopLevelJSONName(config.definition)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("definition: %w", err)
	}
	migratedCompiled, compiledChanged, err := removeTopLevelJSONName(config.compiledDefinition)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}
	if !sourceChanged || !definitionChanged || !compiledChanged {
		return migratedAgentConfig{}, errors.New("legacy top-level name markers are inconsistent")
	}
	definitionCanonical, err := canonicalJSON(migratedDefinition)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("definition: %w", err)
	}
	compiledCanonical, err := canonicalJSON(migratedCompiled)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}

	migrated.source = string(migratedSource)
	migrated.sourceHash = hashBytes(migratedSource)
	migrated.definition = definitionCanonical
	migrated.compiledDefinition = compiledCanonical
	migrated.effectiveDefinitionHash = hashBytes(compiledCanonical)
	migrated.changed = true
	return migrated, nil
}

func validateMigratedAgentConfigKeys(configs []migratedAgentConfig) error {
	seen := make(map[string]string, len(configs))
	for _, config := range configs {
		key := strings.Join([]string{
			config.projectID,
			config.effectiveDefinitionHash,
			config.sourceFormat,
			config.sourceHash,
		}, "\x00")
		if existingID, ok := seen[key]; ok {
			return fmt.Errorf(
				"stored agent configs %s and %s collide after migration",
				existingID,
				config.id,
			)
		}
		seen[key] = config.id
	}
	return nil
}

func storedAgentConfigMigrationError(violations []string) error {
	const reportedViolationLimit = 20
	detail := strings.Join(violations[:min(len(violations), reportedViolationLimit)], ", ")
	if omitted := len(violations) - reportedViolationLimit; omitted > 0 {
		detail += fmt.Sprintf(", and %d more", omitted)
	}
	resource := "agent configs"
	if len(violations) == 1 {
		resource = "agent config"
	}
	return fmt.Errorf("%d %s must be migrated: %s", len(violations), resource, detail)
}

func migrateAgentConfigSource(
	format agentConfigNameMigrationSourceFormat,
	raw []byte,
) ([]byte, bool, error) {
	before, err := agentConfigNameMigrationSourceObject(format, raw)
	if err != nil {
		return nil, false, err
	}
	if err := validateAgentConfigNameMigrationSourceReferences(before); err != nil {
		return nil, false, err
	}
	legacyName, hasLegacyName := before["name"]
	if !hasLegacyName {
		return raw, false, nil
	}
	if _, ok := legacyName.(string); !ok {
		return nil, false, errors.New("top-level name must be a string")
	}
	delete(before, "name")

	var migrated []byte
	switch format {
	case agentConfigNameMigrationSourceFormatJSON:
		migrated, err = json.Marshal(before)
		if err != nil {
			return nil, false, fmt.Errorf("encode migrated agent config JSON: %w", err)
		}
	case agentConfigNameMigrationSourceFormatYAML:
		migrated, err = removeSimpleTopLevelYAMLName(raw)
		if err != nil {
			return nil, false, err
		}
	default:
		return nil, false, fmt.Errorf("unsupported agent config source format %q", format)
	}
	after, err := agentConfigNameMigrationSourceObject(format, migrated)
	if err != nil {
		return nil, false, fmt.Errorf("parse migrated source: %w", err)
	}
	if !reflect.DeepEqual(after, before) {
		return nil, false, errors.New("removing top-level name changed other source values")
	}
	return migrated, true, nil
}

func removeSimpleTopLevelYAMLName(raw []byte) ([]byte, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse agent config YAML: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("agent config YAML must be a top-level mapping")
	}
	root := document.Content[0]
	pairIndex := -1
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value != "name" {
			continue
		}
		if pairIndex >= 0 {
			return nil, errors.New("top-level name must not be repeated")
		}
		pairIndex = index
	}
	if pairIndex < 0 {
		return nil, errors.New("top-level name is missing")
	}
	key := root.Content[pairIndex]
	value := root.Content[pairIndex+1]
	if key.Kind != yaml.ScalarNode || key.Style != 0 || key.Column != 1 ||
		value.Kind != yaml.ScalarNode || value.Line != key.Line {
		return nil, errors.New("top-level name must be a standalone scalar line")
	}
	start, end, err := yamlLineRange(raw, key.Line)
	if err != nil {
		return nil, err
	}
	line := bytes.TrimSuffix(raw[start:end], []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	lineValue, err := decodeAgentConfigNameMigrationYAML(line)
	lineMapping, ok := lineValue.(map[string]any)
	if !bytes.HasPrefix(line, []byte("name:")) || err != nil || !ok || len(lineMapping) != 1 ||
		lineMapping["name"] != value.Value {
		return nil, errors.New("top-level name must be a standalone scalar line")
	}
	migrated := make([]byte, 0, len(raw)-(end-start))
	migrated = append(migrated, raw[:start]...)
	migrated = append(migrated, raw[end:]...)
	return migrated, nil
}

func yamlLineRange(raw []byte, line int) (int, int, error) {
	if line < 1 {
		return 0, 0, errors.New("top-level name has an invalid YAML line")
	}
	start := 0
	for current := 1; current < line; current++ {
		offset := bytes.IndexByte(raw[start:], '\n')
		if offset < 0 {
			return 0, 0, errors.New("top-level name YAML line is outside source")
		}
		start += offset + 1
	}
	end := len(raw)
	if offset := bytes.IndexByte(raw[start:], '\n'); offset >= 0 {
		end = start + offset + 1
	}
	return start, end, nil
}

func agentConfigNameMigrationSourceObject(
	format agentConfigNameMigrationSourceFormat,
	raw []byte,
) (map[string]any, error) {
	var value any
	var err error
	switch format {
	case agentConfigNameMigrationSourceFormatJSON:
		value, err = decodeAgentConfigNameMigrationJSON(raw)
	case agentConfigNameMigrationSourceFormatYAML:
		value, err = decodeAgentConfigNameMigrationYAML(raw)
	default:
		return nil, fmt.Errorf("unsupported agent config source format %q", format)
	}
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("agent config source must be an object")
	}
	return root, nil
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
	value, err := normalizeAgentConfigNameMigrationYAMLValue(value)
	if err != nil {
		return nil, fmt.Errorf("normalize agent config YAML: %w", err)
	}
	return value, nil
}

func normalizeAgentConfigNameMigrationYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeAgentConfigNameMigrationYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key %v is not a string", key)
			}
			normalized, err := normalizeAgentConfigNameMigrationYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[keyString] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeAgentConfigNameMigrationYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[index] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

func validateAgentConfigNameMigrationSourceReferences(root map[string]any) error {
	model, ok := root["model"].(map[string]any)
	if !ok {
		return errors.New("model must be an object")
	}
	for _, field := range []string{"provider_config", "name"} {
		value, ok := model[field].(string)
		if !ok {
			return fmt.Errorf("model.%s must be a string", field)
		}
		if err := validateAgentConfigNameMigrationResourceName("model."+field, value); err != nil {
			return err
		}
	}
	machineSources, exists := root["machine_sources"]
	if !exists || machineSources == nil {
		return nil
	}
	machines, ok := machineSources.([]any)
	if !ok {
		return errors.New("machine_sources must be an array")
	}
	for index, candidate := range machines {
		machine, ok := candidate.(map[string]any)
		if !ok {
			return fmt.Errorf("machine_sources[%d] must be an object", index)
		}
		for _, field := range []string{"machine_name", "machine_pool_name"} {
			candidate, exists := machine[field]
			if !exists {
				continue
			}
			value, ok := candidate.(string)
			if !ok {
				return fmt.Errorf("machine_sources[%d].%s must be a string", index, field)
			}
			if err := validateAgentConfigNameMigrationResourceName(
				fmt.Sprintf("machine_sources[%d].%s", index, field),
				value,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAgentConfigNameMigrationResourceName(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if !norm.NFC.IsNormalString(value) {
		return fmt.Errorf("%s must use Unicode NFC normalization", field)
	}
	if utf8.RuneCountInString(value) > agentConfigNameMigrationMaxCodePoints {
		return fmt.Errorf(
			"%s cannot exceed %d Unicode characters",
			field,
			agentConfigNameMigrationMaxCodePoints,
		)
	}
	if value != strings.Trim(value, " ") {
		return fmt.Errorf("%s must not start or end with whitespace", field)
	}
	for _, codepoint := range value {
		if agentConfigNameMigrationCodePointIsForbidden(codepoint) {
			return fmt.Errorf("%s contains an unsupported invisible, control, or format character", field)
		}
	}
	return nil
}

func agentConfigNameMigrationCodePointIsForbidden(codepoint rune) bool {
	return codepoint >= 0 && codepoint <= 31 ||
		codepoint >= 127 && codepoint <= 160 ||
		codepoint == 173 ||
		codepoint == 847 ||
		codepoint >= 1536 && codepoint <= 1541 ||
		codepoint == 1564 ||
		codepoint == 1757 ||
		codepoint == 1807 ||
		codepoint >= 2192 && codepoint <= 2193 ||
		codepoint == 2274 ||
		codepoint >= 4447 && codepoint <= 4448 ||
		codepoint == 5760 ||
		codepoint >= 6068 && codepoint <= 6069 ||
		codepoint >= 6155 && codepoint <= 6159 ||
		codepoint >= 8192 && codepoint <= 8207 ||
		codepoint >= 8232 && codepoint <= 8239 ||
		codepoint >= 8287 && codepoint <= 8303 ||
		codepoint == 10240 ||
		codepoint == 12288 ||
		codepoint == 12644 ||
		codepoint >= 65024 && codepoint <= 65039 ||
		codepoint == 65279 ||
		codepoint == 65440 ||
		codepoint >= 65520 && codepoint <= 65531 ||
		codepoint == 65533 ||
		codepoint == 69821 ||
		codepoint == 69837 ||
		codepoint >= 78896 && codepoint <= 78911 ||
		codepoint >= 113824 && codepoint <= 113827 ||
		codepoint >= 119155 && codepoint <= 119162 ||
		codepoint >= 917504 && codepoint <= 921599
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
