//go:build integration

package dbmigrate_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSlackAppCutoverPreservesScopedSendingAndHistory(t *testing.T) {
	for _, scenario := range []string{
		"normal", "injected_failure", "rewrite_failure", "continuable_retry", "custom_collision",
		"live_lease", "started_context", "unfinished_tool", "open_interaction", "unsupported_setup",
		"fixed_agent", "mislabeled_fixed_agent", "multiple_targets", "multiple_apps", "enabled", "json", "yaml", "yaml_alias", "enabled_json", "enabled_yaml", "channel", "dm",
		"wire_json", "wire_yaml", "enabled_wire_json", "enabled_wire_yaml", "enabled_null_json", "enabled_null_yaml",
		"absent_send", "preflight_invalid_policy", "preflight_unmapped_policy", "preflight_address", "preflight_quota", "invalid_policy", "unmapped_policy", "invalid_address", "bad_hash", "bad_source_hash", "config_limit",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 40))
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
			versionID, configID := uuid.New(), uuid.New()
			appID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
			secondAppID := uuid.MustParse("00000000-0000-4000-8000-000000000002")
			credentialID, credentialVersionID := uuid.New(), uuid.New()
			exec(`WITH secret AS (
				INSERT INTO secrets(id,org_id,management_kind,owner_kind,name,kind,metadata,
                current_version_id,created_at,updated_at)
				VALUES($1,$2,'tenant','org','slack-cutover','slack_app_credentials','{}',$3,now(),now()))
				INSERT INTO secret_versions(id,org_id,secret_id,version_number,payload_keys,encryption_scheme,key_id,dek_wrapped_by,
				encrypted_dek,encrypted_dek_nonce,nonce,ciphertext,created_at)
				SELECT $3,org_id,$1,1,ARRAY['bot_token','signing_secret'],encryption_scheme,key_id,dek_wrapped_by,
				encrypted_dek,encrypted_dek_nonce,nonce,ciphertext,now() FROM secret_versions WHERE id=$4`,
				credentialID, ids.OrgID, credentialVersionID, ids.ProviderSecretVersionID)
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
			legacyTools := testutil.RequireType[map[string]any](t, compiledObject["tools"])
			legacySendPolicy := testutil.RequireType[map[string]any](t, legacyTools["send_integration_message"])
			sendingEnabled := strings.HasPrefix(scenario, "enabled") || scenario == "absent_send"
			if sendingEnabled {
				legacySendPolicy["enabled"] = true
			}
			if scenario == "absent_send" {
				delete(legacyTools, "send_integration_message")
			}
			if scenario == "custom_collision" {
				compiledObject["tools"] = map[string]any{
					"app__slack__post_message": map[string]any{"type": "custom", "enabled": true},
				}
			}
			if scenario == "preflight_invalid_policy" {
				legacySendPolicy["permission"] = map[string]any{"mode": "always_ask"}
			}
			canonical, err := json.Marshal(compiledObject)
			require.NoError(t, err)
			compiled = string(canonical)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(compiled)))
			// Compiled-only snapshots are a released contract since migration39.
			exec(
				`INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,definition,compiled_definition,
                 effective_definition_hash,created_at)
			 VALUES($1,$2,$3,$4,$5::jsonb,$5::jsonb,$6,now())`,
				configID,
				ids.OrgID,
				ids.ProjectID,
				modelID,
				compiled,
				hash,
			)
			var source, sourceFormat any
			switch scenario {
			case "json", "enabled_json", "bad_source_hash":
				source, sourceFormat = compiled, "json"
			case "yaml", "yaml_alias", "enabled_yaml":
				sourceFormat = "yaml"
				source = `# Keep this profile note.
instruction: Review
tools:
  # Explicitly disabled
  send_integration_message: {enabled: false, permission: {mode: always_allow, parameters: {}}} # policy
  set_integration_target: {enabled: true, permission: {mode: always_allow, parameters: {}}}
`
				if scenario == "yaml_alias" {
					source = `instruction: Review
tools:
  send_integration_message: &disabled {enabled: false, permission: {mode: always_allow, parameters: {}}}
  ask_question: *disabled
  set_integration_target: {enabled: true, permission: {mode: always_allow, parameters: {}}}
`
				}
			}
			if source != nil && sendingEnabled {
				source = strings.ReplaceAll(testutil.RequireType[string](t, source), "enabled: false", "enabled: true")
			}
			// Literal released builder/API tool entries have source-only type and
			// nullable enabled fields absent from their compiled representation.
			if strings.Contains(scenario, "wire_") || strings.Contains(scenario, "null_") {
				policy := `{"type":"built_in","enabled":false}`
				if sendingEnabled {
					policy = `{"type":"built_in"}`
				}
				if strings.Contains(scenario, "null_") {
					policy = `{"type":"built_in","enabled":null}`
				}
				sourceFormat = "json"
				source = `{"instruction":"Review","tools":{"send_integration_message":` + policy + `}}`
				if strings.HasSuffix(scenario, "yaml") {
					sourceFormat = "yaml"
					source = "instruction: Review\ntools:\n  send_integration_message: " + policy + "\n"
				}
			}
			if source != nil {
				exec(`ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`)
				exec(`UPDATE agent_configs SET source=$2,source_format=$3,source_hash=$4 WHERE id=$1`,
					configID, source, sourceFormat, fmt.Sprintf("%x", sha256.Sum256([]byte(testutil.RequireType[string](t, source)))))
				exec(`ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
			}

			exec(`WITH profile AS (
			 INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at)
             VALUES($1,$2,'shared',$3,now(),now()))
			 INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
             VALUES($3,$2,$1,1,$4,now())`, profileID, ids.ProjectID, versionID, configID)
			exec(
				`INSERT INTO integration_installs(id,org_id,project_id,agent_profile_id,installed_by_user_id,
             provider,integration_kind,
			 connection_mode,state,provider_tenant_id,provider_account_ref,provider_identity,
             credential_secret_id,created_at,updated_at)
			 VALUES($1,$2,$3,$4,$5,'slack','agent_profile','webhook','active','T123','A123',
             '{"bot_user_id":"U123"}',$6,'2026-01-01','2026-01-01')`,
				appID,
				ids.OrgID,
				ids.ProjectID,
				profileID,
				ids.ProviderAdminUserID,
				credentialID,
			)
			// Tie creation time to exercise the ID tiebreaker for stable names.
			exec(`INSERT INTO integration_installs(id,org_id,project_id,agent_profile_id,installed_by_user_id,
             provider,integration_kind,
			 connection_mode,state,provider_tenant_id,provider_account_ref,provider_identity,
             credential_secret_id,created_at,updated_at)
			 VALUES($1,$2,$3,$4,$5,'slack','agent_profile','webhook','disabled','T456','A456',
			 '{"bot_user_id":"U456"}',$6,'2026-01-01','2026-01-01')`,
				secondAppID, ids.OrgID, ids.ProjectID, profileID, ids.ProviderAdminUserID, credentialID)
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
				kind, address := "thread", fmt.Sprintf("C123:111.%d", i+1)
				if scenario == "channel" {
					kind, address = "channel", fmt.Sprintf("C%d", i+1)
				}
				if scenario == "dm" {
					kind, address = "dm", fmt.Sprintf("D%d", i+1)
				}
				if scenario == "preflight_address" {
					address = fmt.Sprintf("C%d:invalid", i+1)
				}
				q(
					`INSERT INTO integration_targets(id,project_id,agent_id,integration_install_id,target_ref,
                     provider_ref,provider_ref_kind,created_at,updated_at)
				 VALUES($1,$2,$3,$4,'slack',$5,$6,now(),now())`,
					targetID,
					ids.ProjectID,
					agentID,
					appID,
					address, kind,
				)
				q(`UPDATE agents SET integration_target_id=$2 WHERE id=$1`, agentID, targetID)
				if i == noTurnIndex {
					// Unadmitted provider history retains its original attribution ID.
					q(`INSERT INTO agent_inputs(project_id,agent_id,state,input_kind,delivery_mode,
                     integration_target_id,queued_at,canceled_at)
					 VALUES($1,$2,'canceled','content','steering',$3,now(),now())`, ids.ProjectID, agentID, targetID)
				}
				if scenario == "multiple_apps" || scenario == "multiple_targets" {
					otherApp := secondAppID
					if scenario == "multiple_targets" {
						otherApp = appID
					}
					q(`INSERT INTO integration_targets(id,project_id,agent_id,integration_install_id,target_ref,
                     provider_ref,provider_ref_kind,created_at,updated_at)
					 VALUES($1,$2,$3,$4,'other',$5,'dm',now(),now())`, uuid.New(), ids.ProjectID, agentID, otherApp, fmt.Sprintf(
						"D%d",
						i+1,
					))
				}
				require.NoError(t, tx.Commit())
			}
			retiredTargetID := uuid.New()
			exec(`INSERT INTO integration_targets(id,project_id,agent_id,integration_install_id,target_ref,
                provider_ref,provider_ref_kind,provider_metadata,deleted_at,created_at,updated_at)
                VALUES($1,$2,$3,$4,'retired','C999:1.2','thread','{"legacy":"retained"}',now(),now(),now())`,
				retiredTargetID, ids.ProjectID, agents[0], appID)
			// Snapshot durable history and agent data before the migration writes
			// config activations. Exact JSON comparison catches unintended edits.
			history := func() string {
				t.Helper()
				var value string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT jsonb_build_object(
				 'agents',(SELECT jsonb_agg(to_jsonb(a)-'current_config_id'-'next_event_sequence'
                 -'integration_target_id'-'updated_at'
                 -'interaction_handler_key'-'interaction_handler_args' ORDER BY a.id) FROM agents a),
				 'targets',(SELECT jsonb_agg((to_jsonb(t)-'integration_install_id'-'app_id'
                  -'selection_slot'-'is_tool_context'-'target_ref')
                  || jsonb_build_object('app_id',coalesce(to_jsonb(t)->'app_id',to_jsonb(t)->'integration_install_id'))
                  ORDER BY t.id) FROM integration_targets t),
				 'inputs',(SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM agent_inputs i
                  WHERE i.input_idempotency_key IS DISTINCT FROM 'slack_app_cutover'),
				 'contexts',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM model_call_contexts c),
				 'outputs',(SELECT jsonb_agg(to_jsonb(o) ORDER BY o.id) FROM model_outputs o),
				 'blocks',(SELECT jsonb_agg(to_jsonb(b) ORDER BY b.id) FROM content_blocks b),
				 'secrets',(SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM secrets s),
				 'versions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY v.id) FROM secret_versions v))::text`).Scan(&value))
				return value
			}
			switch scenario {
			case "bad_hash", "bad_source_hash":
				exec(`ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`)
				if scenario == "bad_hash" {
					exec(`UPDATE agent_configs SET effective_definition_hash=repeat('0',64) WHERE id=$1`, configID)
				} else {
					exec(`UPDATE agent_configs SET source_hash=repeat('0',64) WHERE id=$1`, configID)
				}
				exec(`ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
			case "preflight_quota":
				exec(`INSERT INTO org_resource_limit_overrides(org_id,max_agent_configs_per_project) VALUES($1,1)`, ids.OrgID)
			case "live_lease":
				exec(`INSERT INTO agent_runtime_locks(agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
                    VALUES($1,$2,now(),now(),now()+interval '1 hour')`, agents[0], uuid.New())
			case "open_interaction":
				exec(`INSERT INTO agent_interactions(agent_id,tool_call_id,interaction_kind,state,created_at)
                    SELECT agent_id,id,'question','open',now() FROM tool_calls WHERE agent_id=$1`, agents[0])
			case "mislabeled_fixed_agent":
				exec(`UPDATE integration_installs SET agent_profile_id=NULL,agent_id=$2 WHERE id=$1`, appID, agents[0])
			case "fixed_agent":
				exec(
					`UPDATE integration_installs SET agent_profile_id=NULL,agent_id=$2,integration_kind='agent' WHERE id=$1`,
					appID,
					agents[0],
				)
			case "unsupported_setup":
				exec(`UPDATE integration_installs SET connection_mode='custom' WHERE id=$1`, appID)
			}
			if scenario == "preflight_unmapped_policy" {
				projectID := uuid.New()
				storagefixture.InsertProject(t, ctx, pool, ids.OrgID, projectID, "No Slack app", "no-slack-app", time.Now())
				exec(`INSERT INTO agent_configs(org_id,project_id,configured_model_id,definition,compiled_definition,
                 effective_definition_hash,created_at)
				 VALUES($1,$2,$3,$4::jsonb,$4::jsonb,$5,now())`, ids.OrgID, projectID, modelID, compiled, hash)
			}
			switch scenario {
			case "preflight_invalid_policy", "preflight_unmapped_policy", "preflight_address", "preflight_quota",
				"custom_collision", "live_lease", "started_context", "unfinished_tool", "open_interaction",
				"unsupported_setup", "fixed_agent", "mislabeled_fixed_agent", "multiple_targets":
				err := applyProductionPostgresMigrations(ctx, db)
				want := "requires maintenance"
				if scenario == "custom_collision" {
					want = "conflicts with a new app built-in"
				}
				if scenario == "unsupported_setup" || scenario == "fixed_agent" || scenario == "mislabeled_fixed_agent" {
					want = "not a profile-bound Slack webhook setup"
				}
				if scenario == "multiple_targets" {
					want = "has several live targets through one Slack app"
				}
				if strings.HasPrefix(scenario, "preflight_") {
					want = "has an unmappable legacy send policy"
				}
				if scenario == "preflight_address" {
					want = "invalid Slack target"
				}
				if scenario == "preflight_quota" {
					want = "needs additional config quota"
				}
				require.ErrorContains(t, err, want)
				require.Equal(t, int64(40), currentPostgresMigrationVersion(t, ctx, db))
				// The complete SQL41 transaction rolled back, leaving the old release usable.
				var oldConnections int
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM integration_installs WHERE id=$1`, appID).Scan(&oldConnections))
				require.Equal(t, 1, oldConnections)
				var newTableExists bool
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT to_regclass('project_apps') IS NOT NULL`).Scan(&newTableExists))
				require.False(t, newTableExists)
				// Exercise an old-writer statement after rollback, not just a table lookup.
				exec(
					`UPDATE integration_installs SET updated_at=now() WHERE id=$1 AND agent_profile_id IS NOT DISTINCT FROM agent_profile_id`,
					appID,
				)
				var untouched string
				require.NoError(
					t,
					db.QueryRowContext(
						ctx,
						`SELECT effective_definition_hash FROM agent_configs WHERE id=$1`,
						configID,
					).Scan(
						&untouched,
					),
				)
				require.Equal(t, hash, untouched)
				if scenario == "multiple_targets" {
					var count int
					require.NoError(
						t,
						db.QueryRowContext(
							ctx,
							`SELECT count(*) FROM integration_targets WHERE integration_install_id=$1`,
							appID,
						).Scan(
							&count,
						),
					)
					require.Equal(t, 2*len(agents)+1, count)
					identified := false
					for _, agentID := range agents {
						if !strings.Contains(err.Error(), agentID.String()) {
							continue
						}
						identified = true
						var rawTargets []byte
						require.NoError(
							t,
							db.QueryRowContext(
								ctx,
								`SELECT jsonb_agg(id::text ORDER BY id) FROM integration_targets WHERE agent_id=$1 AND integration_install_id=$2 AND deleted_at IS NULL`,
								agentID,
								appID,
							).Scan(
								&rawTargets,
							),
						)
						var targets []string
						require.NoError(t, json.Unmarshal(rawTargets, &targets))
						for _, target := range targets {
							require.Contains(t, err.Error(), target)
						}
					}
					require.True(t, identified, "guard must identify the agent and every conflicting target")
				}
				return
			}
			assertContextRollback := func() {
				t.Helper()
				var contexts int
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM integration_targets WHERE is_tool_context`).Scan(&contexts))
				require.Zero(t, contexts)
				var enabled string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT tgenabled::text FROM pg_trigger WHERE tgname='integration_targets_tool_context_immutable'`).Scan(&enabled))
				require.Equal(t, "O", enabled, "migration failure restores the write-once guard")
			}
			if scenario == "invalid_policy" || scenario == "unmapped_policy" || scenario == "invalid_address" ||
				scenario == "bad_hash" || scenario == "bad_source_hash" || scenario == "config_limit" {
				// Independently exercise Go42's defensive checks and transaction
				// rollback even if maintenance preflight was bypassed after SQL41.
				require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 41))
				if scenario == "config_limit" {
					exec(`INSERT INTO org_resource_limit_overrides(org_id,max_agent_configs_per_project) VALUES($1,1)`, ids.OrgID)
				}
				if scenario == "invalid_address" {
					exec(`UPDATE integration_targets SET provider_ref='C1:invalid' WHERE agent_id=$1`, agents[0])
				}
				expectedConfigs := 1
				if scenario == "invalid_policy" {
					// Inject after SQL41 so this remains a Go42 defense test when
					// SQL41 also rejects unmappable policies before its rename.
					legacySendPolicy["permission"] = map[string]any{"mode": "always_ask", "parameters": map[string]any{}}
					invalid, err := json.Marshal(compiledObject)
					require.NoError(t, err)
					exec(`ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`)
					exec(
						`UPDATE agent_configs SET definition=$2::jsonb,compiled_definition=$2::jsonb,effective_definition_hash=$3 WHERE id=$1`,
						configID,
						invalid,
						fmt.Sprintf(
							"%x",
							sha256.Sum256(
								invalid,
							),
						),
					)
					exec(`ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
				}
				if scenario == "unmapped_policy" {
					projectID := uuid.New()
					storagefixture.InsertProject(t, ctx, pool, ids.OrgID, projectID, "No Slack app", "no-slack-app", time.Now())
					exec(`INSERT INTO agent_configs(org_id,project_id,configured_model_id,definition,compiled_definition,
                 effective_definition_hash,created_at)
					 VALUES($1,$2,$3,$4::jsonb,$4::jsonb,$5,now())`, ids.OrgID, projectID, modelID, compiled, hash)
					expectedConfigs++
				}

				before := history()
				err := applyProductionPostgresMigrations(ctx, db)
				want := "stored config hashes do not match content"
				if scenario == "invalid_policy" {
					want = "legacy send only supports always_allow"
				}
				if scenario == "unmapped_policy" {
					want = "disabled legacy send policy has no known Slack app"
				}
				if scenario == "invalid_address" {
					want = "invalid Slack target"
				}
				if scenario == "config_limit" {
					want = "exceeds project config limit"
				}
				require.ErrorContains(t, err, want)
				require.Equal(t, int64(41), currentPostgresMigrationVersion(t, ctx, db))
				assertContextRollback()
				require.JSONEq(t, before, history())
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_configs`).Scan(&count))
				require.Equal(t, expectedConfigs, count)
				require.NoError(
					t,
					db.QueryRowContext(
						ctx,
						`SELECT count(*) FROM agents WHERE current_config_id=$1 AND integration_target_id IS NULL`,
						configID,
					).Scan(
						&count,
					),
				)
				require.Equal(t, len(agents), count)
				return
			}
			if scenario == "rewrite_failure" {
				exec(`CREATE FUNCTION reject_cutover_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
				 BEGIN RAISE EXCEPTION 'injected rewrite failure'; END $$;
				 CREATE TRIGGER reject_cutover_rewrite BEFORE UPDATE ON agent_configs
				 FOR EACH ROW EXECUTE FUNCTION reject_cutover_rewrite()`)
				before := history()
				err := applyProductionPostgresMigrations(ctx, db)
				require.ErrorContains(t, err, "injected rewrite failure")
				require.Equal(t, int64(41), currentPostgresMigrationVersion(t, ctx, db))
				assertContextRollback()
				require.JSONEq(t, before, history())
				var active, configs int
				require.NoError(
					t,
					db.QueryRowContext(
						ctx,
						`SELECT count(*) FROM agents WHERE current_config_id=$1 AND integration_target_id IS NULL`,
						configID,
					).Scan(
						&active,
					),
				)
				require.Equal(t, len(agents), active)
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_configs`).Scan(&configs))
				require.Equal(t, 1, configs)
				var triggerEnabled string
				require.NoError(
					t,
					db.QueryRowContext(
						ctx,
						`SELECT tgenabled::text FROM pg_trigger WHERE tgname='agent_configs_immutable'`,
					).Scan(
						&triggerEnabled,
					),
				)
				require.Equal(t, "O", triggerEnabled)
				exec(`DROP TRIGGER reject_cutover_rewrite ON agent_configs; DROP FUNCTION reject_cutover_rewrite()`)
			}

			if scenario == "injected_failure" {
				exec(`CREATE FUNCTION reject_cutover_config_event() RETURNS trigger LANGUAGE plpgsql AS $$
				 BEGIN
                 IF NEW.input_idempotency_key='slack_app_cutover'
                    AND EXISTS (SELECT 1 FROM integration_targets WHERE is_tool_context) THEN
                     RAISE EXCEPTION 'injected cutover failure';
                 END IF;
                 RETURN NEW;
                 END $$;
				 CREATE TRIGGER reject_cutover BEFORE INSERT ON agent_inputs
                 FOR EACH ROW EXECUTE FUNCTION reject_cutover_config_event()`)
				err := applyProductionPostgresMigrations(ctx, db)
				require.ErrorContains(t, err, "injected cutover failure")
				require.Equal(t, int64(41), currentPostgresMigrationVersion(t, ctx, db))
				assertContextRollback()
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_configs`).Scan(&count))
				require.Equal(t, 1, count)
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_inputs`).Scan(&count))
				require.Equal(t, len(agents)+1, count)
				exec(`DROP TRIGGER reject_cutover ON agent_inputs; DROP FUNCTION reject_cutover_config_event()`)
			}
			if scenario == "continuable_retry" {
				err := applyProductionPostgresMigrations(ctx, db)
				require.ErrorContains(t, err, "still has continuable work")
				require.Equal(t, int64(40), currentPostgresMigrationVersion(t, ctx, db))
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
			before := history()
			require.NoError(t, applyProductionPostgresMigrations(ctx, db))
			require.JSONEq(t, before, history())
			var retiredContext bool
			var retiredSlot sql.NullString
			var retiredMetadata []byte
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT is_tool_context,selection_slot,provider_metadata FROM integration_targets WHERE id=$1`, retiredTargetID).
				Scan(&retiredContext, &retiredSlot, &retiredMetadata))
			require.False(t, retiredContext, "a target excluded from the old fixed config must not become context")
			require.False(t, retiredSlot.Valid)
			require.JSONEq(t, `{"legacy":"retained"}`, string(retiredMetadata))
			execution := executionstore.New(pool, executionstore.Config{})
			for i, agentID := range agents {
				snapshot, err := execution.CaptureAgentConfigForModelContext(ctx, ids.ProjectID, agentID)
				require.NoError(t, err)
				if scenario == "multiple_apps" {
					require.Equal(t, configID, snapshot.AgentConfig.ID,
						"reuse identical rewritten config without a hidden destination")
				} else {
					require.NotEqual(t, configID, snapshot.AgentConfig.ID)
				}
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
				require.Empty(t, contract.InteractionHandlers)
				var raw agentconfig.Compiled
				require.NoError(t, json.Unmarshal(snapshot.AgentConfig.CompiledDefinition, &raw))
				encodedAppID, err := publicid.Encode(publicid.KindProjectApp, appID)
				require.NoError(t, err)
				tool := raw.Tools["app__slack__post_message"]
				require.Equal(t, sendingEnabled, tool.Enabled)
				require.Equal(t, "always_allow", tool.Permission.Mode)
				require.Equal(t, encodedAppID, tool.AppID)
				contexts := integrationstore.New(pool, executionstore.AppAccess{})
				context, found, err := contexts.GetAgentAppToolContext(ctx, ids.ProjectID, agentID, appID)
				require.NoError(t, err)
				require.True(t, found)
				require.True(t, context.IsToolContext)
				require.Empty(t, context.SelectionSlot, "legacy contexts must not suppress new mention launches")
				if scenario != "channel" && scenario != "dm" {
					require.Equal(t, "thread", context.ProviderRefKind)
					require.Equal(t, fmt.Sprintf("C123:111.%d", i+1), context.ProviderRef)
				}
				if scenario == "channel" {
					require.Equal(t, "channel", context.ProviderRefKind)
					require.Equal(t, fmt.Sprintf("C%d", i+1), context.ProviderRef)
				}
				if scenario == "dm" {
					require.Equal(t, "dm", context.ProviderRefKind)
					require.Equal(t, fmt.Sprintf("D%d", i+1), context.ProviderRef)
				}
				wantApps := 1
				if scenario == "multiple_apps" {
					wantApps = 2
					second, found, err := contexts.GetAgentAppToolContext(ctx, ids.ProjectID, agentID, secondAppID)
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, "dm", second.ProviderRefKind)
					require.Equal(t, fmt.Sprintf("D%d", i+1), second.ProviderRef)
					require.Empty(t, second.SelectionSlot)
				}
				require.Len(t, contract.AppTools, wantApps)
				require.Contains(t, raw.Tools, "set_interaction_handler")
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
			rows, err := db.QueryContext(
				ctx,
				`SELECT definition,compiled_definition,effective_definition_hash FROM agent_configs`,
			)
			require.NoError(t, err)
			defer rows.Close()
			for rows.Next() {
				var definition, raw []byte
				var hash string
				require.NoError(t, rows.Scan(&definition, &raw, &hash))
				require.JSONEq(t, string(raw), string(definition))
				_, err := agentconfig.RuntimeContractFromCompiled(raw, "", hash)
				require.NoError(t, err)
			}
			require.NoError(t, rows.Err())

			var subscriptions, pointers int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM app_subscriptions`).Scan(&subscriptions))
			require.Zero(t, subscriptions, "cutover preserves sending and history without creating receive routes")
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT count(*) FROM agents WHERE integration_target_id IS NOT NULL OR interaction_handler_key IS NOT NULL OR interaction_handler_args IS NOT NULL`,
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
					`SELECT settings FROM project_apps WHERE id=$1`,
					appID,
				).
					Scan(
						&setup,
					),
			)
			require.Contains(t, string(setup), profileID.String())
			var name, appType, secondName, secondType, secondState string
			var preservedCredential uuid.UUID
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT name,app_type,credential_secret_id FROM project_apps WHERE id=$1`,
					appID,
				).Scan(
					&name,
					&appType,
					&preservedCredential,
				),
			)
			require.Equal(t, "slack", name)
			require.Equal(t, "slack_thread", appType)
			require.Equal(t, credentialID, preservedCredential)
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT name,app_type,state FROM project_apps WHERE id=$1`,
					secondAppID,
				).Scan(
					&secondName,
					&secondType,
					&secondState,
				),
			)
			require.Equal(t, "slack-2", secondName)
			require.Equal(t, "slack_thread", secondType)
			require.Equal(t, "disconnected", secondState)
			var oldCompiled []byte
			var migratedSource, migratedFormat, migratedSourceHash sql.NullString
			require.NoError(t, db.QueryRowContext(
				ctx,
				`SELECT compiled_definition,source,source_format,source_hash FROM agent_configs WHERE id=$1`,
				configID,
			).
				Scan(&oldCompiled, &migratedSource, &migratedFormat, &migratedSourceHash))
			var historical agentconfig.Compiled
			require.NoError(t, json.Unmarshal(oldCompiled, &historical))
			for _, name := range []string{"app__slack__post_message", "app__slack-2__post_message"} {
				tool, exists := historical.Tools[name]
				if sendingEnabled {
					require.False(t, exists)
				} else {
					require.True(t, exists)
					require.False(t, tool.Enabled)
				}
			}
			// The shared profile is still usable for a new mention. An enabled
			// legacy entry must not mask the launcher's ordinary app tool;
			// an explicit disable still wins unchanged.
			encodedAppID, err := publicid.Encode(publicid.KindProjectApp, appID)
			require.NoError(t, err)
			launched, err := agentconfig.DeriveWithAppCapabilities(historical, agentconfig.AppCapabilitiesSource{
				Tools: map[string]agentconfig.AgentConfigToolSource{
					"app__slack__post_message": {},
				},
			}, agentconfig.CompileOptions{ResolveAppName: func(name string) (agentconfig.AppResolution, error) {
				require.Equal(t, "slack", name)
				return agentconfig.AppResolution{AppID: encodedAppID, AppType: appdefinition.SlackThread}, nil
			}})
			require.NoError(t, err)
			if sendingEnabled {
				require.True(t, launched.Tools["app__slack__post_message"].Enabled)
				require.Equal(t, encodedAppID, launched.Tools["app__slack__post_message"].AppID)
			} else {
				require.Equal(t, historical.Tools["app__slack__post_message"], launched.Tools["app__slack__post_message"])
			}

			require.Equal(t, source != nil, migratedSource.Valid)
			if source != nil {
				require.Equal(t, sourceFormat, migratedFormat.String)
				require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(migratedSource.String))), migratedSourceHash.String)
				var object map[string]any
				if sourceFormat == "json" {
					require.NoError(t, json.Unmarshal([]byte(migratedSource.String), &object))
				} else {
					require.NoError(t, yaml.Unmarshal([]byte(migratedSource.String), &object))
				}
				require.NotContains(t, object["tools"], "send_integration_message")
				if sendingEnabled {
					require.NotContains(t, object["tools"], "app__slack__post_message")
				} else {
					require.Contains(t, object["tools"], "app__slack__post_message")
				}
				require.NotContains(t, migratedSource.String, "app_id")
				if scenario == "yaml" {
					require.Contains(t, migratedSource.String, "# Keep this profile note.")
					require.Contains(t, migratedSource.String, "# Explicitly disabled")
					require.Contains(t, migratedSource.String, "# policy")
				}
			}
			var inputTargets int
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT count(*) FROM agent_inputs input JOIN integration_targets target ON target.id=input.integration_target_id WHERE target.app_id=$1`,
					appID,
				).Scan(
					&inputTargets,
				),
			)
			require.Equal(t, 1, inputTargets)

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
				if scenario == "multiple_apps" {
					require.Equal(t, configID, snapshot.AgentConfig.ID,
						"reuse identical rewritten config without a hidden destination")
				} else {
					require.NotEqual(t, configID, snapshot.AgentConfig.ID)
				}
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
	exec(`INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,text_content,created_at)
	 VALUES($1,'model_output',$2,1,'text','Historical Slack reply',now())`, agentID, outputID)
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
