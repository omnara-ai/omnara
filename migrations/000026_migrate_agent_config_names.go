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
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/pressly/goose/v3"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	AgentConfigNameMigrationFile            = "000026_migrate_agent_config_names.go"
	AgentConfigNameMigrationVersion         = 26
	agentConfigNameMigrationMaxCodePoints   = 64
	agentConfigNameMigrationCompilerVersion = ""
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
	compilerVersion         string
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
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id::text,
			project_id::text,
			source,
			source_format,
			source_hash,
			definition::text,
			compiled_definition::text,
			compiler_version,
			effective_definition_hash
		FROM agent_configs
		ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list stored agent configs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	configs := make([]migratedAgentConfig, 0)
	violations := make([]string, 0)
	for rows.Next() {
		var config storedAgentConfig
		if err := rows.Scan(
			&config.id,
			&config.projectID,
			&config.source,
			&config.sourceFormat,
			&config.sourceHash,
			&config.definition,
			&config.compiledDefinition,
			&config.compilerVersion,
			&config.effectiveDefinitionHash,
		); err != nil {
			return fmt.Errorf("scan stored agent config: %w", err)
		}
		migrated, err := migrateStoredAgentConfig(config)
		if err != nil {
			violations = append(violations, config.id+" ("+err.Error()+")")
			continue
		}
		configs = append(configs, migrated)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stored agent configs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close stored agent configs: %w", err)
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
		if _, err := tx.ExecContext(
			ctx,
			`ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`,
		); err != nil {
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
		if _, err := tx.ExecContext(
			ctx,
			`ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`,
		); err != nil {
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

func migrateStoredAgentConfig(config storedAgentConfig) (migratedAgentConfig, error) {
	migrated := migratedAgentConfig{storedAgentConfig: config}
	if config.sourceHash != hashBytes([]byte(config.source)) {
		return migratedAgentConfig{}, errors.New("source hash does not match source")
	}
	migratedSource, sourceChanged, err := migrateAgentConfigSource(
		agentConfigNameMigrationSourceFormat(config.sourceFormat),
		[]byte(config.source),
	)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("source: %w", err)
	}
	if err := validateAgentConfigNameMigrationSource(
		agentConfigNameMigrationSourceFormat(config.sourceFormat),
		migratedSource,
	); err != nil {
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
	definitionCanonical, err := canonicalJSON(migratedDefinition)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("definition: %w", err)
	}
	compiledCanonical, err := canonicalJSON(migratedCompiled)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}
	definitionHash := config.effectiveDefinitionHash
	if compiledChanged {
		definitionHash = hashBytes(compiledCanonical)
	}
	if err := validateAgentConfigNameMigrationCompiledDefinition(
		migratedCompiled,
		config.compilerVersion,
		definitionHash,
	); err != nil {
		return migratedAgentConfig{}, fmt.Errorf("compiled definition: %w", err)
	}

	migrated.source = string(migratedSource)
	if sourceChanged {
		migrated.sourceHash = hashBytes(migratedSource)
	}
	migrated.definition = definitionCanonical
	migrated.compiledDefinition = compiledCanonical
	migrated.effectiveDefinitionHash = definitionHash
	migrated.changed = sourceChanged || definitionChanged || compiledChanged
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
	switch format {
	case agentConfigNameMigrationSourceFormatJSON:
		return migrateAgentConfigJSONSource(raw)
	case agentConfigNameMigrationSourceFormatYAML:
		return migrateAgentConfigYAMLSource(raw)
	default:
		return nil, false, fmt.Errorf("unsupported agent config source format %q", format)
	}
}

func migrateAgentConfigJSONSource(raw []byte) ([]byte, bool, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false, fmt.Errorf("parse agent config JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, false, errors.New("parse agent config JSON: trailing value")
		}
		return nil, false, fmt.Errorf("parse agent config JSON: %w", err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return raw, false, nil
	}
	changed := removeLegacyNameAndRepairReferences(root)
	if !changed {
		return raw, false, nil
	}
	migrated, err := json.Marshal(root)
	if err != nil {
		return nil, false, fmt.Errorf("encode migrated agent config JSON: %w", err)
	}
	return migrated, true, nil
}

func removeLegacyNameAndRepairReferences(root map[string]any) bool {
	changed := false
	if _, ok := root["name"]; ok {
		delete(root, "name")
		changed = true
	}
	if model, ok := root["model"].(map[string]any); ok {
		changed = repairJSONResourceReference(model, "provider_config") || changed
		changed = repairJSONResourceReference(model, "name") || changed
	}
	if machineSources, ok := root["machine_sources"].([]any); ok {
		for _, value := range machineSources {
			machine, ok := value.(map[string]any)
			if !ok {
				continue
			}
			changed = repairJSONResourceReference(machine, "machine_name") || changed
			changed = repairJSONResourceReference(machine, "machine_pool_name") || changed
		}
	}
	return changed
}

func repairJSONResourceReference(object map[string]any, key string) bool {
	value, ok := object[key].(string)
	if !ok {
		return false
	}
	repaired := repairResourceReference(value)
	if repaired == value {
		return false
	}
	object[key] = repaired
	return true
}

func migrateAgentConfigYAMLSource(raw []byte) ([]byte, bool, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, false, fmt.Errorf("parse agent config YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, false, errors.New("parse agent config YAML: trailing document")
		}
		return nil, false, fmt.Errorf("parse agent config YAML: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return raw, false, nil
	}
	root := document.Content[0]
	nameStart, nameEnd, canRemoveNameInPlace := yamlSingleLineMappingPairRange(raw, root, "name")
	nameChanged := removeYAMLMappingKey(root, "name")
	referencesChanged := false
	if model := yamlMappingValue(root, "model"); model != nil && model.Kind == yaml.MappingNode {
		referencesChanged = repairYAMLResourceReference(model, "provider_config") || referencesChanged
		referencesChanged = repairYAMLResourceReference(model, "name") || referencesChanged
	}
	machineSources := yamlMappingValue(root, "machine_sources")
	if machineSources != nil && machineSources.Kind == yaml.SequenceNode {
		for _, candidate := range machineSources.Content {
			machine := dereferenceAgentConfigNameMigrationYAMLNode(candidate)
			if machine == nil || machine.Kind != yaml.MappingNode {
				continue
			}
			referencesChanged = repairYAMLResourceReference(machine, "machine_name") || referencesChanged
			referencesChanged = repairYAMLResourceReference(machine, "machine_pool_name") || referencesChanged
		}
	}
	if !nameChanged && !referencesChanged {
		return raw, false, nil
	}
	if nameChanged && !referencesChanged && canRemoveNameInPlace {
		migrated := make([]byte, 0, len(raw)-(nameEnd-nameStart))
		migrated = append(migrated, raw[:nameStart]...)
		migrated = append(migrated, raw[nameEnd:]...)
		return migrated, true, nil
	}
	var migrated bytes.Buffer
	encoder := yaml.NewEncoder(&migrated)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, false, fmt.Errorf("encode migrated agent config YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, false, fmt.Errorf("finish migrated agent config YAML: %w", err)
	}
	return migrated.Bytes(), true, nil
}

func yamlSingleLineMappingPairRange(
	raw []byte,
	mapping *yaml.Node,
	key string,
) (int, int, bool) {
	if mapping.Style&yaml.FlowStyle != 0 {
		return 0, 0, false
	}
	pairIndex := -1
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		if pairIndex >= 0 {
			return 0, 0, false
		}
		pairIndex = index
	}
	if pairIndex < 0 {
		return 0, 0, false
	}
	keyNode := mapping.Content[pairIndex]
	valueNode := mapping.Content[pairIndex+1]
	if keyNode.Line < 1 || keyNode.Column != 1 || valueNode.Kind != yaml.ScalarNode ||
		valueNode.Line != keyNode.Line || valueNode.Style != 0 {
		return 0, 0, false
	}
	lineOffsets := []int{0}
	for index, value := range raw {
		if value == '\n' {
			lineOffsets = append(lineOffsets, index+1)
		}
	}
	if keyNode.Line > len(lineOffsets) {
		return 0, 0, false
	}
	start := lineOffsets[keyNode.Line-1]
	end := len(raw)
	if keyNode.Line < len(lineOffsets) {
		end = lineOffsets[keyNode.Line]
	}
	scanEnd := len(raw)
	if pairIndex+2 < len(mapping.Content) {
		nextKey := mapping.Content[pairIndex+2]
		if nextKey.Line <= keyNode.Line || nextKey.Line > len(lineOffsets) {
			return 0, 0, false
		}
		scanEnd = lineOffsets[nextKey.Line-1]
	}
	for _, line := range bytes.Split(raw[end:scanEnd], []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] != '#' {
			return 0, 0, false
		}
	}
	return start, end, true
}

