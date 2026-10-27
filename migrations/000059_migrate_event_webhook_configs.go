package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

func newEventWebhookConfigMigration() *goose.Migration {
	return goose.NewGoMigration(59, &goose.GoFunc{RunTx: upEventWebhookConfigs}, nil)
}

type eventWebhookConfig struct {
	id, projectID, modelID, hash string
	source, format, sourceHash   sql.NullString
	compiled                     []byte
}

func upEventWebhookConfigs(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs, agents, agent_profiles,
		agent_profile_versions, agent_inputs, model_call_contexts, event_webhook_deliveries,
		event_webhooks, integration_inbox, tool_calls, model_outputs IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	configs, err := eventWebhookConfigRewrites(ctx, tx)
	if err != nil {
		return err
	}
	if err := migrateEventWebhookSubscriptions(ctx, tx); err != nil {
		return err
	}
	if len(configs) == 0 {
		return nil
	}
	guards := [][2]string{
		{"agent_configs", "agent_configs_immutable"},
		{"agent_inputs", "agent_inputs_mutation_policy"},
		{"agent_profile_versions", "agent_profile_versions_direct_mutation_blocked"},
		{"model_call_contexts", "model_call_contexts_transition_guard"},
	}
	for _, guard := range guards {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+guard[0]+" DISABLE TRIGGER "+guard[1]); err != nil {
			return err
		}
	}
	replacements := make(map[string]string)
	for _, config := range configs {
		var existing eventWebhookConfig
		err := tx.QueryRowContext(ctx, `SELECT id::text, configured_model_id::text, source, compiled_definition::text
			FROM agent_configs WHERE project_id=$1::uuid AND effective_definition_hash=$2
			AND source_format IS NOT DISTINCT FROM $3::text AND source_hash IS NOT DISTINCT FROM $4::text`,
			config.projectID, config.hash, config.format, config.sourceHash,
		).Scan(&existing.id, &existing.modelID, &existing.source, &existing.compiled)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `UPDATE agent_configs SET source=$2, source_hash=$3,
				compiled_definition=$4::jsonb, effective_definition_hash=$5 WHERE id=$1::uuid`,
				config.id, config.source, config.sourceHash, config.compiled, config.hash)
		case err == nil:
			var oldValue, newValue any
			if err := json.Unmarshal(existing.compiled, &oldValue); err != nil {
				return err
			}
			if err := json.Unmarshal(config.compiled, &newValue); err != nil {
				return err
			}
			if existing.modelID != config.modelID || existing.source != config.source || !reflect.DeepEqual(oldValue, newValue) {
				return fmt.Errorf("webhook migration hash collision between configs %s and %s", existing.id, config.id)
			}
			replacements[config.id] = existing.id
		}
		if err != nil {
			return fmt.Errorf("migrate webhook config %s: %w", config.id, err)
		}
	}
	if err := mergeEventWebhookConfigs(ctx, tx, replacements); err != nil {
		return err
	}
	for _, guard := range guards {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+guard[0]+" ENABLE TRIGGER "+guard[1]); err != nil {
			return err
		}
	}
	return nil
}

func eventWebhookConfigRewrites(ctx context.Context, tx *sql.Tx) ([]eventWebhookConfig, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id::text, project_id::text, configured_model_id::text,
		source, source_format, source_hash, compiled_definition::text, effective_definition_hash
		FROM agent_configs WHERE compiled_definition ? 'event_webhook' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var configs []eventWebhookConfig
	for rows.Next() {
		var config eventWebhookConfig
		if err := rows.Scan(&config.id, &config.projectID, &config.modelID, &config.source,
			&config.format, &config.sourceHash, &config.compiled, &config.hash); err != nil {
			return nil, err
		}
		migrated, err := rewriteEventWebhookConfig(config)
		if err != nil {
			return nil, fmt.Errorf("rewrite webhook config %s: %w", config.id, err)
		}
		configs = append(configs, migrated)
	}
	return configs, rows.Err()
}

