package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/pressly/goose/v3"
)

func newInternalConfigIDsMigration() *goose.Migration {
	return goose.NewGoMigration(42, &goose.GoFunc{RunTx: upInternalConfigIDs}, nil)
}

func upInternalConfigIDs(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs, machine_pools, project_machine_pool_grants,
machines, agent_machine_bindings, processes IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, configured_model_id, compiled_definition,
effective_definition_hash, compiler_version FROM agent_configs ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type update struct {
		id   uuid.UUID
		raw  []byte
		hash string
	}
	var updates []update
	for rows.Next() {
		var id, modelID uuid.UUID
		var raw []byte
		var hash, version string
		if err := rows.Scan(&id, &modelID, &raw, &hash, &version); err != nil {
			return err
		}
		if version == "1" {
			continue
		}
		if version != "" {
			return fmt.Errorf("config %s has unsupported compiler version %q", id, version)
		}
		previousHash, err := explicitDefaultToolsConfigHash(raw)
		if err != nil {
			return err
		}
		if previousHash != hash {
			return fmt.Errorf("config %s compiled hash does not match content", id)
		}
		converted, err := internalCompiledIDs(raw, modelID)
		if err != nil {
			return fmt.Errorf("config %s: %w", id, err)
		}
		updatedHash, err := explicitDefaultToolsConfigHash(converted)
		if err != nil {
			return err
		}
		updates = append(updates, update{id: id, raw: converted, hash: updatedHash})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(updates) > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
			return err
		}
		for _, config := range updates {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET compiled_definition = $2::jsonb,
effective_definition_hash = $3, compiler_version = '1' WHERE id = $1`,
				config.id, config.raw, config.hash); err != nil {
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

func internalCompiledIDs(raw []byte, modelID uuid.UUID) ([]byte, error) {
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
	if err := internalIDObjects(root, "subagents", func(subagent map[string]any) error {
		return internalIDField(subagent, "profile_id", publicid.KindAgentProfile)
	}); err != nil {
		return nil, err
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
