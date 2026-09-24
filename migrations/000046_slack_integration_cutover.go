package migrations

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strings"

	"github.com/pressly/goose/v3"
	"gopkg.in/yaml.v3"
)

func newSlackIntegrationCutoverMigration() *goose.Migration {
	return goose.NewGoMigration(46, &goose.GoFunc{RunTx: upSlackIntegrationCutover}, nil)
}

// Keep encoding local: migration replay must not depend on future config compilers.
func upSlackIntegrationCutover(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `LOCK TABLE agent_configs, agents IN ACCESS EXCLUSIVE MODE`); err != nil {
		return err
	}
	if err := preflightSlackIntegrationCutover(ctx, tx); err != nil {
		return err
	}
	integrations, err := slackCutoverIntegrations(ctx, tx)
	if err != nil {
		return err
	}
	return migrateSlackAgentTools(ctx, tx, integrations)
}

type integrationCutoverConfig struct {
	id, projectID, hash        string
	source, format, sourceHash sql.NullString
	compiled                   []byte
}

type slackCutoverIntegration struct {
	id, name string
	deleted  bool
}

func (integration slackCutoverIntegration) toolName() string {
	return "int__" + integration.name + "__post_message"
}

func slackCutoverIntegrations(ctx context.Context, tx *sql.Tx) (map[string][]slackCutoverIntegration, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT project_id::text,id::text,name,deleted_at IS NOT NULL FROM project_integrations
		WHERE integration_type='slack_thread' ORDER BY project_id,created_at,id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	integrations := map[string][]slackCutoverIntegration{}
	for rows.Next() {
		var projectID string
		var integration slackCutoverIntegration
		if err := rows.Scan(&projectID, &integration.id, &integration.name, &integration.deleted); err != nil {
			return nil, err
		}
		integrations[projectID] = append(integrations[projectID], integration)
	}
	return integrations, rows.Err()
}

func rewriteSlackIntegrationConfigs(
	ctx context.Context,
	tx *sql.Tx,
	integrations map[string][]slackCutoverIntegration,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, project_id::text, source, source_format, source_hash,
		       compiled_definition::text, effective_definition_hash
		FROM agent_configs ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var updates []integrationCutoverConfig
	for rows.Next() {
		var config integrationCutoverConfig
		if err := rows.Scan(&config.id, &config.projectID, &config.source, &config.format,
			&config.sourceHash, &config.compiled, &config.hash); err != nil {
			return err
		}
		updated, changed, err := rewriteSlackIntegrationConfig(config, integrations[config.projectID])
		if err != nil {
			return fmt.Errorf("slack integration cutover config %s: %w", config.id, err)
		}
		if changed {
			updates = append(updates, updated)
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
				"rewrite Slack config %s (resolve duplicate configs before retrying if necessary): %w",
				config.id,
				err,
			)
		}
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
	return err
}

func rewriteSlackIntegrationConfig(
	config integrationCutoverConfig,
	integrations []slackCutoverIntegration,
) (integrationCutoverConfig, bool, error) {
	compiled, compiledChanged, err := rewriteSlackToolsJSON(config.compiled, integrations, true)
	if err != nil {
		return config, false, err
	}
	var source []byte
	var sourceChanged bool
	if config.source.Valid {
		switch config.format.String {
		case "json":
			source, sourceChanged, err = rewriteSlackToolsJSON([]byte(config.source.String), integrations, false)
		case "yaml":
			source, sourceChanged, err = rewriteSlackToolsYAML([]byte(config.source.String), integrations)
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
	config.hash, err = explicitDefaultToolsConfigHash(compiled)
	if sourceChanged {
		config.source.String, config.sourceHash.String = string(source), hashBytes(source)
	}
	return config, true, err
}

// Released send policies supported only always_allow; disabling the tool represented denial.
func slackCutoverSendPolicy(value any) (map[string]any, error) {
	tool, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("legacy send policy must be an object; repair before cutover")
	}
	policy := maps.Clone(tool)
	for key, value := range tool {
		switch key {
		case "type":
			if value != "built_in" {
				return nil, errors.New("legacy send type must be built_in")
			}
			delete(policy, key)
		case "enabled", "deferred":
			// Released JSON/YAML treats enabled: null as the default; compiled policies use booleans.
			if key == "enabled" && value == nil {
				delete(policy, key)
				continue
			}
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("legacy send %s must be boolean", key)
			}
		case "permission":
			permission, ok := value.(map[string]any)
			if !ok || permission["mode"] != "always_allow" {
				return nil, errors.New("legacy send only supports always_allow; repair before cutover")
			}
			for field, parameter := range permission {
				if field == "mode" {
					continue
				}
				parameters, ok := parameter.(map[string]any)
				if field != "parameters" || !ok || len(parameters) != 0 {
					return nil, errors.New("legacy send policy has unsupported settings; repair before cutover")
				}
			}
		default:
			return nil, fmt.Errorf("legacy send has unsupported field %q; repair before cutover", key)
		}
	}
	return policy, nil
}