func rewriteEventWebhookConfig(config eventWebhookConfig) (eventWebhookConfig, error) {
	oldHash, err := explicitDefaultToolsConfigHash(config.compiled)
	if err != nil {
		return config, err
	}
	if oldHash != config.hash ||
		(config.source.Valid && hashBytes([]byte(config.source.String)) != config.sourceHash.String) {
		return config, errors.New("stored config hashes do not match content")
	}
	config.compiled, err = removeEventWebhookJSON(config.compiled, true)
	if err != nil {
		return config, err
	}
	config.hash, err = explicitDefaultToolsConfigHash(config.compiled)
	if err != nil || !config.source.Valid {
		return config, err
	}
	var source []byte
	switch config.format.String {
	case "json":
		source, err = removeEventWebhookJSON([]byte(config.source.String), false)
	case "yaml":
		source, err = removeEventWebhookYAML([]byte(config.source.String))
	default:
		err = fmt.Errorf("unsupported source format %q", config.format.String)
	}
	if err != nil {
		return config, err
	}
	config.source.String = string(source)
	config.sourceHash.String = hashBytes(source)
	return config, nil
}

func removeEventWebhookJSON(raw []byte, escapeHTML bool) ([]byte, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("config must be an object")
	}
	if _, exists := fields["event_webhook"]; !exists {
		return raw, nil
	}
	delete(fields, "event_webhook")
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(escapeHTML)
	if err := encoder.Encode(fields); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(encoded.Bytes(), []byte("\n")), nil
}

func removeEventWebhookYAML(raw []byte) ([]byte, error) {
	before, err := decodeAgentConfigNameMigrationYAML(raw)
	if err != nil {
		return nil, err
	}
	fields, ok := before.(map[string]any)
	if !ok {
		return nil, errors.New("config must be an object")
	}
	if _, exists := fields["event_webhook"]; !exists {
		return raw, nil
	}
	delete(fields, "event_webhook")
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	root := document.Content[0]
	var result []byte
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Value != "event_webhook" {
			continue
		}
		if root.Style&yaml.FlowStyle != 0 {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			result, err = yaml.Marshal(&document)
		} else {
			lines := bytes.SplitAfter(raw, []byte("\n"))
			start, end := key.Line-1, len(lines)
			if i+2 < len(root.Content) {
				end = root.Content[i+2].Line - 1
			}
			for end > start+1 {
				line := strings.TrimSpace(string(lines[end-1]))
				if line != "" && !strings.HasPrefix(line, "#") && line != "..." {
					break
				}
				end--
			}
			result = bytes.Join(append(lines[:start:start], lines[end:]...), nil)
		}
		break
	}
	if err != nil {
		return nil, err
	}
	after, err := decodeAgentConfigNameMigrationYAML(result)
	if err == nil && reflect.DeepEqual(after, fields) {
		return result, nil
	}
	result, err = yaml.Marshal(fields)
	if err != nil {
		return nil, err
	}
	after, err = decodeAgentConfigNameMigrationYAML(result)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(after, fields) {
		return nil, errors.New("removing event_webhook changed other source values")
	}
	return result, nil
}

