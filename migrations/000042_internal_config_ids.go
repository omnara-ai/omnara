package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

func newInternalConfigIDsMigration() *goose.Migration {
	return goose.NewGoMigration(42, &goose.GoFunc{RunTx: upInternalConfigIDs}, nil)
}

func upInternalConfigIDs(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs, machine_pools, project_machine_pool_grants,
machines, agent_machine_bindings, processes IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE model_provider_configs, configured_models,
agent_profiles, agent_profile_versions IN SHARE MODE`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, org_id, project_id, configured_model_id, compiled_definition,
effective_definition_hash, compiler_version, source, source_format, source_hash FROM agent_configs ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type update struct {
		id, orgID, projectID, modelID    uuid.UUID
		raw                              []byte
		hash                             string
		source, sourceFormat, sourceHash *string
	}
	var updates []update
	for rows.Next() {
		var config update
		var version string
		if err := rows.Scan(&config.id, &config.orgID, &config.projectID, &config.modelID, &config.raw,
			&config.hash, &version, &config.source, &config.sourceFormat, &config.sourceHash); err != nil {
			return err
		}
		if version == "1" {
			continue
		}
		if version != "" {
			return fmt.Errorf("config %s has unsupported compiler version %q", config.id, version)
		}
		updates = append(updates, config)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for index := range updates {
		config := &updates[index]
		previousHash, err := explicitDefaultToolsConfigHash(config.raw)
		if err != nil {
			return err
		}
		if previousHash != config.hash {
			return fmt.Errorf("config %s compiled hash does not match content", config.id)
		}
		defaults := make(map[string]map[string]string)
		converted, err := internalCompiledIDs(config.raw, config.modelID, func(
			key string, profileID uuid.UUID, provider, name string,
		) (uuid.UUID, error) {
			if provider == "" || name == "" {
				baseModelID := config.modelID
				if profileID != uuid.Nil {
					err := tx.QueryRowContext(ctx, `SELECT config.configured_model_id FROM agent_profiles profile
JOIN agent_profile_versions version ON version.profile_id = profile.id AND version.project_id = profile.project_id
 AND version.id = profile.current_version_id
JOIN agent_configs config ON config.id = version.agent_config_id AND config.project_id = profile.project_id
WHERE profile.id = $1 AND profile.project_id = $2 AND profile.deleted_at IS NULL
 AND version.deleted_at IS NULL`, profileID, config.projectID).Scan(&baseModelID)
					if err != nil {
						return uuid.Nil, fmt.Errorf("load subagent profile model: %w", err)
					}
				}
				var baseProvider, baseName string
				err := tx.QueryRowContext(ctx, `SELECT provider.name, model.name FROM configured_models model
JOIN model_provider_configs provider ON provider.id = model.model_provider_config_id AND provider.org_id = model.org_id
WHERE model.id = $1 AND model.org_id = $2 AND model.deleted_at IS NULL AND provider.deleted_at IS NULL`,
					baseModelID, config.orgID).Scan(&baseProvider, &baseName)
				if err != nil {
					return uuid.Nil, fmt.Errorf("load inherited subagent model: %w", err)
				}
				defaults[key] = make(map[string]string)
				if provider == "" {
					provider = baseProvider
					defaults[key]["provider_config"] = provider
				}
				if name == "" {
					name = baseName
					defaults[key]["name"] = name
				}
			}
			var id uuid.UUID
			err := tx.QueryRowContext(ctx, `SELECT model.id FROM configured_models model
JOIN model_provider_configs provider ON provider.id = model.model_provider_config_id AND provider.org_id = model.org_id
WHERE model.org_id = $1 AND provider.name = $2 AND model.name = $3
AND provider.deleted_at IS NULL AND model.deleted_at IS NULL`, config.orgID, provider, name).Scan(&id)
			return id, err
		})
		if err != nil {
			return fmt.Errorf("config %s: %w", config.id, err)
		}
		if config.source != nil && len(defaults) > 0 {
			if config.sourceHash == nil || config.sourceFormat == nil ||
				hashBytes([]byte(*config.source)) != *config.sourceHash {
				return fmt.Errorf("config %s source hash does not match content", config.id)
			}
			source, err := addSubagentModelSourceDefaults([]byte(*config.source), *config.sourceFormat, defaults)
			if err != nil {
				return fmt.Errorf("config %s source: %w", config.id, err)
			}
			config.source, config.sourceHash = new(string(source)), new(hashBytes(source))
		}
		updatedHash, err := explicitDefaultToolsConfigHash(converted)
		if err != nil {
			return err
		}
		config.raw, config.hash = converted, updatedHash
	}
	if len(updates) > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
			return err
		}
		for _, config := range updates {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET compiled_definition = $2::jsonb,
effective_definition_hash = $3, compiler_version = '1', source = $4, source_hash = $5 WHERE id = $1`,
				config.id, config.raw, config.hash, config.source, config.sourceHash); err != nil {
				return fmt.Errorf("update config %s: %w", config.id, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`); err != nil {
			return err
		}
	}
	for _, target := range [][2]string{
		{"machine_pools", "default_machine_secret_env"},
		{"project_machine_pool_grants", "default_machine_secret_env_overlay"},
		{"machines", "secret_env"},
		{"agent_machine_bindings", "secret_env_overlay"},
		{"processes", "secret_env"},
	} {
		if err := migrateSecretMapColumn(ctx, tx, target[0], target[1]); err != nil {
			return err
		}
	}
	return nil
}

func migrateSecretMapColumn(ctx context.Context, tx *sql.Tx, table, column string) error {
	query := fmt.Sprintf("SELECT id, %s FROM %s WHERE %s <> '{}'::jsonb ORDER BY id", column, table, column)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type update struct {
		id  uuid.UUID
		raw []byte
	}
	var updates []update
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		var fields map[string]*string
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		changed := false
		for key, value := range fields {
			if value == nil {
				continue
			}
			if existing, err := uuid.Parse(*value); err == nil && existing != uuid.Nil {
				continue
			}
			decoded, err := publicid.Decode(publicid.KindSecret, *value)
			if err != nil {
				return fmt.Errorf("%s %s %s.%s: %w", table, id, column, key, err)
			}
			text := decoded.String()
			fields[key] = &text
			changed = true
		}
		if changed {
			converted, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			updates = append(updates, update{id: id, raw: converted})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range updates {
		query := fmt.Sprintf("UPDATE %s SET %s = $2::jsonb WHERE id = $1", table, column)
		if _, err := tx.ExecContext(ctx, query, value.id, value.raw); err != nil {
			return err
		}
	}
	return nil
}

func internalCompiledIDs(
	raw []byte,
	modelID uuid.UUID,
	resolveModel func(key string, profileID uuid.UUID, provider, name string) (uuid.UUID, error),
) ([]byte, error) {
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	model, ok := root["model"].(map[string]any)
	if !ok || model["configured_model_id"] != modelID.String() || modelID == uuid.Nil {
		return nil, fmt.Errorf("compiled model does not match configured model")
	}
	if err := internalIDObjects(root, "machine_sources", func(machine map[string]any) error {
		for key, kind := range map[string]publicid.Kind{
			"machine_id":      publicid.KindMachine,
			"machine_pool_id": publicid.KindMachinePool,
		} {
			if err := internalIDField(machine, key, kind); err != nil {
				return err
			}
		}
		if value, exists := machine["secret_env_overlay"]; exists && value != nil {
			refs, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("secret_env_overlay must be an object")
			}
			for key, value := range refs {
				if value != nil {
					if err := internalIDField(refs, key, publicid.KindSecret); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := internalIDObjects(root, "skills", func(skill map[string]any) error {
		if _, exists := skill["id"]; exists {
			return fmt.Errorf("legacy skill already contains id")
		}
		if _, exists := skill["public_id"]; !exists {
			return fmt.Errorf("legacy skill is missing public_id")
		}
		if err := internalIDField(skill, "public_id", publicid.KindSkill); err != nil {
			return err
		}
		skill["id"] = skill["public_id"]
		delete(skill, "public_id")
		return nil
	}); err != nil {
		return nil, err
	}
	if err := internalSubagentModelIDs(root, resolveModel); err != nil {
		return nil, err
	}
	if value := root["event_webhook"]; value != nil {
		webhook, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("event_webhook must be an object")
		}
		if err := internalIDField(webhook, "signing_secret_id", publicid.KindSecret); err != nil {
			return nil, err
		}
	}
	if err := internalIDObjects(root, "mcp", func(server map[string]any) error {
		if value, exists := server["auth"]; exists && value != nil {
			auth, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("mcp auth must be an object")
			}
			return internalIDField(auth, "secret_id", publicid.KindSecret)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func internalSubagentModelIDs(
	root map[string]any,
	resolve func(key string, profileID uuid.UUID, provider, name string) (uuid.UUID, error),
) error {
	if root["subagents"] == nil {
		return nil
	}
	subagents, ok := root["subagents"].(map[string]any)
	if !ok {
		return fmt.Errorf("subagents must be an object")
	}
	for _, key := range slices.Sorted(maps.Keys(subagents)) {
		subagent, ok := subagents[key].(map[string]any)
		if !ok {
			return fmt.Errorf("subagent %q must be an object", key)
		}
		if err := internalIDField(subagent, "profile_id", publicid.KindAgentProfile); err != nil {
			return err
		}
		if subagent["model"] == nil {
			continue
		}
		model, ok := subagent["model"].(map[string]any)
		if !ok {
			return fmt.Errorf("subagent %q model must be an object", key)
		}
		provider, hasProvider := model["provider_config"]
		name, hasName := model["name"]
		if !hasProvider && !hasName {
			continue
		}
		providerName, providerOK := provider.(string)
		modelName, nameOK := name.(string)
		if (hasProvider && (!providerOK || providerName == "")) || (hasName && (!nameOK || modelName == "")) {
			return fmt.Errorf("subagent %q model names must be nonempty strings", key)
		}
		var profileID uuid.UUID
		if subagent["type"] == "profile" {
			text, _ := subagent["profile_id"].(string)
			var err error
			profileID, err = uuid.Parse(text)
			if err != nil || profileID == uuid.Nil {
				return fmt.Errorf("subagent %q has an invalid profile id", key)
			}
		}
		id, err := resolve(key, profileID, providerName, modelName)
		if err != nil {
			return fmt.Errorf("resolve subagent %q model: %w", key, err)
		}
		model["configured_model_id"] = id.String()
		delete(model, "provider_config")
		delete(model, "name")
	}
	return nil
}

func addSubagentModelSourceDefaults(raw []byte, format string, defaults map[string]map[string]string) ([]byte, error) {
	decode := decodeAgentConfigNameMigrationYAML
	if format == "json" {
		decode = decodeAgentConfigNameMigrationJSON
	} else if format != "yaml" {
		return nil, fmt.Errorf("unsupported source format %q", format)
	}
	expected, err := decode(raw)
	if err != nil {
		return nil, err
	}
	root, ok := expected.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("source must be an object")
	}
	subagents, _ := root["subagents"].(map[string]any)
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	type insertion struct {
		offset int
		text   string
	}
	var insertions []insertion
	patchable := true
	for _, key := range slices.Sorted(maps.Keys(defaults)) {
		subagent, _ := subagents[key].(map[string]any)
		model, ok := subagent["model"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("source subagent %q model is missing", key)
		}
		for field, value := range defaults[key] {
			if _, exists := model[field]; exists {
				return nil, fmt.Errorf("source subagent %q model differs from compiled selection", key)
			}
			model[field] = value
		}
		node := subagentModelSourceNode(document.Content[0], "subagents", key, "model")
		if node == nil || node.Kind != yaml.MappingNode || node.Anchor != "" || len(node.Content) == 0 {
			patchable = false
			continue
		}
		fields, err := json.MarshalIndent(defaults[key], "", "")
		if err != nil {
			return nil, err
		}
		text := strings.TrimSpace(string(fields[1 : len(fields)-1]))
		first := node.Content[0]
		if node.Style&yaml.FlowStyle != 0 {
			text += ", "
		} else {
			newline := "\n"
			if bytes.Contains(raw, []byte("\r\n")) {
				newline = "\r\n"
			}
			text += newline + strings.Repeat(" ", first.Column-1)
		}
		lines := bytes.SplitAfter(raw, []byte("\n"))
		offset := 0
		for _, line := range lines[:first.Line-1] {
			offset += len(line)
		}
		offset += len(string([]rune(string(lines[first.Line-1]))[:first.Column-1]))
		insertions = append(insertions, insertion{offset: offset, text: text})
	}
	if patchable {
		slices.SortFunc(insertions, func(a, b insertion) int { return b.offset - a.offset })
		patched := slices.Clone(raw)
		for _, edit := range insertions {
			patched = append(patched[:edit.offset], append([]byte(edit.text), patched[edit.offset:]...)...)
		}
		after, err := decode(patched)
		if err == nil && reflect.DeepEqual(expected, after) {
			return patched, nil
		}
	}
	if format == "json" {
		return json.MarshalIndent(expected, "", "  ")
	}
	encoded, err := yaml.Marshal(expected)
	if err != nil {
		return nil, err
	}
	after, err := decode(encoded)
	if err != nil || !reflect.DeepEqual(expected, after) {
		return nil, fmt.Errorf("adding subagent model defaults changed other source values")
	}
	return encoded, nil
}

func subagentModelSourceNode(node *yaml.Node, path ...string) *yaml.Node {
	for _, key := range path {
		if node == nil || node.Kind != yaml.MappingNode || node.Anchor != "" {
			return nil
		}
		var child *yaml.Node
		for index := 0; index < len(node.Content); index += 2 {
			if node.Content[index].Value == key {
				child = node.Content[index+1]
			}
		}
		node = child
	}
	return node
}

func internalIDField(object map[string]any, key string, kind publicid.Kind) error {
	value, exists := object[key]
	if !exists {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a public id", key)
	}
	id, err := publicid.Decode(kind, text)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	object[key] = id.String()
	return nil
}

func internalIDObjects(root map[string]any, key string, convert func(map[string]any) error) error {
	visit := func(value any) error {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s entries must be objects", key)
		}
		return convert(object)
	}
	switch values := root[key].(type) {
	case nil:
		return nil
	case []any:
		for _, value := range values {
			if err := visit(value); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, value := range values {
			if err := visit(value); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid %s", key)
	}
	return nil
}