func rewriteSlackToolsJSON(raw []byte, integrations []slackCutoverIntegration, compiled bool) ([]byte, bool, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, false, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, false, errors.New("config must be an object")
	}
	changed, err := rewriteSlackToolKeys(root, integrations, compiled)
	if err != nil || !changed {
		return raw, changed, err
	}
	encoded, err := json.Marshal(root)
	return encoded, true, err
}

func rewriteSlackToolKeys(root map[string]any, integrations []slackCutoverIntegration, compiled bool) (bool, error) {
	tools, _ := root["tools"].(map[string]any)
	changed := false
	if value, exists := tools["send_integration_message"]; exists {
		policy, err := slackCutoverSendPolicy(value)
		if err != nil {
			return false, err
		}
		if policy["enabled"] == false {
			if len(integrations) == 0 {
				return false, errors.New("disabled legacy send policy has no known Slack integration; repair before cutover")
			}
			// Deleted integration names cannot resolve when this source is saved again.
			for _, integration := range integrations {
				if integration.deleted {
					continue
				}
				tool := maps.Clone(policy)
				if compiled {
					tool["integration_id"] = integration.id
				}
				tools[integration.toolName()] = tool
			}
		}
		delete(tools, "send_integration_message")
		changed = true
	}
	if policy, exists := tools["set_integration_target"]; exists {
		if current, exists := tools["set_interaction_handler"]; exists && !reflect.DeepEqual(current, policy) {
			return false, errors.New("legacy and new handler selection tools have different settings")
		}
		tools["set_interaction_handler"] = policy
		delete(tools, "set_integration_target")
		changed = true
	}
	return changed, nil
}

func rewriteSlackToolsYAML(raw []byte, integrations []slackCutoverIntegration) ([]byte, bool, error) {
	value, err := decodeAgentConfigNameMigrationYAML(raw)
	if err != nil {
		return nil, false, err
	}
	expected, ok := value.(map[string]any)
	if !ok {
		return nil, false, errors.New("config must be an object")
	}
	changed, err := rewriteSlackToolKeys(expected, integrations, false)
	if err != nil || !changed {
		return raw, changed, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, false, err
	}
	root := document.Content[0]
	// Expand aliases before editing shared YAML nodes to avoid changing their other uses.
	if fileToolYAMLHasReferences(root) {
		if err := root.Encode(expected); err != nil {
			return nil, false, err
		}
	} else {
		var tools *yaml.Node
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == "tools" {
				tools = root.Content[i+1]
			}
		}
		if tools == nil || tools.Kind != yaml.MappingNode {
			return nil, false, errors.New("tools must be a mapping")
		}
		var entries []*yaml.Node
		for i := 0; i < len(tools.Content); i += 2 {
			key, value := tools.Content[i], tools.Content[i+1]
			switch key.Value {
			case "send_integration_message":
				var policyFields []*yaml.Node
				for j := 0; j < len(value.Content); j += 2 {
					field, setting := value.Content[j], value.Content[j+1]
					if field.Value == "type" || (field.Value == "enabled" && setting.Tag == "!!null") {
						continue
					}
					policyFields = append(policyFields, field, setting)
				}
				value.Content = policyFields
				for _, integration := range integrations {
					if _, exists := expected["tools"].(map[string]any)[integration.toolName()]; exists {
						renamedKey := *key
						renamedKey.Value = integration.toolName()
						entries = append(entries, &renamedKey, value)
					}
				}
			case "set_integration_target":
				key.Value = "set_interaction_handler"
				entries = append(entries, key, value)
			default:
				entries = append(entries, key, value)
			}
		}
		tools.Content = entries
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, false, err
	}
	if err := encoder.Close(); err != nil {
		return nil, false, err
	}
	decoded, err := decodeAgentConfigNameMigrationYAML(encoded.Bytes())
	if err != nil || !reflect.DeepEqual(expected, decoded) {
		return nil, false, errors.New("rewriting Slack tools changed other source values")
	}
	return encoded.Bytes(), true, nil
}