func mergeEventWebhookConfigs(ctx context.Context, tx *sql.Tx, replacements map[string]string) error {
	if len(replacements) == 0 {
		return nil
	}
	raw, err := json.Marshal(replacements)
	if err != nil {
		return err
	}
	for _, statement := range []string{
		`UPDATE agents SET current_config_id=replacement.value::uuid
			FROM jsonb_each_text($1::jsonb) replacement WHERE current_config_id=replacement.key::uuid`,
		`UPDATE agent_profile_versions SET agent_config_id=replacement.value::uuid
			FROM jsonb_each_text($1::jsonb) replacement WHERE agent_config_id=replacement.key::uuid`,
		`UPDATE model_call_contexts SET agent_config_id=replacement.value::uuid
			FROM jsonb_each_text($1::jsonb) replacement WHERE agent_config_id=replacement.key::uuid`,
		`UPDATE agent_inputs SET agent_config_id=replacement.value::uuid,
			metadata=CASE WHEN metadata->>'agent_config_id'=replacement.key
				THEN jsonb_set(metadata,'{agent_config_id}',to_jsonb(replacement.value)) ELSE metadata END
			FROM jsonb_each_text($1::jsonb) replacement WHERE agent_config_id=replacement.key::uuid`,
		`WITH updated AS (
			SELECT inbox.id, jsonb_object_agg(recipient.key,
				CASE WHEN $1::jsonb ? (recipient.value#>>'{launch,agent_config_id}')
					OR $1::jsonb ? (recipient.value#>>'{launch,derived_base_config_id}')
				THEN jsonb_set(recipient.value,'{launch}',recipient.value->'launch' ||
					jsonb_strip_nulls(jsonb_build_object(
						'agent_config_id',$1::jsonb -> (recipient.value#>>'{launch,agent_config_id}'),
						'derived_base_config_id',$1::jsonb -> (recipient.value#>>'{launch,derived_base_config_id}'))))
				ELSE recipient.value END) AS recipients
			FROM integration_inbox inbox CROSS JOIN LATERAL jsonb_each(
				CASE WHEN jsonb_typeof(inbox.plan->'recipients')='object'
					THEN inbox.plan->'recipients' ELSE '{}'::jsonb END) recipient
			GROUP BY inbox.id
			HAVING bool_or($1::jsonb ? (recipient.value#>>'{launch,agent_config_id}')
				OR $1::jsonb ? (recipient.value#>>'{launch,derived_base_config_id}'))
		) UPDATE integration_inbox inbox SET plan=jsonb_set(inbox.plan,'{recipients}',updated.recipients)
			FROM updated WHERE inbox.id=updated.id`,
		`DELETE FROM agent_configs config USING jsonb_each_text($1::jsonb) replacement
			WHERE config.id=replacement.key::uuid`,
	} {
		if _, err := tx.ExecContext(ctx, statement, raw); err != nil {
			return err
		}
	}
	return nil
}