func removeYAMLMappingKey(mapping *yaml.Node, key string) bool {
	changed := false
	content := mapping.Content[:0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			changed = true
			continue
		}
		content = append(content, mapping.Content[index], mapping.Content[index+1])
	}
	mapping.Content = content
	return changed
}

func yamlMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	return yamlMappingValueSeen(mapping, key, make(map[*yaml.Node]bool))
}

func yamlMappingValueSeen(mapping *yaml.Node, key string, seen map[*yaml.Node]bool) *yaml.Node {
	mapping = dereferenceAgentConfigNameMigrationYAMLNode(mapping)
	if mapping == nil || mapping.Kind != yaml.MappingNode || seen[mapping] {
		return nil
	}
	seen[mapping] = true
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key && key != "<<" {
			return dereferenceAgentConfigNameMigrationYAMLNode(mapping.Content[index+1])
		}
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != "<<" {
			continue
		}
		merge := dereferenceAgentConfigNameMigrationYAMLNode(mapping.Content[index+1])
		if merge == nil {
			continue
		}
		if merge.Kind == yaml.SequenceNode {
			for _, candidate := range merge.Content {
				if value := yamlMappingValueSeen(candidate, key, seen); value != nil {
					return value
				}
			}
			continue
		}
		if value := yamlMappingValueSeen(merge, key, seen); value != nil {
			return value
		}
	}
	return nil
}