type slackCutoverTarget struct {
	id, integrationID, integrationName, kind, ref string
}

var slackCutoverChannel = regexp.MustCompile(`^[CDG][A-Z0-9]+$`)
var slackCutoverTimestamp = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

func slackSendingSuccessor(raw []byte, targets []slackCutoverTarget) ([]byte, error) {
	value, err := decodeAgentConfigNameMigrationJSON(raw)
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("compiled config must be an object")
	}
	tools, _ := root["tools"].(map[string]any)
	if tools == nil {
		tools = map[string]any{}
		root["tools"] = tools
	}
	policy, exists := tools["send_integration_message"]
	if !exists {
		policy = map[string]any{
			"enabled":    true,
			"permission": map[string]any{"mode": "always_allow", "parameters": map[string]any{}},
		}
	}
	tool, err := slackCutoverSendPolicy(policy)
	if err != nil {
		return nil, err
	}
	delete(tools, "send_integration_message")
	if _, err := rewriteSlackToolKeys(root, nil, true); err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, target := range targets {
		if previous, exists := seen[target.integrationID]; exists {
			return nil, fmt.Errorf(
				"targets %s and %s belong to one Slack integration; repair before cutover", previous, target.id,
			)
		}
		seen[target.integrationID] = target.id
		integration := slackCutoverIntegration{id: target.integrationID, name: target.integrationName}
		switch target.kind {
		case "dm", "channel":
			if !slackCutoverChannel.MatchString(target.ref) {
				return nil, fmt.Errorf("invalid Slack target %s address %q", target.id, target.ref)
			}
		case "thread":
			channel, timestamp, found := strings.Cut(target.ref, ":")
			if !found || !slackCutoverChannel.MatchString(channel) || !slackCutoverTimestamp.MatchString(timestamp) {
				return nil, fmt.Errorf("invalid Slack target %s", target.id)
			}
		default:
			return nil, fmt.Errorf("unsupported Slack target %s kind %q", target.id, target.kind)
		}
		sending := maps.Clone(tool)
		sending["integration_id"] = integration.id
		tools[integration.toolName()] = sending
	}
	return json.Marshal(root)
}

