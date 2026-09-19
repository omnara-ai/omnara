//go:build integration

package dbmigrate_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestSlackAppCutoverPreservesScopedSendingAndHistory(t *testing.T) {
	for _, scenario := range []string{
		"normal", "injected_failure", "continuable_retry", "custom_collision",
		"live_lease", "started_context", "unfinished_tool", "open_interaction", "unsupported_setup",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 39))
			ids := storagefixture.ProjectIDs{
				OrgID:                   uuid.New(),
				ProjectID:               uuid.New(),
				ProviderAdminUserID:     uuid.New(),
				ProviderSecretID:        uuid.New(),
				ProviderSecretVersionID: uuid.New(),
				ProviderConfigID:        uuid.New(),
			}
			storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
			exec := func(query string, args ...any) {
				t.Helper()
				_, err := db.ExecContext(ctx, query, args...)
				require.NoError(t, err)
			}
			modelID, revisionID, profileID := uuid.New(), uuid.New(), uuid.New()
			versionID, configID, connectionID := uuid.New(), uuid.New(), uuid.New()
			exec(`WITH model AS (
			 INSERT INTO configured_models(id,org_id,model_provider_config_id,name,
             current_revision_id,management_kind,created_at,updated_at)
			 VALUES($1,$2,$3,'test',$4,'tenant',now(),now()))
			 INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,provider_model_slug,
			 context_window_tokens,max_output_tokens,created_at) VALUES($4,$2,$1,$3,'test',10000,1000,now())`,
				modelID, ids.OrgID, ids.ProviderConfigID, revisionID)
			compiled := fmt.Sprintf(
				`{"instruction":"Review","model":{"configured_model_id":%q},"tools":{"set_integration_target":{"enabled":true,"permission":{"mode":"always_allow","parameters":{}}},"send_integration_message":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}}`,
				modelID.String(),
			)
			var compiledObject map[string]any
			require.NoError(t, json.Unmarshal([]byte(compiled), &compiledObject))
			if scenario == "custom_collision" {
				compiledObject["tools"] = map[string]any{
					"slack_post_message": map[string]any{"type": "custom", "enabled": true},
				}
			}
			canonical, err := json.Marshal(compiledObject)
			require.NoError(t, err)
			compiled = string(canonical)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(compiled)))
			// Compiled-only snapshots are a released contract since migration39.
			exec(
				`INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,definition,compiled_definition,effective_definition_hash,created_at)
			 VALUES($1,$2,$3,$4,$5::jsonb,$5::jsonb,$6,now())`,
				configID,
				ids.OrgID,
				ids.ProjectID,
				modelID,
				compiled,
				hash,
			)
			exec(`WITH profile AS (
			 INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at)
             VALUES($1,$2,'shared',$3,now(),now()))
			 INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
             VALUES($3,$2,$1,1,$4,now())`, profileID, ids.ProjectID, versionID, configID)
			exec(
				`INSERT INTO integration_installs(id,org_id,project_id,agent_profile_id,installed_by_user_id,provider,integration_kind,
			 connection_mode,state,provider_tenant_id,provider_account_ref,provider_identity,created_at,updated_at)
			 VALUES($1,$2,$3,$4,$5,'slack','agent_profile','webhook','active','T123','A123',
             '{"bot_user_id":"U123"}',now(),now())`,
				connectionID,
				ids.OrgID,
				ids.ProjectID,
				profileID,
				ids.ProviderAdminUserID,
			)
			agents := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
			const archivedIndex, subagentIndex, noTurnIndex = 2, 3, 4
			for i, agentID := range agents {
				inputID, eventID, turnID, targetID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				q := func(query string, args ...any) {
					t.Helper()
					_, err := tx.ExecContext(ctx, query, args...)
					require.NoError(t, err)
				}
				var parentID *uuid.UUID
				var subagentKey string
				if i == subagentIndex {
					parentID = &agents[0]
					subagentKey = "child"
				}
				q(
					`INSERT INTO agents(id,org_id,project_id,state,name,agent_profile_id,current_config_id,next_event_sequence,parent_agent_id,subagent_key,created_at,updated_at)
                VALUES($1,$2,$3,'active','reviewer',$4,$5,CASE WHEN $6 THEN 1 ELSE 2 END,$7,$8,now(),now())`,
					agentID,
					ids.OrgID,
					ids.ProjectID,
					profileID,
					configID,
					i == noTurnIndex,
					parentID,
					subagentKey,
				)
				if i != noTurnIndex {
					q(
						`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,agent_config_id,queued_at)
				 VALUES($1,$2,$3,'received','config_change','immediate',$4,now())`,
						inputID,
						ids.ProjectID,
						agentID,
						configID,
					)
					q(
						`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,is_opening_event,created_at)
				 VALUES($1,$2,$3,1,'agent_input',$4,$5,true,now())`,
						eventID,
						agentID,
						turnID,
						"agent_input:"+inputID.String(),
						inputID,
					)
					q(
						`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id) VALUES($1,$2,1,$3,$3)`,
						turnID,
						agentID,
						eventID,
					)
					q(
						`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now() WHERE id=$1`,
						inputID,
						eventID,
					)
				}
				// Recoverable failures remain immutable history after retry or stop.
				// Neither may block a later maintenance migration.
				if i == 0 || i == 1 {
					failed := uuid.New()
					q(`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,
                      agent_config_id,configured_model_revision_id,input_event_sequence,
                      runtime_lock_id,state,created_at)
                      VALUES($1,$2,$3,$4,'normal',1,$5,$6,1,$7,'started',now())`,
						failed, ids.OrgID, ids.ProjectID, agentID, configID, revisionID, uuid.New())
					if i != 0 || scenario != "started_context" {
						q(`UPDATE model_call_contexts SET state='failed',recovery_kind='retry',error_kind='provider_error',
                          retry_at=now(),completed_at=now() WHERE id=$1`, failed)
					}
					if i == 0 && scenario != "continuable_retry" && scenario != "started_context" {
						seedSlackCutoverSuccessfulRetry(t, q, ids, agentID, configID, revisionID, turnID,
							scenario == "unfinished_tool" || scenario == "open_interaction")
					}
					if i == 1 {
						stopInput, stopEvent := uuid.New(), uuid.New()
						q(
							`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,control_type,delivery_mode,queued_at)
                        VALUES($1,$2,$3,'received','control','cancel_current','immediate',now())`,
							stopInput,
							ids.ProjectID,
							agentID,
						)
						q(
							`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,is_opening_event,created_at)
                        VALUES($1,$2,$3,2,'agent_input',$4,$5,false,now())`,
							stopEvent,
							agentID,
							turnID,
							"agent_input:"+stopInput.String(),
							stopInput,
						)
						q(
							`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now() WHERE id=$1`,
							stopInput,
							stopEvent,
						)
						q(`UPDATE agents SET next_event_sequence=3 WHERE id=$1`, agentID)
						q(
							`UPDATE agent_turns SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1`,
							turnID,
							stopEvent,
						)
					}
				}
				if i == archivedIndex {
					q(`UPDATE agents SET state='archived',archived_at=now() WHERE id=$1`, agentID)
				}
				q(
					`INSERT INTO integration_targets(id,project_id,agent_id,integration_install_id,target_ref,provider_ref,provider_ref_kind,created_at,updated_at)
				 VALUES($1,$2,$3,$4,'slack',$5,'thread',now(),now())`,
					targetID,
					ids.ProjectID,
					agentID,
					connectionID,
					fmt.Sprintf("C123:111.%d", i+1),
				)
				q(`UPDATE agents SET integration_target_id=$2 WHERE id=$1`, agentID, targetID)
				require.NoError(t, tx.Commit())
			}
			switch scenario {
			case "live_lease":
				exec(`INSERT INTO agent_runtime_locks(agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
                    VALUES($1,$2,now(),now(),now()+interval '1 hour')`, agents[0], uuid.New())
			case "open_interaction":
				exec(`INSERT INTO agent_interactions(agent_id,tool_call_id,interaction_kind,state,created_at)
                    SELECT agent_id,id,'question','open',now() FROM tool_calls WHERE agent_id=$1`, agents[0])
			case "unsupported_setup":
				exec(`UPDATE integration_installs SET connection_mode='custom' WHERE id=$1`, connectionID)
			}
			switch scenario {
			case "custom_collision", "live_lease", "started_context", "unfinished_tool", "open_interaction", "unsupported_setup":
				err := applyProductionPostgresMigrations(ctx, db)
				want := "requires maintenance"
				if scenario == "custom_collision" {
					want = "conflicts with a new app built-in"
				}
				if scenario == "unsupported_setup" {
					want = "unsupported legacy integration setup"
				}
				require.ErrorContains(t, err, want)
				require.Equal(t, int64(39), currentPostgresMigrationVersion(t, ctx, db))
				// The complete SQL40 transaction rolled back, leaving the old release usable.
				var oldConnections int
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM integration_installs WHERE id=$1`, connectionID).Scan(&oldConnections))
				require.Equal(t, 1, oldConnections)
				var newTableExists bool
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT to_regclass('project_apps') IS NOT NULL`).Scan(&newTableExists))
				require.False(t, newTableExists)
				return
			}
			if scenario == "injected_failure" {
				exec(`CREATE FUNCTION reject_cutover_config_event() RETURNS trigger LANGUAGE plpgsql AS $$
				 BEGIN
                 IF NEW.input_idempotency_key='slack_app_cutover' THEN
                     RAISE EXCEPTION 'injected cutover failure';
                 END IF;
                 RETURN NEW;
                 END $$;
				 CREATE TRIGGER reject_cutover BEFORE INSERT ON agent_inputs
                 FOR EACH ROW EXECUTE FUNCTION reject_cutover_config_event()`)
				err := applyProductionPostgresMigrations(ctx, db)
				require.ErrorContains(t, err, "injected cutover failure")
				require.Equal(t, int64(40), currentPostgresMigrationVersion(t, ctx, db))
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_configs`).Scan(&count))
				require.Equal(t, 1, count)
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_inputs`).Scan(&count))
				require.Equal(t, len(agents), count)
				exec(`DROP TRIGGER reject_cutover ON agent_inputs; DROP FUNCTION reject_cutover_config_event()`)
			}
			if scenario == "continuable_retry" {
				err := applyProductionPostgresMigrations(ctx, db)
				require.ErrorContains(t, err, "still has continuable work")
				require.Equal(t, int64(39), currentPostgresMigrationVersion(t, ctx, db))
				var turnID uuid.UUID
				require.NoError(
					t,
					db.QueryRowContext(ctx, `SELECT id FROM agent_turns WHERE agent_id=$1`, agents[0]).Scan(&turnID),
				)
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				seedSlackCutoverSuccessfulRetry(t, func(query string, args ...any) {
					t.Helper()
					_, err := tx.ExecContext(ctx, query, args...)
					require.NoError(t, err)
				}, ids, agents[0], configID, revisionID, turnID, false)
				require.NoError(t, tx.Commit())
			}
			require.NoError(t, applyProductionPostgresMigrations(ctx, db))
			execution := executionstore.New(pool, executionstore.Config{})
			for i, agentID := range agents {
				snapshot, err := execution.CaptureAgentConfigForModelContext(ctx, ids.ProjectID, agentID)
				require.NoError(t, err)
				require.NotEqual(t, configID, snapshot.AgentConfig.ID)
				expectedSequence := int64(2)
				if i == 0 || i == 1 {
					expectedSequence = 3
				}
				if i == noTurnIndex {
					expectedSequence = 1
				}
				require.Equal(t, expectedSequence, snapshot.InputEventSequence)
				contract, err := agentconfig.RuntimeContractFromCompiled(
					snapshot.AgentConfig.CompiledDefinition,
					"",
					snapshot.AgentConfig.EffectiveDefinitionHash,
				)
				require.NoError(t, err)
				require.Len(t, contract.AppResources, 1)
				for _, resource := range contract.AppResources {
					require.Equal(t, fmt.Sprintf("111.%d", i+1), resource.Scope.Slack.ThreadTS)
					require.Nil(t, resource.Listener)
					require.Nil(t, resource.InteractionHandler)
					require.Nil(t, resource.Follow)
				}
				var raw agentconfig.Compiled
				require.NoError(t, json.Unmarshal(snapshot.AgentConfig.CompiledDefinition, &raw))
				require.False(t, raw.Tools["slack_post_message"].Enabled)
				require.Contains(t, raw.Tools, "set_interaction_destination")
				require.NotContains(t, raw.Tools, "set_integration_target")
				require.NotContains(t, raw.Tools, "send_integration_message")
				if i != noTurnIndex {
					var oldID string
					require.NoError(
						t,
						db.QueryRowContext(ctx, `SELECT input.agent_config_id::text FROM agent_events event
				 JOIN agent_inputs input ON input.id=event.agent_input_id
                 WHERE event.agent_id=$1 AND event.sequence=1`, agentID).Scan(&oldID),
					)
					require.Equal(t, configID.String(), oldID)
					historical, err := execution.CaptureAgentConfigForEventWatermark(ctx, ids.ProjectID, agentID, 1)
					require.NoError(t, err)
					require.Equal(t, configID, historical.AgentConfig.ID)
				}
				var runnable bool
				require.NoError(
					t,
					db.QueryRowContext(
						ctx,
						`SELECT EXISTS(SELECT 1 FROM agent_next_model_work($1,$2))`,
						ids.ProjectID,
						agentID,
					).
						Scan(
							&runnable,
						),
				)
				require.False(t, runnable)
			}
			rows, err := db.QueryContext(ctx, `SELECT compiled_definition,effective_definition_hash FROM agent_configs`)
			require.NoError(t, err)
			defer rows.Close()
			for rows.Next() {
				var raw []byte
				var hash string
				require.NoError(t, rows.Scan(&raw, &hash))
				_, err := agentconfig.RuntimeContractFromCompiled(raw, "", hash)
				require.NoError(t, err)
			}
			require.NoError(t, rows.Err())

			var listeners, pointers int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_listeners`).Scan(&listeners))
			require.Zero(t, listeners)
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT count(*) FROM agents WHERE integration_target_id IS NOT NULL OR interaction_resource_key IS NOT NULL`,
				).
					Scan(
						&pointers,
					),
			)
			require.Zero(t, pointers)
			var setup []byte
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT settings FROM project_apps WHERE launch_connection_id=$1`,
					connectionID,
				).
					Scan(
						&setup,
					),
			)
			encoded, err := publicid.Encode(publicid.KindIntegrationConnection, connectionID)
			require.NoError(t, err)
			require.Contains(t, string(setup), encoded)
			require.Contains(t, string(setup), profileID.String())
			var profileConfig string
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT agent_config_id::text FROM agent_profile_versions WHERE id=$1`,
					versionID,
				).
					Scan(
						&profileConfig,
					),
			)
			require.Equal(t, configID.String(), profileConfig)
			require.NoError(t, applyProductionPostgresMigrations(ctx, db))
			// Ordinary public input remains usable after the cutover; it observes
			// the successor without recreating an old Slack receive subscription.
			for i, agentID := range agents {
				if i == archivedIndex {
					continue
				}
				_, _, created, err := execution.CreateAgentContentInput(
					ctx,
					executionstore.CreateAgentContentInputInput{
						ProjectID:        ids.ProjectID,
						AgentID:          agentID,
						ContentBlocks:    json.RawMessage(`[{"type":"text","text":"Continue reviewing"}]`),
						IdempotencyScope: "cutover-test",
						IdempotencyKey:   "resume",
					},
				)
				require.NoError(t, err)
				require.True(t, created)
				snapshot, err := execution.CaptureAgentConfigForModelContext(ctx, ids.ProjectID, agentID)
				require.NoError(t, err)
				require.NotEqual(t, configID, snapshot.AgentConfig.ID)
			}
		})
	}
}

