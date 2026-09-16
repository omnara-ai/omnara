package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/pressly/goose/v3"
)

func newExplicitDefaultToolsMigration() *goose.Migration {
	return goose.NewGoMigration(38, &goose.GoFunc{RunTx: upExplicitDefaultTools}, nil)
}

func upExplicitDefaultTools(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, source, source_format, source_hash,
		       definition::text, compiled_definition::text, effective_definition_hash
		FROM agent_configs
		WHERE (COALESCE(jsonb_array_length(NULLIF(compiled_definition->'skills', 'null'::jsonb)), 0) > 0
		       AND NOT COALESCE(compiled_definition->'tools', '{}'::jsonb) ? 'skill')
		   OR (COALESCE(NULLIF(compiled_definition->'subagents', 'null'::jsonb), '{}'::jsonb) <> '{}'::jsonb
		       AND NOT COALESCE(compiled_definition->'tools', '{}'::jsonb) ?&
		           ARRAY['spawn_agent', 'read_agent', 'send_agent_message', 'stop_agent', 'list_agents'])
		   OR ((EXISTS (SELECT 1
		                FROM jsonb_each(COALESCE(NULLIF(compiled_definition->'tools', 'null'::jsonb), '{}'::jsonb)) AS tool
		                WHERE tool.value->'enabled' = 'true'::jsonb)
		        OR COALESCE(NULLIF(compiled_definition->'mcp', 'null'::jsonb), '{}'::jsonb) <> '{}'::jsonb)
		       AND NOT COALESCE(compiled_definition->'tools', '{}'::jsonb) ?& ARRAY['read_file', 'search_files'])
		ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var updates []storedAgentConfig
	for rows.Next() {
		var config storedAgentConfig
		if err := rows.Scan(&config.id, &config.source, &config.sourceFormat, &config.sourceHash,
			&config.definition, &config.compiledDefinition, &config.effectiveDefinitionHash); err != nil {
			return err
		}
		migrated, changed, err := migrateExplicitDefaultTools(config)
		if err != nil {
			return fmt.Errorf("materialize default tools in config %s: %w", config.id, err)
		}
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
	if len(updates) > 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`); err != nil {
			return err
		}
		for _, config := range updates {
			if _, err := tx.ExecContext(ctx, `
				UPDATE agent_configs SET definition = $2::jsonb,
				    compiled_definition = $3::jsonb, effective_definition_hash = $4
				WHERE id = $1::uuid`, config.id,
				config.definition, config.compiledDefinition, config.effectiveDefinitionHash); err != nil {
				return fmt.Errorf("update default tools in config %s: %w", config.id, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`); err != nil {
			return err
		}
	}
	return nil
}

func migrateExplicitDefaultTools(config storedAgentConfig) (storedAgentConfig, bool, error) {
	compiled, additions, err := addExplicitDefaultTools(config.compiledDefinition)
	if err != nil || len(additions) == 0 {
		return config, false, err
	}
	definition, definitionAdditions, err := addExplicitDefaultTools(config.definition)
	if err != nil {
		return config, false, err
	}
	if !slices.Equal(additions, definitionAdditions) {
		return config, false, errors.New("default tools differ between definition and compiled definition")
	}
	previousHash, err := explicitDefaultToolsConfigHash(config.compiledDefinition)
	if err != nil {
		return config, false, err
	}
	if hashBytes([]byte(config.source)) != config.sourceHash || previousHash != config.effectiveDefinitionHash {
		return config, false, errors.New("stored config hashes do not match content")
	}
	updatedHash, err := explicitDefaultToolsConfigHash(compiled)
	if err != nil {
		return config, false, err
	}
	config.definition = definition
	config.compiledDefinition = compiled
	config.effectiveDefinitionHash = updatedHash
	return config, true, nil
}

func explicitDefaultToolsConfigHash(raw []byte) (string, error) {
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

func addExplicitDefaultTools(raw []byte) ([]byte, []string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, err
	}
	var skills []json.RawMessage
	if len(fields["skills"]) > 0 {
		if err := json.Unmarshal(fields["skills"], &skills); err != nil {
			return nil, nil, err
		}
	}
	var subagents map[string]json.RawMessage
	if len(fields["subagents"]) > 0 {
		if err := json.Unmarshal(fields["subagents"], &subagents); err != nil {
			return nil, nil, err
		}
	}
	var additions []string
	if len(skills) > 0 {
		additions = append(additions, "skill")
	}
	if len(subagents) > 0 {
		additions = append(additions, "spawn_agent", "read_agent", "send_agent_message", "stop_agent", "list_agents")
	}
	tools := make(map[string]json.RawMessage)
	if len(fields["tools"]) > 0 {
		if err := json.Unmarshal(fields["tools"], &tools); err != nil {
			return nil, nil, err
		}
	}
	additions = slices.DeleteFunc(additions, func(name string) bool {
		_, configured := tools[name]
		return configured
	})
	var mcp map[string]json.RawMessage
	if len(fields["mcp"]) > 0 {
		if err := json.Unmarshal(fields["mcp"], &mcp); err != nil {
			return nil, nil, err
		}
	}
	hasTools := len(additions) > 0 || len(mcp) > 0
	for _, raw := range tools {
		var tool struct{ Enabled bool }
		if err := json.Unmarshal(raw, &tool); err != nil {
			return nil, nil, err
		}
		if tool.Enabled {
			hasTools = true
			break
		}
	}
	if hasTools {
		for _, name := range []string{"read_file", "search_files"} {
			if _, configured := tools[name]; !configured {
				additions = append(additions, name)
			}
		}
	}
	if len(additions) == 0 {
		return raw, nil, nil
	}
	if tools == nil {
		tools = make(map[string]json.RawMessage)
	}
	for _, name := range additions {
		tools[name] = json.RawMessage(`{"enabled":true,"permission":{"mode":"always_allow","parameters":{}}}`)
	}
	var err error
	fields["tools"], err = json.Marshal(tools)
	if err != nil {
		return nil, nil, err
	}
	updated, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := canonicalJSON(updated)
	return canonical, additions, err
}