func preflightSlackIntegrationCutover(ctx context.Context, tx *sql.Tx) error {
	var collisionConfig, collisionTool string
	collisionErr := tx.QueryRowContext(ctx, `SELECT config.id::text, tool.key
 FROM agent_configs config
 CROSS JOIN LATERAL jsonb_each(coalesce(nullif(config.compiled_definition->'tools','null'::jsonb),'{}'::jsonb)) tool
 WHERE tool.value->>'type'='custom' AND (starts_with(tool.key,'int__')
 OR tool.key IN ('list_interaction_handlers','set_interaction_handler')) LIMIT 1`,
	).Scan(&collisionConfig, &collisionTool)
	if collisionErr == nil {
		return fmt.Errorf(
			"config %s custom tool %q conflicts with a new integration built-in; historical configs require repair before cutover; remain in maintenance and follow the cutover recovery runbook",
			collisionConfig,
			collisionTool,
		)
	}
	if !errors.Is(collisionErr, sql.ErrNoRows) {
		return collisionErr
	}
	var unfinished bool
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM agent_runtime_locks WHERE lease_expires_at > statement_timestamp())
		OR EXISTS (SELECT 1 FROM model_call_contexts WHERE state='started')
		OR EXISTS (SELECT 1 FROM tool_calls WHERE state <> 'completed')
		OR EXISTS (SELECT 1 FROM agent_interactions WHERE state='open')`).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished {
		return errors.New(
			"slack integration cutover requires the documented maintenance window: unfinished work remains; stay in maintenance and follow the cutover recovery runbook (the old release cannot run on schema 45)",
		)
	}
	// An idle worker can still have a continuation with no model call inserted yet.
	var agentID string
	err := tx.QueryRowContext(ctx, `SELECT agent.id::text FROM agents agent
		JOIN LATERAL (SELECT id FROM agent_turns WHERE agent_id=agent.id ORDER BY turn_sequence DESC LIMIT 1) latest ON true
		WHERE EXISTS (SELECT 1 FROM agent_continuable_model_contexts(agent.project_id,agent.id) context
		              WHERE context.turn_id=latest.id)
		   OR agent_has_incomplete_tool_batch(agent.project_id,agent.id)
		   OR EXISTS (SELECT 1 FROM agent_next_model_work(agent.project_id,agent.id) frontier
		              WHERE frontier.turn_id=latest.id)
		LIMIT 1`).Scan(&agentID)
	if err == nil {
		return fmt.Errorf(
			"slack integration cutover: agent %s still has continuable work; remain in maintenance and follow the cutover recovery runbook",
			agentID,
		)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func migrateSlackAgentTools(ctx context.Context, tx *sql.Tx, integrations map[string][]slackCutoverIntegration) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT agent.id::text, agent.current_config_id::text, config.compiled_definition::text,
		       target.id::text, integration.id::text, integration.name, target.provider_ref_kind, target.provider_ref
		FROM agents agent
		JOIN projects project ON project.id=agent.project_id AND project.deleted_at IS NULL
		JOIN orgs org ON org.id=agent.org_id AND org.deleted_at IS NULL
		JOIN agent_configs config ON config.project_id=agent.project_id AND config.id=agent.current_config_id
		JOIN integration_targets target ON target.project_id=agent.project_id AND target.agent_id=agent.id
		  AND target.deleted_at IS NULL
		JOIN project_integrations integration ON integration.project_id=target.project_id
		  AND integration.id=target.integration_id AND integration.integration_type='slack_thread'
		  AND integration.deleted_at IS NULL
		ORDER BY agent.id, target.id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type successor struct {
		agentID, configID string
		compiled          []byte
		targets           []slackCutoverTarget
	}
	var agents []successor
	for rows.Next() {
		var agentID, configID string
		var compiled []byte
		var target slackCutoverTarget
		if err := rows.Scan(
			&agentID,
			&configID,
			&compiled,
			&target.id,
			&target.integrationID,
			&target.integrationName,
			&target.kind,
			&target.ref,
		); err != nil {
			return err
		}
		if len(agents) == 0 || agents[len(agents)-1].agentID != agentID {
			agents = append(agents, successor{agentID: agentID, configID: configID, compiled: compiled})
		}
		agents[len(agents)-1].targets = append(agents[len(agents)-1].targets, target)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Rewrite history after capturing old policies but before inserting successors:
	// a successor can hash to a rewritten historical config and must reuse that row.
	if err := rewriteSlackIntegrationConfigs(ctx, tx, integrations); err != nil {
		return err
	}
	// Leave selection slots NULL so new mentions can launch through the integration after cutover.
	for _, agent := range agents {
		compiled, err := slackSendingSuccessor(agent.compiled, agent.targets)
		if err != nil {
			return fmt.Errorf("slack successor for agent %s: %w", agent.agentID, err)
		}
		hash, err := explicitDefaultToolsConfigHash(compiled)
		if err != nil {
			return err
		}
		var configID string
		err = tx.QueryRowContext(ctx, `WITH inserted AS (
			INSERT INTO agent_configs(org_id,project_id,configured_model_id,compiled_definition,
				effective_definition_hash,created_at)
			SELECT org_id,project_id,configured_model_id,$2::jsonb,$3,statement_timestamp()
			FROM agent_configs WHERE id=$1::uuid
			ON CONFLICT (project_id,effective_definition_hash,source_format,source_hash) DO NOTHING RETURNING id
		)
		SELECT id::text FROM inserted UNION ALL
		SELECT config.id::text FROM agent_configs config JOIN agent_configs original ON original.id=$1::uuid
		WHERE config.project_id=original.project_id AND config.effective_definition_hash=$3
		  AND config.source_format IS NULL AND config.source_hash IS NULL LIMIT 1`,
			agent.configID, compiled, hash).Scan(&configID)
		if err != nil {
			return fmt.Errorf("insert Slack successor for agent %s: %w", agent.agentID, err)
		}
		var withinLimit bool
		if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM agent_configs config WHERE config.project_id=agent.project_id)
            <= limits.max_agent_configs_per_project
			FROM agents agent JOIN effective_resource_limits limits ON limits.org_id=agent.org_id
            WHERE agent.id=$1::uuid`,
			agent.agentID,
		).Scan(&withinLimit); err != nil {
			return err
		}
		if !withinLimit {
			return fmt.Errorf(
				"slack successor for agent %s exceeds project config limit; raise the existing org override before retrying",
				agent.agentID,
			)
		}
		if err := activateSlackSuccessor(ctx, tx, agent.agentID, configID); err != nil {
			return err
		}
		for _, target := range agent.targets {
			if _, err := tx.ExecContext(
				ctx, `INSERT INTO integration_states(project_id,integration_id,kind,key,data)
				SELECT project_id,integration_id,'agent_conversation',agent_id::text,
                    jsonb_build_object('kind',provider_ref_kind,'ref',provider_ref)
				FROM integration_targets WHERE id=$1::uuid`, target.id,
			); err != nil {
				return fmt.Errorf("assign Slack conversation from target %s: %w", target.id, err)
			}
		}
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE agents SET integration_target_id=NULL, interaction_handler_key=NULL, interaction_handler_args=NULL WHERE integration_target_id IS NOT NULL`,
	)
	return err
}

func activateSlackSuccessor(ctx context.Context, tx *sql.Tx, agentID, configID string) error {
	var inputID, eventID, turnID string
	if err := tx.QueryRowContext(ctx, `INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,
		delivery_mode,agent_config_id,idempotency_scope,input_idempotency_key,queued_at,metadata)
		SELECT project_id,id,'received','config_change','immediate',$2::uuid,'agent_config_change',
		'slack_integration_cutover',statement_timestamp(),
        jsonb_build_object('agent_config_id',$2::text,'reason','slack_integration_cutover')
		FROM agents WHERE id=$1::uuid RETURNING id::text`, agentID, configID).Scan(&inputID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `WITH allocated AS (
		UPDATE agents SET next_event_sequence=next_event_sequence+1
        WHERE id=$1::uuid RETURNING next_event_sequence-1 AS sequence
	)
	INSERT INTO agent_events(
        agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,is_opening_event,created_at
    )
	SELECT $1::uuid,uuidv7(),sequence,'agent_input','agent_input:' || $2::text,$2::uuid,true,statement_timestamp()
	FROM allocated RETURNING id::text,turn_id::text`, agentID, inputID).Scan(&eventID, &turnID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
		SELECT $2::uuid,$1::uuid,coalesce(max(turn_sequence),0)+1,$3::uuid,$3::uuid FROM agent_turns WHERE agent_id=$1::uuid`,
		agentID,
		turnID,
		eventID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_inputs SET state='resolved',admitted_event_id=$2::uuid,
		admitted_at=event.created_at,resolved_at=event.created_at FROM agent_events event
		WHERE agent_inputs.id=$1::uuid AND event.id=$2::uuid AND event.agent_id=agent_inputs.agent_id`,
		inputID, eventID,
	); err != nil {
		return err
	}
	_, err := tx.ExecContext(
		ctx,
		`WITH activated AS (
            UPDATE agents SET current_config_id=$2::uuid,updated_at=statement_timestamp()
            WHERE id=$1::uuid RETURNING id,org_id,project_id
        )
        INSERT INTO event_webhook_deliveries(org_id,agent_id,event_sequence)
        SELECT agent.org_id,agent.id,event.sequence FROM activated agent
        JOIN agent_events event ON event.agent_id=agent.id AND event.id=$3::uuid
        JOIN agent_configs config ON config.id=$2::uuid AND config.project_id=agent.project_id
        WHERE coalesce(config.compiled_definition->'event_webhook'->>'url','') <> ''
          AND config.compiled_definition->'event_webhook'->'events' @> '["agent_input"]'::jsonb`,
		agentID,
		configID,
		eventID,
	)
	return err
}