// Exercise the ordinary immutable model context/output ledger, not an artificial
// edit of the prior failed context. These are legal pre-cutover records.
func seedSlackCutoverSuccessfulRetry(
	t *testing.T,
	exec func(string, ...any),
	ids storagefixture.ProjectIDs,
	agentID, configID, revisionID, turnID uuid.UUID,
	withTool bool,
) {
	t.Helper()
	succeeded, outputID, eventID := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,
    agent_config_id,configured_model_revision_id,input_event_sequence,
                      runtime_lock_id,state,created_at)
    VALUES($1,$2,$3,$4,'normal',2,$5,$6,1,$7,'started',now())`,
		succeeded, ids.OrgID, ids.ProjectID, agentID, configID, revisionID, uuid.New())
	exec(
		`INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at) VALUES($1,$2,$3,'end_turn',now())`,
		outputID,
		agentID,
		succeeded,
	)
	if withTool {
		toolID := uuid.New()
		exec(`INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
            VALUES($1,$2,$3,'cutover-call','ask_question','{}','built_in','awaiting_authorization',now())`,
			toolID, agentID, outputID)
		exec(`INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,created_at)
            VALUES($1,'model_output',$2,0,'tool_call',$3,now())`, agentID, outputID, toolID)
	}
	exec(
		`UPDATE model_call_contexts SET state='succeeded',api_format='openai',api_variant='responses',completed_at=now() WHERE id=$1`,
		succeeded,
	)
	exec(
		`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,model_output_id,is_opening_event,created_at)
   VALUES($1,$2,$3,2,'model_output',$4,$5,false,now())`,
		eventID,
		agentID,
		turnID,
		"model_output:"+outputID.String(),
		outputID,
	)
	exec(`UPDATE agents SET next_event_sequence=3 WHERE id=$1`, agentID)
	exec(`UPDATE agent_turns SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1`, turnID, eventID)
}
