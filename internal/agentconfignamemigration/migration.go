package agentconfignamemigration

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
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"gopkg.in/yaml.v3"
)

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

func Up(ctx context.Context, tx *sql.Tx) error {
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
		agentconfig.SourceFormat(config.sourceFormat),
		[]byte(config.source),
	)
	if err != nil {
		return migratedAgentConfig{}, fmt.Errorf("source: %w", err)
	}
	if _, err := agentconfig.ParseSource(
		agentconfig.SourceFormat(config.sourceFormat),
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
	if _, err := agentconfig.RuntimeContractFromCompiled(
		json.RawMessage(migratedCompiled),
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
	format agentconfig.SourceFormat,
	raw []byte,
) ([]byte, bool, error) {
	switch format {
	case agentconfig.SourceFormatJSON:
		return migrateAgentConfigJSONSource(raw)
	case agentconfig.SourceFormatYAML:
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
		for _, machine := range machineSources.Content {
			if machine.Kind != yaml.MappingNode {
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
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
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
	repaired := strings.TrimSpace(value)
	runes := []rune(repaired)
	if len(runes) > resourcename.MaxCodePoints {
		repaired = string(runes[:resourcename.MaxCodePoints])
	}
	return strings.TrimSpace(repaired)
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