func migrateEventWebhookSubscriptions(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE legacy_event_webhook_configs ON COMMIT DROP AS
		SELECT id, compiled_definition->'event_webhook'->>'url' AS url,
			ARRAY(SELECT DISTINCT jsonb_array_elements_text(compiled_definition->'event_webhook'->'events')
				ORDER BY 1) AS events,
			NULLIF(compiled_definition->'event_webhook'->>'signing_secret_id',
				'00000000-0000-0000-0000-000000000000')::uuid AS signing_secret_id
		FROM agent_configs WHERE jsonb_typeof(compiled_definition->'event_webhook')='object'`); err != nil {
		return err
	}
	var conflict string
	err := tx.QueryRowContext(ctx, `SELECT agent.id::text FROM agents agent
		JOIN agent_profiles profile ON profile.id=agent.agent_profile_id AND profile.deleted_at IS NULL
		JOIN agent_profile_versions version ON version.id=profile.current_version_id
		JOIN legacy_event_webhook_configs inherited ON inherited.id=version.agent_config_id
		LEFT JOIN legacy_event_webhook_configs own ON own.id=agent.current_config_id
		JOIN projects project ON project.id=agent.project_id AND project.deleted_at IS NULL
		JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
		WHERE ROW(inherited.url,inherited.events,inherited.signing_secret_id)
			IS DISTINCT FROM ROW(own.url,own.events,own.signing_secret_id)
		AND (agent.state='active' OR EXISTS(SELECT 1 FROM event_webhook_deliveries delivery
			WHERE delivery.agent_id=agent.id AND delivery.webhook_id IS NULL))
		ORDER BY agent.id LIMIT 1`).Scan(&conflict)
	if err == nil {
		return fmt.Errorf("agent %s and its profile have different webhooks; resolve before migrating", conflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	err = tx.QueryRowContext(ctx, `WITH self_spawns AS (
		SELECT agent.id AS agent_id, config.id AS config_id FROM agents agent
		JOIN agent_configs config ON config.id=agent.current_config_id
		WHERE config.compiled_definition#>>'{tools,spawn_agent,enabled}'='true'
		AND EXISTS(SELECT 1 FROM jsonb_each(
			coalesce(nullif(config.compiled_definition->'subagents','null'::jsonb),'{}')) subagent
			WHERE subagent.value->>'type'='self')
		UNION
		SELECT call.agent_id, context.agent_config_id FROM tool_calls call
		JOIN model_outputs output ON output.agent_id=call.agent_id AND output.id=call.model_output_id
		JOIN model_call_contexts context ON context.agent_id=output.agent_id AND context.id=output.model_call_context_id
		JOIN agent_configs config ON config.id=context.agent_config_id
		WHERE call.type='built_in' AND call.name='spawn_agent' AND call.state<>'completed'
		AND config.compiled_definition#>>'{tools,spawn_agent,enabled}'='true'
		AND config.compiled_definition->'subagents'->btrim(call.input->>'agent',
			U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')->>'type'='self'
	) SELECT agent.id::text FROM self_spawns spawn
		JOIN agents agent ON agent.id=spawn.agent_id
		JOIN legacy_event_webhook_configs own ON own.id=spawn.config_id
		JOIN projects project ON project.id=agent.project_id AND project.deleted_at IS NULL
		JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
		WHERE agent.state='active'
		AND NOT EXISTS(SELECT 1 FROM agent_profiles profile
			JOIN agent_profile_versions version ON version.id=profile.current_version_id
			JOIN legacy_event_webhook_configs inherited ON inherited.id=version.agent_config_id
			WHERE profile.id=agent.agent_profile_id AND profile.deleted_at IS NULL
			AND ROW(inherited.url,inherited.events,inherited.signing_secret_id)
				IS NOT DISTINCT FROM ROW(own.url,own.events,own.signing_secret_id))
		ORDER BY agent.id LIMIT 1`).Scan(&conflict)
	if err == nil {
		return fmt.Errorf("agent %s would lose webhook delivery for future self-subagents; "+
			"finish its work, relaunch under a profile with the same webhook URL, events, and signing secret, "+
			"archive the original, then rerun the preflight", conflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, statement := range []string{
		`INSERT INTO event_webhooks(org_id,project_id,agent_profile_id,url,event_types,signing_secret_id)
		 SELECT project.org_id,profile.project_id,profile.id,config.url,config.events,config.signing_secret_id
		 FROM agent_profiles profile
		 JOIN agent_profile_versions version ON version.id=profile.current_version_id
		 JOIN legacy_event_webhook_configs config ON config.id=version.agent_config_id
		 JOIN projects project ON project.id=profile.project_id AND project.deleted_at IS NULL
		 JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
		 WHERE profile.deleted_at IS NULL`,
		`INSERT INTO event_webhooks(org_id,project_id,agent_id,url,event_types,signing_secret_id)
		 SELECT agent.org_id,agent.project_id,agent.id,config.url,config.events,config.signing_secret_id
		 FROM agents agent JOIN legacy_event_webhook_configs config ON config.id=agent.current_config_id
		 JOIN projects project ON project.id=agent.project_id AND project.deleted_at IS NULL
		 JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
		 WHERE NOT EXISTS(SELECT 1 FROM event_webhooks webhook WHERE webhook.agent_profile_id=agent.agent_profile_id)
		 AND (agent.state='active' OR EXISTS(SELECT 1 FROM event_webhook_deliveries delivery
			WHERE delivery.agent_id=agent.id AND delivery.webhook_id IS NULL))`,
		`DELETE FROM event_webhook_deliveries delivery WHERE delivery.webhook_id IS NULL AND NOT EXISTS(
		 SELECT 1 FROM agents agent JOIN legacy_event_webhook_configs config ON config.id=agent.current_config_id
		 JOIN projects project ON project.id=agent.project_id AND project.deleted_at IS NULL
		 JOIN orgs org ON org.id=project.org_id AND org.deleted_at IS NULL
		 WHERE agent.id=delivery.agent_id)`,
		`UPDATE event_webhook_deliveries delivery SET webhook_id=webhook.id
		 FROM agents agent JOIN event_webhooks webhook ON webhook.agent_id=agent.id
			OR webhook.agent_profile_id=agent.agent_profile_id
		 WHERE delivery.agent_id=agent.id AND delivery.webhook_id IS NULL`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var unmapped bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM event_webhook_deliveries WHERE webhook_id IS NULL)`,
	).Scan(&unmapped); err != nil {
		return err
	}
	if unmapped {
		return errors.New("webhook migration left a delivery without a subscription")
	}
	return nil
}
