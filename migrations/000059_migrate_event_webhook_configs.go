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
		event_webhooks IN ACCESS EXCLUSIVE MODE`); err != nil {
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
			err = mergeEventWebhookConfig(ctx, tx, config.id, existing.id)
		}
		if err != nil {
			return fmt.Errorf("migrate webhook config %s: %w", config.id, err)
		}
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

func mergeEventWebhookConfig(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	for _, statement := range []string{
		`UPDATE agents SET current_config_id=$2::uuid WHERE current_config_id=$1::uuid`,
		`UPDATE agent_profile_versions SET agent_config_id=$2::uuid WHERE agent_config_id=$1::uuid`,
		`UPDATE model_call_contexts SET agent_config_id=$2::uuid WHERE agent_config_id=$1::uuid`,
		`UPDATE agent_inputs SET agent_config_id=$2::uuid,
			metadata=CASE WHEN metadata->>'agent_config_id'=$1::text
				THEN jsonb_set(metadata,'{agent_config_id}',to_jsonb($2::text)) ELSE metadata END
			WHERE agent_config_id=$1::uuid`,
	} {
		if _, err := tx.ExecContext(ctx, statement, oldID, newID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM agent_configs WHERE id=$1::uuid`, oldID)
	return err
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
		 WHERE NOT EXISTS(SELECT 1 FROM event_webhooks webhook WHERE webhook.agent_profile_id=agent.agent_profile_id)`,
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