func dereferenceAgentConfigNameMigrationYAMLNode(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func repairYAMLResourceReference(mapping *yaml.Node, key string) bool {
	value := yamlMappingValue(mapping, key)
	if value == nil || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return false
	}
	repaired := repairResourceReference(value.Value)
	if repaired == value.Value {
		return false
	}
	value.Value = repaired
	return true
}

func repairResourceReference(value string) string {
	repaired := strings.Trim(value, agentConfigNameMigrationWhitespace)
	repaired = norm.NFC.String(repaired)
	runes := []rune(repaired)
	if len(runes) > agentConfigNameMigrationMaxCodePoints {
		repaired = string(runes[:agentConfigNameMigrationMaxCodePoints])
	}
	return strings.Trim(repaired, agentConfigNameMigrationWhitespace)
}

const agentConfigNameMigrationWhitespace = "\u0009\u000a\u000b\u000c\u000d\u0020\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

type agentConfigNameMigrationSourceReferences struct {
	Model struct {
		ProviderConfig string `json:"provider_config"`
		Name           string `json:"name"`
	} `json:"model"`
	MachineSources []struct {
		MachineName     string `json:"machine_name"`
		MachinePoolName string `json:"machine_pool_name"`
	} `json:"machine_sources"`
}

func validateAgentConfigNameMigrationSource(format agentConfigNameMigrationSourceFormat, raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("agent config source is required")
	}
	jsonSource, err := agentConfigNameMigrationSourceJSON(format, raw)
	if err != nil {
		return err
	}
	schema, err := compiledAgentConfigNameMigrationSourceSchema()
	if err != nil {
		return err
	}
	result := schema.Validate(jsonSource)
	if !result.IsValid() {
		return fmt.Errorf(
			"agent config source does not match frozen migration JSON schema: %s",
			agentConfigNameMigrationValidationErrors(result),
		)
	}
	var references agentConfigNameMigrationSourceReferences
	if err := json.Unmarshal(jsonSource, &references); err != nil {
		return fmt.Errorf("decode agent config source references: %w", err)
	}
	if err := validateAgentConfigNameMigrationResourceName(
		"model.provider_config",
		references.Model.ProviderConfig,
	); err != nil {
		return err
	}
	if err := validateAgentConfigNameMigrationResourceName("model.name", references.Model.Name); err != nil {
		return err
	}
	for index, machine := range references.MachineSources {
		if machine.MachineName != "" {
			if err := validateAgentConfigNameMigrationResourceName(
				fmt.Sprintf("machine_sources[%d].machine_name", index),
				machine.MachineName,
			); err != nil {
				return err
			}
		}
		if machine.MachinePoolName != "" {
			if err := validateAgentConfigNameMigrationResourceName(
				fmt.Sprintf("machine_sources[%d].machine_pool_name", index),
				machine.MachinePoolName,
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
	if len(value) > 4*agentConfigNameMigrationMaxCodePoints {
		return fmt.Errorf(
			"%s cannot exceed %d UTF-8 bytes",
			field,
			4*agentConfigNameMigrationMaxCodePoints,
		)
	}
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	if first == ' ' || last == ' ' {
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

func agentConfigNameMigrationSourceJSON(
	format agentConfigNameMigrationSourceFormat,
	raw []byte,
) ([]byte, error) {
	switch format {
	case agentConfigNameMigrationSourceFormatJSON:
		return raw, nil
	case agentConfigNameMigrationSourceFormatYAML:
		var value any
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
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
		normalized, err := normalizeAgentConfigNameMigrationYAMLValue(value)
		if err != nil {
			return nil, fmt.Errorf("normalize agent config YAML: %w", err)
		}
		jsonSource, err := json.Marshal(normalized)
		if err != nil {
			return nil, fmt.Errorf("marshal agent config YAML as JSON: %w", err)
		}
		return jsonSource, nil
	default:
		return nil, fmt.Errorf("unsupported agent config source format %q", format)
	}
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

var compiledAgentConfigNameMigrationSourceSchema = sync.OnceValues(func() (*kjsonschema.Schema, error) {
	schemaJSON, err := json.Marshal(agentConfigNameMigrationSourceSchema())
	if err != nil {
		return nil, fmt.Errorf("marshal frozen agent config migration JSON schema: %w", err)
	}
	compiled, err := kjsonschema.NewCompiler().Compile(schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("compile frozen agent config migration JSON schema: %w", err)
	}
	return compiled, nil
})

func agentConfigNameMigrationSourceSchema() *kjsonschema.Schema {
	schema := kjsonschema.Object(
		kjsonschema.Prop("version", kjsonschema.Enum("v1")),
		kjsonschema.Prop("instruction", kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))),
		kjsonschema.Prop("model", kjsonschema.Ref("#/$defs/AgentConfigModelSource")),
		kjsonschema.Prop("machine_sources", kjsonschema.AnyOf(
			kjsonschema.Array(kjsonschema.Items(kjsonschema.Ref("#/$defs/AgentConfigMachineSource"))),
			kjsonschema.Null(),
		)),
		kjsonschema.Prop("tools", kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.AllOf(
				kjsonschema.String(kjsonschema.Pattern(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)),
				kjsonschema.Not(kjsonschema.String(kjsonschema.Pattern(`^mcp__`))),
			)),
			kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigToolSource")),
		)),
		kjsonschema.Prop("mcp", kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(`^[a-zA-Z][a-zA-Z0-9-]{0,31}$`))),
			kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigMCPSource")),
		)),
		kjsonschema.Prop("skills", kjsonschema.AnyOf(
			kjsonschema.Array(
				kjsonschema.Items(kjsonschema.String(kjsonschema.Pattern(`^skl_[a-z2-7]{26}$`))),
				kjsonschema.UniqueItems(true),
			),
			kjsonschema.Null(),
		)),
		kjsonschema.Required("instruction", "model"),
		kjsonschema.AdditionalProps(false),
		kjsonschema.Defs(map[string]*kjsonschema.Schema{
			"AgentConfigModelSource": kjsonschema.Object(
				kjsonschema.Prop("provider_config", agentConfigNameMigrationResourceNameSchema()),
				kjsonschema.Prop("name", agentConfigNameMigrationResourceNameSchema()),
				kjsonschema.Prop("context_window_tokens", kjsonschema.Integer(kjsonschema.Min(1))),
				kjsonschema.Prop("default_max_output_tokens", kjsonschema.Integer(kjsonschema.Min(1))),
				kjsonschema.Prop("cache_retention", kjsonschema.Enum("none", "short", "long")),
				kjsonschema.Prop("reasoning", kjsonschema.Object(
					kjsonschema.Prop("effort", kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))),
					kjsonschema.Required("effort"),
					kjsonschema.AdditionalProps(false),
				)),
				kjsonschema.Required("provider_config", "name"),
				kjsonschema.AdditionalProps(false),
			),
			"AgentConfigMachineSource": kjsonschema.Object(
				kjsonschema.Prop("machine_name", agentConfigNameMigrationResourceNameSchema()),
				kjsonschema.Prop("machine_pool_name", agentConfigNameMigrationResourceNameSchema()),
				kjsonschema.Prop("max_machines", kjsonschema.Integer(kjsonschema.Min(0))),
				kjsonschema.Prop("initial_num_machines", kjsonschema.Integer(kjsonschema.Min(0))),
				kjsonschema.Prop("delete_after_idle_minutes", kjsonschema.AnyOf(
					kjsonschema.Const(0),
					kjsonschema.Integer(kjsonschema.Min(5), kjsonschema.Max(float64(math.MaxInt32))),
				)),
				kjsonschema.Prop("cwd", kjsonschema.String()),
				kjsonschema.Prop("machine_cpu", kjsonschema.Integer(
					kjsonschema.Min(1),
					kjsonschema.Max(float64(math.MaxInt32)),
				)),
				kjsonschema.Prop("machine_memory_mb", kjsonschema.Integer(
					kjsonschema.Min(1),
					kjsonschema.Max(float64(math.MaxInt32)),
				)),
				kjsonschema.Prop("env_overlay", kjsonschema.AnyOf(
					kjsonschema.Object(
						kjsonschema.PropertyNames(agentConfigNameMigrationEnvNameSchema()),
						kjsonschema.AdditionalPropsSchema(kjsonschema.AnyOf(kjsonschema.String(), kjsonschema.Null())),
					),
					kjsonschema.Null(),
				)),
				kjsonschema.Prop("secret_env_overlay", kjsonschema.AnyOf(
					kjsonschema.Object(
						kjsonschema.PropertyNames(agentConfigNameMigrationEnvNameSchema()),
						kjsonschema.AdditionalPropsSchema(kjsonschema.AnyOf(
							kjsonschema.String(kjsonschema.Pattern(`^sec_[a-z2-7]{26}$`)),
							kjsonschema.Null(),
						)),
					),
					kjsonschema.Null(),
				)),
				kjsonschema.Prop("machine_provider_options_overlay", kjsonschema.AnyOf(
					kjsonschema.Object(),
					kjsonschema.Null(),
				)),
				kjsonschema.Prop("description", kjsonschema.String()),
				kjsonschema.AdditionalProps(false),
				kjsonschema.Keyword(func(schema *kjsonschema.Schema) {
					schema.OneOf = []*kjsonschema.Schema{
						kjsonschema.Object(kjsonschema.Required("machine_name")),
						kjsonschema.Object(kjsonschema.Required("machine_pool_name")),
					}
				}),
			),
			"AgentConfigToolSource": agentConfigNameMigrationToolSourceSchema(),
			"AgentToolInputSchema": kjsonschema.Object(
				kjsonschema.Prop("type", kjsonschema.Const("object")),
				kjsonschema.Prop("properties", kjsonschema.Object(
					kjsonschema.AdditionalPropsSchema(kjsonschema.Object()),
				)),
				kjsonschema.Prop("required", kjsonschema.Array(
					kjsonschema.Items(kjsonschema.String(kjsonschema.MinLength(1))),
				)),
				kjsonschema.Required("type"),
			),
			"AgentConfigMCPSource": kjsonschema.Object(
				kjsonschema.Prop("url", kjsonschema.String(kjsonschema.MinLength(1))),
				kjsonschema.Prop("auth", kjsonschema.Ref("#/$defs/AgentConfigMCPAuthSource")),
				kjsonschema.Prop("default_enabled", kjsonschema.AnyOf(kjsonschema.Boolean(), kjsonschema.Null())),
				kjsonschema.Prop("permission", kjsonschema.Ref("#/$defs/ToolPermissionSelection")),
				kjsonschema.Prop("tools", kjsonschema.Object(
					kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`))),
					kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigMCPToolSource")),
				)),
				kjsonschema.Required("url"),
				kjsonschema.AdditionalProps(false),
			),
			"AgentConfigMCPAuthSource": kjsonschema.Object(
				kjsonschema.Prop("type", kjsonschema.Enum("bearer", "oauth", "sigv4")),
				kjsonschema.Prop("secret_id", kjsonschema.String(
					kjsonschema.MinLength(1),
					kjsonschema.Pattern(`^sec_[a-z2-7]{26}$`),
				)),
				kjsonschema.Prop("service", kjsonschema.String(kjsonschema.MinLength(1))),
				kjsonschema.Prop("region", kjsonschema.String(kjsonschema.MinLength(1))),
				kjsonschema.Required("type", "secret_id"),
				kjsonschema.AdditionalProps(false),
			),
			"AgentConfigMCPToolSource": kjsonschema.Object(
				kjsonschema.Prop("enabled", kjsonschema.AnyOf(kjsonschema.Boolean(), kjsonschema.Null())),
				kjsonschema.Prop("permission", kjsonschema.Ref("#/$defs/ToolPermissionSelection")),
				kjsonschema.AdditionalProps(false),
			),
			"ToolPermissionSelection": kjsonschema.Object(
				kjsonschema.Prop("mode", kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))),
				kjsonschema.Prop("parameters", kjsonschema.Object()),
				kjsonschema.Required("mode"),
				kjsonschema.AdditionalProps(false),
			),
		}),
	)
	return schema
}

func agentConfigNameMigrationToolSourceSchema() *kjsonschema.Schema {
	schema := kjsonschema.Object(
		kjsonschema.Prop("type", kjsonschema.Enum("built_in", "custom")),
		kjsonschema.Prop("enabled", kjsonschema.AnyOf(kjsonschema.Boolean(), kjsonschema.Null())),
		kjsonschema.Prop("permission", kjsonschema.Ref("#/$defs/ToolPermissionSelection")),
		kjsonschema.Prop("description", kjsonschema.String(kjsonschema.MinLength(1))),
		kjsonschema.Prop("input_schema", kjsonschema.Ref("#/$defs/AgentToolInputSchema")),
		kjsonschema.AdditionalProps(false),
	)
	schema.If = kjsonschema.Object(
		kjsonschema.Prop("type", kjsonschema.Const("custom")),
		kjsonschema.Required("type"),
	)
	schema.Then = &kjsonschema.Schema{Required: []string{"description", "input_schema"}}
	schema.Else = kjsonschema.Not(kjsonschema.AnyOf(
		kjsonschema.Object(kjsonschema.Required("description")),
		kjsonschema.Object(kjsonschema.Required("input_schema")),
	))
	return schema
}

func agentConfigNameMigrationResourceNameSchema() *kjsonschema.Schema {
	return kjsonschema.String(
		kjsonschema.MinLength(1),
		kjsonschema.MaxLength(agentConfigNameMigrationMaxCodePoints),
		kjsonschema.Pattern(`^\S(?:.*\S)?$`),
	)
}

func agentConfigNameMigrationEnvNameSchema() *kjsonschema.Schema {
	return kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern("^[^=\x00]+$"))
}

func agentConfigNameMigrationValidationErrors(result *kjsonschema.EvaluationResult) string {
	detailed := result.DetailedErrors()
	messages := make([]string, 0, len(detailed))
	for location, message := range detailed {
		if location == "" {
			location = "/"
		}
		messages = append(messages, location+": "+message)
	}
	if len(messages) == 0 {
		return "validation failed"
	}
	sort.Strings(messages)
	return strings.Join(messages, "; ")
}

type agentConfigNameMigrationPermission struct {
	Mode       string          `json:"mode"`
	Parameters json.RawMessage `json:"parameters"`
}

type agentConfigNameMigrationCompiled struct {
	Version        string                                          `json:"version,omitempty"`
	Instruction    string                                          `json:"instruction"`
	Model          agentConfigNameMigrationCompiledModel           `json:"model,omitempty"`
	MachineSources []agentConfigNameMigrationCompiledMachineSource `json:"machine_sources,omitempty"`
	Tools          map[string]agentConfigNameMigrationCompiledTool `json:"tools,omitempty"`
	MCP            map[string]agentConfigNameMigrationCompiledMCP  `json:"mcp,omitempty"`
	Skills         []agentConfigNameMigrationCompiledSkill         `json:"skills,omitempty"`
}

type agentConfigNameMigrationCompiledModel struct {
	ConfiguredModelID      string                                          `json:"configured_model_id,omitempty"`
	ContextWindowTokens    *int                                            `json:"context_window_tokens,omitempty"`
	DefaultMaxOutputTokens *int                                            `json:"default_max_output_tokens,omitempty"`
	CacheRetention         string                                          `json:"cache_retention,omitempty"`
	Reasoning              *agentConfigNameMigrationCompiledModelReasoning `json:"reasoning,omitempty"`
}

type agentConfigNameMigrationCompiledModelReasoning struct {
	Effort string `json:"effort"`
}

type agentConfigNameMigrationCompiledMachineSource struct {
	MachineID                     string                     `json:"machine_id,omitempty"`
	MachinePoolID                 string                     `json:"machine_pool_id,omitempty"`
	MaxMachines                   int                        `json:"max_machines,omitempty"`
	InitialNumMachines            int                        `json:"initial_num_machines,omitempty"`
	DeleteAfterIdleMinutes        *int                       `json:"delete_after_idle_minutes,omitempty"`
	Cwd                           string                     `json:"cwd,omitempty"`
	MachineCPU                    *int                       `json:"machine_cpu,omitempty"`
	MachineMemoryMB               *int                       `json:"machine_memory_mb,omitempty"`
	EnvOverlay                    map[string]*string         `json:"env_overlay,omitempty"`
	SecretEnvOverlay              map[string]*string         `json:"secret_env_overlay,omitempty"`
	MachineProviderOptionsOverlay map[string]json.RawMessage `json:"machine_provider_options_overlay,omitempty"`
	Description                   string                     `json:"description,omitempty"`
}

type agentConfigNameMigrationCompiledTool struct {
	Enabled     bool                               `json:"enabled"`
	Type        string                             `json:"type,omitempty"`
	Permission  agentConfigNameMigrationPermission `json:"permission"`
	Description string                             `json:"description,omitempty"`
	InputSchema json.RawMessage                    `json:"input_schema,omitempty"`
}

type agentConfigNameMigrationCompiledMCP struct {
	URL            string                                             `json:"url"`
	Auth           *agentConfigNameMigrationCompiledMCPAuth           `json:"auth,omitempty"`
	DefaultEnabled bool                                               `json:"default_enabled"`
	Permission     agentConfigNameMigrationPermission                 `json:"permission"`
	Tools          map[string]agentConfigNameMigrationCompiledMCPTool `json:"tools,omitempty"`
}

type agentConfigNameMigrationCompiledMCPAuth struct {
	Type     string `json:"type"`
	SecretID string `json:"secret_id"`
	Service  string `json:"service,omitempty"`
	Region   string `json:"region,omitempty"`
}

type agentConfigNameMigrationCompiledMCPTool struct {
	Enabled    *bool                               `json:"enabled,omitempty"`
	Permission *agentConfigNameMigrationPermission `json:"permission,omitempty"`
}

type agentConfigNameMigrationCompiledSkill struct {
	PublicID string `json:"public_id"`
}

var agentConfigNameMigrationBuiltInTools = map[string]struct{}{
	"run_command":              {},
	"write_process":            {},
	"read_process":             {},
	"stop_process":             {},
	"list_processes":           {},
	"create_machine":           {},
	"delete_machine":           {},
	"list_machines":            {},
	"inspect_machine":          {},
	"ask_question":             {},
	"send_integration_message": {},
	"set_integration_target":   {},
	"web_search":               {},
	"web_fetch":                {},
	"skill":                    {},
}

func validateAgentConfigNameMigrationCompiledDefinition(
	raw []byte,
	compilerVersion string,
	definitionHash string,
) error {
	if len(raw) == 0 {
		return errors.New("agent config compiled definition is required")
	}
	if compilerVersion != agentConfigNameMigrationCompilerVersion {
		return fmt.Errorf("agent config compiler contract %q is not supported", compilerVersion)
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return fmt.Errorf("parse compiled agent config: %w", err)
	}
	if got := hashBytes(canonical); got != definitionHash {
		return fmt.Errorf(
			"agent config definition hash mismatch: got %s want %s",
			got,
			definitionHash,
		)
	}
	var compiled agentConfigNameMigrationCompiled
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&compiled); err != nil {
		return fmt.Errorf("parse compiled agent config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("parse compiled agent config: trailing JSON value")
		}
		return fmt.Errorf("parse compiled agent config: %w", err)
	}
	for name, tool := range compiled.Tools {
		if err := validateAgentConfigNameMigrationCompiledTool(name, tool); err != nil {
			return err
		}
	}
	for serverKey, server := range compiled.MCP {
		if err := validateAgentConfigNameMigrationPermission(server.Permission, false); err != nil {
			return fmt.Errorf("compiled mcp server %q permission: %w", serverKey, err)
		}
		for remoteName, tool := range server.Tools {
			if tool.Permission == nil {
				continue
			}
			if err := validateAgentConfigNameMigrationPermission(*tool.Permission, false); err != nil {
				return fmt.Errorf(
					"compiled mcp server %q tool %q permission: %w",
					serverKey,
					remoteName,
					err,
				)
			}
		}
	}
	return nil
}

func validateAgentConfigNameMigrationCompiledTool(
	name string,
	tool agentConfigNameMigrationCompiledTool,
) error {
	_, builtIn := agentConfigNameMigrationBuiltInTools[name]
	if tool.Type == "custom" {
		if strings.HasPrefix(name, "mcp__") {
			return fmt.Errorf("compiled custom tool %q uses the reserved MCP tool namespace", name)
		}
		if builtIn {
			return fmt.Errorf("compiled custom tool %q collides with a built-in tool", name)
		}
		if err := validateAgentConfigNameMigrationPermission(tool.Permission, false); err != nil {
			return fmt.Errorf("compiled custom tool %q permission: %w", name, err)
		}
		return nil
	}
	if !builtIn {
		return fmt.Errorf("compiled tool %q is not registered", name)
	}
	alwaysAllowOnly := name == "send_integration_message"
	if err := validateAgentConfigNameMigrationPermission(tool.Permission, alwaysAllowOnly); err != nil {
		return fmt.Errorf("compiled built-in tool %q permission: %w", name, err)
	}
	return nil
}

func validateAgentConfigNameMigrationPermission(
	permission agentConfigNameMigrationPermission,
	alwaysAllowOnly bool,
) error {
	mode := strings.TrimSpace(permission.Mode)
	if mode == "" {
		return errors.New("permission mode is required")
	}
	if alwaysAllowOnly {
		if mode != "always_allow" {
			return fmt.Errorf("unsupported permission mode %q", mode)
		}
	} else {
		switch mode {
		case "always_allow", "always_ask", "always_deny":
		default:
			return fmt.Errorf("unsupported permission mode %q", mode)
		}
	}
	parameters := permission.Parameters
	if len(bytes.TrimSpace(parameters)) == 0 {
		parameters = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("must be an object")
		}
		return fmt.Errorf("permission mode %q parameters: %w", mode, err)
	}
	if len(object) != 0 {
		return fmt.Errorf("permission mode %q parameters must not define properties", mode)
	}
	return nil
}

func removeTopLevelJSONName(raw []byte) ([]byte, bool, error) {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&object); err != nil {
		return nil, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, false, errors.New("trailing JSON value")
		}
		return nil, false, err
	}
	if _, ok := object["name"]; !ok {
		return raw, false, nil
	}
	delete(object, "name")
	migrated, err := json.Marshal(object)
	if err != nil {
		return nil, false, err
	}
	return migrated, true, nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
