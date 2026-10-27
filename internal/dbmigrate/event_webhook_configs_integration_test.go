//go:build integration

package dbmigrate_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookConfigMigration(t *testing.T) {
	preflight, err := os.ReadFile("../../migrations/preflight/000059_migrate_event_webhook_configs.sql")
	require.NoError(t, err)
	for _, scenario := range []string{
		"existing_clean", "merge_legacy", "distinct_config", "other_project_clean", "generated", "yaml",
		"profile", "deleted_profile",
		"archived_agent", "unroutable_delivery", "already_scoped_delivery", "profile_conflict", "bad_hash", "rollback",
		"profile_missing_webhook", "archived_profile_without_deliveries", "archived_profile_with_deliveries",
		"oversized_agent_url", "oversized_profile_url", "oversized_archived_url", "unused_oversized_url",
		"oversized_archived_without_deliveries",
		"self_agent", "self_profile", "self_archived", "self_disabled", "self_profile_without_webhook",
		"pending_self", "pending_completed", "pending_disabled", "pending_unknown_subagent",
		"pending_profile_covered", "pending_profile_conflict",
	} {
		t.Run(scenario, func(t *testing.T) {
			secondAgentProfileConflict := scenario == "profile_missing_webhook" ||
				scenario == "archived_profile_without_deliveries" || scenario == "archived_profile_with_deliveries"
			pendingSpawn := strings.HasPrefix(scenario, "pending_")
			profileWebhook := scenario == "profile" || scenario == "self_profile" || scenario == "oversized_profile_url" ||
				scenario == "pending_profile_covered" || scenario == "pending_profile_conflict"
			ctx := t.Context()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 57))
			ids := storagefixture.ProjectIDs{
				OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
				ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
			}
			storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
			exec := func(statement string, args ...any) {
				t.Helper()
				_, err := db.ExecContext(ctx, statement, args...)
				require.NoError(t, err)
			}
			modelID, revisionID := uuid.New(), uuid.New()
			exec(`WITH model AS (
				INSERT INTO configured_models(id,org_id,model_provider_config_id,name,current_revision_id,
					management_kind,created_at,updated_at) VALUES($1,$2,$3,'test',$4,'tenant',now(),now()))
				INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,
					provider_model_slug,context_window_tokens,max_output_tokens,created_at)
				VALUES($4,$2,$1,$3,'test',10000,1000,now())`, modelID, ids.OrgID, ids.ProviderConfigID, revisionID)
			cleanID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
			firstID := uuid.MustParse("00000000-0000-4000-8000-000000000002")
			secondID := uuid.MustParse("00000000-0000-4000-8000-000000000003")
			otherProject := uuid.New()
			if scenario == "other_project_clean" {
				storagefixture.InsertProject(t, ctx, pool, ids.OrgID, otherProject, "Other", "other", time.Now())
			}
			insertConfig := func(id uuid.UUID, endpoint string) {
				t.Helper()
				compiled := map[string]any{"instruction": "Review <tag>&", "model": map[string]any{"configured_model_id": modelID}}
				source := map[string]any{
					"instruction": "Review <tag>&", "model": map[string]any{"provider_config": "openai-prod", "name": "test"},
				}
				if scenario == "distinct_config" && id == firstID {
					compiled["instruction"], source["instruction"] = "Different", "Different"
				}
				if strings.HasPrefix(scenario, "self_") || pendingSpawn && id == firstID {
					tools := map[string]any{"spawn_agent": map[string]any{
						"enabled":    scenario != "self_disabled" && scenario != "pending_disabled",
						"permission": map[string]any{"mode": "always_allow"},
					}}
					subagents := map[string]any{"worker": map[string]any{"type": "self"}}
					compiled["tools"], source["tools"] = tools, tools
					compiled["subagents"], source["subagents"] = subagents, subagents
				}
				if endpoint != "" {
					hook := map[string]any{"url": endpoint, "events": []string{"tool_call_update", "model_output"}}
					if endpoint == "https://example.com/second" {
						hook["signing_secret_id"] = ids.ProviderSecretID
					}
					if scenario == "profile" && id == firstID {
						hook["events"] = []string{"model_output", "tool_call_update"}
					}
					compiled["event_webhook"], source["event_webhook"] = hook, hook
				}
				encoded, err := json.Marshal(compiled)
				require.NoError(t, err)
				var sourceText, format, sourceHash any
				if scenario != "generated" {
					var buffer bytes.Buffer
					encoder := json.NewEncoder(&buffer)
					encoder.SetEscapeHTML(false)
					require.NoError(t, encoder.Encode(source))
					encodedSource := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
					format = "json"
					if scenario == "yaml" {
						encodedSource = []byte("# keep note\ninstruction: Review <tag>&\n" +
							"model: {provider_config: openai-prod, name: test}\n")
						if endpoint != "" {
							encodedSource = append(encodedSource, []byte(fmt.Sprintf(
								"event_webhook: {url: %s, events: [model_output, tool_call_update]}\n", endpoint,
							))...)
						}
						format = "yaml"
					}
					sourceText, sourceHash = string(encodedSource), fmt.Sprintf("%x", sha256.Sum256(encodedSource))
				}
				hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
				if scenario == "bad_hash" && id == firstID {
					hash = "bad"
				}
				projectID := ids.ProjectID
				if scenario == "other_project_clean" && id == cleanID {
					projectID = otherProject
				}
				exec(`INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,source,source_format,
					source_hash,compiled_definition,effective_definition_hash,created_at)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,now())`,
					id, ids.OrgID, projectID, modelID, sourceText, format, sourceHash, encoded, hash)
			}
			winnerID := cleanID
			if scenario == "merge_legacy" {
				winnerID = firstID
			} else {
				insertConfig(cleanID, "")
			}
			if scenario == "distinct_config" || scenario == "other_project_clean" || pendingSpawn {
				winnerID = firstID
			}
			firstEndpoint := "https://example.com/first"
			if strings.HasPrefix(scenario, "oversized_") {
				firstEndpoint = "https://example.com/" + strings.Repeat("a", 3000)
			}
			if scenario == "pending_profile_covered" || scenario == "profile" {
				firstEndpoint = "https://example.com/second"
			}
			insertConfig(firstID, firstEndpoint)
			insertConfig(secondID, "https://example.com/second")
			if scenario == "unused_oversized_url" {
				insertConfig(uuid.New(), "https://example.com/"+strings.Repeat("a", 3000))
			}
			profileID, versionID := uuid.New(), uuid.New()
			profileConfigID := firstID
			if scenario == "self_profile_without_webhook" {
				profileConfigID = cleanID
			}
			if pendingSpawn && profileWebhook {
				profileConfigID = secondID
			}
			exec(`WITH profile AS (
				INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at)
				VALUES($1,$2,'profile',$3,now(),now()))
				INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
				VALUES($3,$2,$1,1,$4,now())`, profileID, ids.ProjectID, versionID, profileConfigID)
			if !profileWebhook && scenario != "profile_conflict" && !secondAgentProfileConflict &&
				scenario != "self_profile_without_webhook" {
				exec(`UPDATE agent_profiles SET deleted_at=now() WHERE id=$1`, profileID)
			}
			firstAgent, secondAgent := uuid.New(), uuid.New()
			for index, agentID := range []uuid.UUID{firstAgent, secondAgent} {
				configID := firstID
				var profile any
				if index == 0 && (profileWebhook || scenario == "profile_conflict" || scenario == "deleted_profile" ||
					scenario == "self_profile_without_webhook") {
					profile = profileID
				}
				if index == 1 && (secondAgentProfileConflict || scenario == "profile_conflict") {
					profile = profileID
				}
				if index == 1 || scenario == "profile_conflict" {
					configID = secondID
				}
				if index == 1 && (scenario == "unroutable_delivery" || scenario == "already_scoped_delivery" ||
					scenario == "profile_missing_webhook") {
					configID = cleanID
				}
				exec(`INSERT INTO agents(id,root_agent_id,org_id,project_id,state,current_config_id,
					agent_profile_id,created_at,updated_at)
					VALUES($1,$1,$2,$3,'active',$4,$5,now(),now())`, agentID, ids.OrgID, ids.ProjectID, configID, profile)
			}
			if scenario == "archived_agent" || scenario == "archived_profile_without_deliveries" ||
				scenario == "archived_profile_with_deliveries" || strings.HasPrefix(scenario, "self_") {
				exec(`UPDATE agents SET state='archived',archived_at=now() WHERE id=$1`, secondAgent)
			}
			if scenario == "self_archived" || strings.HasPrefix(scenario, "oversized_archived_") {
				exec(`UPDATE agents SET state='archived',archived_at=now() WHERE id=$1`, firstAgent)
			}
			inputID, eventID, turnID, contextID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			contentID, contentEventID := uuid.New(), uuid.New()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			write := func(statement string, args ...any) {
				t.Helper()
				_, err := tx.ExecContext(ctx, statement, args...)
				require.NoError(t, err)
			}
			write(`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
				VALUES($1,$2,$3,'received','content','queued',now())`, contentID, ids.ProjectID, firstAgent)
			write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
				agent_input_id,is_opening_event,created_at)
				VALUES($1,$2,$3,1,'agent_input','agent_input:'||$4::text,$4::uuid,true,now())`,
				contentEventID, firstAgent, turnID, contentID)
			write(`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now()
				WHERE id=$1`, contentID, contentEventID)
			write(`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,
				agent_config_id,queued_at,metadata)
				VALUES($1,$2,$3,'received','config_change','immediate',$4::uuid,now(),
					jsonb_build_object('agent_config_id',$4::text,'reason','keep'))`,
				inputID, ids.ProjectID, firstAgent, firstID)
			write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
				agent_input_id,is_opening_event,created_at)
				VALUES($1,$2,$3,2,'agent_input','agent_input:'||$4::text,$4::uuid,true,now())`,
				eventID, firstAgent, turnID, inputID)
			write(`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
				VALUES($1,$2,1,$3,$3)`, turnID, firstAgent, eventID)
			write(`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now()
				,opening_input_ids=ARRAY[$3::uuid],opening_event_sequence=1 WHERE id=$1`, inputID, eventID, contentID)
			require.NoError(t, tx.Commit())
			exec(`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,
				agent_config_id,configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at,
				turn_id,opening_input_ids,opening_event_sequence)
				VALUES($1,$2,$3,$4,'normal',1,$5,$6,2,$7,'started',now(),$8,ARRAY[$9::uuid],1)`,
				contextID, ids.OrgID, ids.ProjectID, firstAgent, firstID, revisionID, uuid.New(), turnID, contentID)
			if pendingSpawn {
				tx, err = db.BeginTx(ctx, nil)
				require.NoError(t, err)
				outputID, callID := uuid.New(), uuid.New()
				write(`INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
					VALUES($1,$2,$3,'tool_use',now())`, outputID, firstAgent, contextID)
				write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
					model_output_id,is_opening_event,created_at)
					VALUES($1,$2,$3,3,'model_output','model_output:'||$4::text,$4::uuid,false,now())`,
					uuid.New(), firstAgent, turnID, outputID)
				key := "worker"
				if scenario == "pending_unknown_subagent" {
					key = "missing"
				}
				write(`INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
					VALUES($1,$2,$3,'spawn','spawn_agent',jsonb_build_object('agent',$4::text,'task','Review'),
					'built_in','awaiting_authorization',now())`, callID, firstAgent, outputID, key)
				write(`INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,
					tool_call_id,created_at) VALUES($1,'model_output',$2,0,'tool_call',$3,now())`, firstAgent, outputID, callID)
				write(`UPDATE model_call_contexts SET state='succeeded',api_format='openai-chat-completions',
					api_variant='openai',completed_at=now() WHERE id=$1`, contextID)
				require.NoError(t, tx.Commit())
				if scenario == "pending_completed" {
					tx, err = db.BeginTx(ctx, nil)
					require.NoError(t, err)
					resultID := uuid.New()
					write(`UPDATE tool_calls SET state='completed' WHERE id=$1`, callID)
					write(`INSERT INTO tool_call_results(id,agent_id,tool_call_id,outcome,completed_at)
						VALUES($1,$2,$3,'failed',now())`, resultID, firstAgent, callID)
					write(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,
						tool_call_result_id,is_opening_event,created_at)
						VALUES($1,$2,$3,4,'tool_result','tool_result:'||$4::text,$4::uuid,false,now())`,
						uuid.New(), firstAgent, turnID, resultID)
					require.NoError(t, tx.Commit())
				}
				exec(`UPDATE agents SET current_config_id=$2 WHERE id=$1`, firstAgent, secondID)
				var hasSpawnTool bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT coalesce(compiled_definition->'tools' ? 'spawn_agent',false)
					FROM agent_configs WHERE id=$1`, secondID).Scan(&hasSpawnTool))
				require.False(t, hasSpawnTool)
			} else {
				exec(`UPDATE model_call_contexts SET state='failed',recovery_kind='retry',error_kind='provider_error',
					retry_at=now(),completed_at=now() WHERE id=$1`, contextID)
			}
			firstDelivery, secondDelivery := uuid.New(), uuid.New()
			exec(`INSERT INTO event_webhook_deliveries(id,org_id,agent_id,event_sequence,
				attempt_count,claim_token,claim_expires_at)
				VALUES($1,$2,$3,1,3,$4,now()+interval '20 seconds')`, firstDelivery, ids.OrgID, firstAgent, uuid.New())
			exec(`INSERT INTO event_webhook_deliveries(id,org_id,agent_id,tool_call_id,tool_state,interaction_update)
				VALUES($1,$2,$3,$4,'pending','{"kind":"permission","state":"resolved"}')`,
				secondDelivery, ids.OrgID, secondAgent, uuid.New())
			var originalDelivery string
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT to_jsonb(d)::text FROM event_webhook_deliveries d WHERE id=$1`, firstDelivery,
			).Scan(&originalDelivery))
			var originalToolDelivery string
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT to_jsonb(d)::text FROM event_webhook_deliveries d WHERE id=$1`, secondDelivery,
			).Scan(&originalToolDelivery))
			if scenario == "archived_profile_without_deliveries" {
				exec(`DELETE FROM event_webhook_deliveries WHERE id=$1`, secondDelivery)
			}
			if scenario == "oversized_archived_without_deliveries" {
				exec(`DELETE FROM event_webhook_deliveries WHERE id=$1`, firstDelivery)
			}
			if scenario == "rollback" {
				exec(`CREATE FUNCTION reject_test_config_merge() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN RAISE EXCEPTION 'injected merge failure'; END $$`)
				exec(`CREATE TRIGGER reject_test_config_merge BEFORE DELETE ON agent_configs
					FOR EACH ROW EXECUTE FUNCTION reject_test_config_merge()`)
			}
			if scenario == "already_scoped_delivery" {
				require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 58))
				exec(`WITH webhook AS (
					INSERT INTO event_webhooks(org_id,project_id,agent_id,url,event_types,signing_secret_id)
					VALUES($1,$2,$3,'https://example.com/already',ARRAY['tool_call_update'],$4) RETURNING id)
					UPDATE event_webhook_deliveries SET webhook_id=webhook.id FROM webhook WHERE agent_id=$3`,
					ids.OrgID, ids.ProjectID, secondAgent, ids.ProviderSecretID)
			}
			var checkInbox func(bool)
			if scenario == "profile" || scenario == "rollback" {
				checkInbox = seedWebhookMigrationInbox(t, pool, ids, modelID, profileID, firstID, secondID, winnerID)
			}
			wantError := map[string]string{
				"profile_conflict": "different webhooks", "profile_missing_webhook": "different webhooks",
				"archived_profile_with_deliveries": "different webhooks",
				"bad_hash":                         "hashes do not match", "rollback": "injected merge failure",
				"oversized_agent_url": "event_webhooks_url_check", "oversized_profile_url": "event_webhooks_url_check",
				"oversized_archived_url": "event_webhooks_url_check",
				"self_agent":             "future self-subagents", "self_profile_without_webhook": "future self-subagents",
				"pending_self": "future self-subagents", "pending_profile_conflict": "future self-subagents",
			}[scenario]
			for _, version := range []int64{57, 58} {
				if scenario == "already_scoped_delivery" && version == 57 {
					continue
				}
				require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, version))
				conn, err := pool.Acquire(ctx)
				require.NoError(t, err)
				results, err := conn.Conn().PgConn().Exec(ctx, string(preflight)).ReadAll()
				conn.Release()
				require.NoError(t, err)
				require.Len(t, results, 6)
				var wantAgents []string
				if scenario == "profile_conflict" {
					wantAgents = []string{firstAgent.String(), secondAgent.String()}
				} else if wantError == "different webhooks" {
					wantAgents = []string{secondAgent.String()}
				}
				var agents []string
				for _, row := range results[2].Rows {
					agents = append(agents, string(row[2]))
				}
				require.ElementsMatch(t, wantAgents, agents)
				if strings.HasPrefix(scenario, "oversized_") && scenario != "oversized_archived_without_deliveries" {
					require.Len(t, results[3].Rows, 1)
					require.Equal(t, firstID.String(), string(results[3].Rows[0][4]))
					require.Equal(t, fmt.Sprint(len(firstEndpoint)), string(results[3].Rows[0][5]))
					if scenario == "oversized_archived_url" {
						require.Equal(t, "archived", string(results[3].Rows[0][6]))
						require.Contains(t, string(results[3].Rows[0][7]), "maintenance remove expired deliveries")
						require.Contains(t, string(results[3].Rows[0][7]), "rerun the preflight")
					} else {
						require.NotEqual(t, "archived", string(results[3].Rows[0][6]))
					}
					require.Contains(t, string(results[3].Rows[0][7]), "Never truncate")
				} else {
					require.Empty(t, results[3].Rows)
				}
				if wantError == "future self-subagents" {
					require.Len(t, results[4].Rows, 1)
					require.Equal(t, firstAgent.String(), string(results[4].Rows[0][2]))
					require.Equal(t, firstID.String(), string(results[4].Rows[0][4]))
					require.Contains(t, string(results[4].Rows[0][5]), "archive the original")
				} else {
					require.Empty(t, results[4].Rows)
				}
			}
			err = applyProductionPostgresMigrations(ctx, db)
			if wantError != "" {
				require.ErrorContains(t, err, wantError)
				var count int
				require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM event_webhooks`).Scan(&count))
				require.Zero(t, count)
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM agent_configs WHERE compiled_definition ? 'event_webhook'`,
				).Scan(&count))
				require.Equal(t, 2, count)
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM event_webhook_deliveries WHERE webhook_id IS NULL`,
				).Scan(&count))
				require.Equal(t, 2, count)
				var delivery string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT (to_jsonb(d)-'webhook_id')::text FROM event_webhook_deliveries d WHERE id=$1`, secondDelivery,
				).Scan(&delivery))
				require.JSONEq(t, originalToolDelivery, delivery)
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT (to_jsonb(d)-'webhook_id')::text FROM event_webhook_deliveries d WHERE id=$1`, firstDelivery,
				).Scan(&delivery))
				require.JSONEq(t, originalDelivery, delivery)
				if checkInbox != nil {
					checkInbox(false)
				}
				return
			}
			require.NoError(t, err)
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM agent_configs`).Scan(&count))
			wantConfigs := 1
			if scenario == "distinct_config" || scenario == "other_project_clean" || pendingSpawn {
				wantConfigs = 2
			}
			require.Equal(t, wantConfigs, count)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM event_webhooks`).Scan(&count))
			wantWebhooks := 2
			if scenario == "unroutable_delivery" || scenario == "oversized_archived_without_deliveries" {
				wantWebhooks = 1
			}
			require.Equal(t, wantWebhooks, count)
			for index, statement := range []string{
				`SELECT current_config_id FROM agents WHERE id=$1`,
				`SELECT agent_config_id FROM agent_inputs WHERE agent_id=$1 AND input_kind='config_change'`,
				`SELECT (metadata->>'agent_config_id')::uuid FROM agent_inputs WHERE agent_id=$1 AND input_kind='config_change'`,
				`SELECT agent_config_id FROM model_call_contexts WHERE agent_id=$1`,
			} {
				var actual uuid.UUID
				require.NoError(t, db.QueryRowContext(ctx, statement, firstAgent).Scan(&actual))
				want := winnerID
				if pendingSpawn && index == 0 {
					want = cleanID
				}
				require.Equal(t, want, actual)
			}
			var profileConfig uuid.UUID
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT agent_config_id FROM agent_profile_versions WHERE profile_id=$1`, profileID,
			).Scan(&profileConfig))
			wantProfileConfig := winnerID
			if pendingSpawn && profileWebhook {
				wantProfileConfig = cleanID
			}
			require.Equal(t, wantProfileConfig, profileConfig)
			var endpoint, delivery string
			if scenario == "oversized_archived_without_deliveries" {
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM event_webhooks WHERE agent_id=$1`, firstAgent,
				).Scan(&count))
				require.Zero(t, count)
			} else {
				var profileScope sql.NullString
				require.NoError(t, db.QueryRowContext(ctx, `SELECT w.url,w.agent_profile_id::text,(to_jsonb(d)-'webhook_id')::text
					FROM event_webhook_deliveries d JOIN event_webhooks w ON w.id=d.webhook_id WHERE d.id=$1`, firstDelivery).
					Scan(&endpoint, &profileScope, &delivery))
				wantFirstEndpoint := firstEndpoint
				if pendingSpawn {
					wantFirstEndpoint = "https://example.com/second"
				}
				require.Equal(t, wantFirstEndpoint, endpoint)
				require.Equal(t, profileWebhook, profileScope.Valid)
				require.JSONEq(t, originalDelivery, delivery)
			}
			if scenario == "unroutable_delivery" || scenario == "archived_profile_without_deliveries" {
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM event_webhook_deliveries WHERE id=$1`, secondDelivery,
				).Scan(&count))
				require.Zero(t, count)
			} else {
				var secretID uuid.UUID
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT w.url,w.signing_secret_id,(to_jsonb(d)-'webhook_id')::text FROM event_webhook_deliveries d
					JOIN event_webhooks w ON w.id=d.webhook_id WHERE d.id=$1`, secondDelivery).Scan(&endpoint, &secretID, &delivery))
				wantEndpoint := "https://example.com/second"
				if scenario == "already_scoped_delivery" {
					wantEndpoint = "https://example.com/already"
				}
				require.Equal(t, wantEndpoint, endpoint)
				require.Equal(t, ids.ProviderSecretID, secretID)
				require.JSONEq(t, originalToolDelivery, delivery)
			}
			var compiled []byte
			var hash string
			var source, format sql.NullString
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT compiled_definition,effective_definition_hash,source,source_format FROM agent_configs WHERE id=$1`, winnerID).
				Scan(&compiled, &hash, &source, &format))
			_, err = agentconfig.RuntimeContractFromCompiled(compiled, hash)
			require.NoError(t, err)
			if source.Valid {
				_, err = agentconfig.ParseSource(agentconfig.SourceFormat(format.String), []byte(source.String))
				require.NoError(t, err)
			}
			_, err = db.ExecContext(ctx, `UPDATE agent_configs SET effective_definition_hash='bad' WHERE id=$1`, winnerID)
			require.ErrorContains(t, err, "immutable")
			if checkInbox != nil {
				checkInbox(true)
			}
			require.NoError(t, applyProductionPostgresMigrations(ctx, db))
		})
	}
}

func seedWebhookMigrationInbox(
	t *testing.T,
	pool *pgxpool.Pool,
	ids storagefixture.ProjectIDs,
	modelID, profileID, baseID, derivedID, winnerID uuid.UUID,
) func(bool) {
	t.Helper()
	ctx := t.Context()
	integrationID, receiptID := uuid.New(), uuid.New()
	plannedID := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(ctx, `INSERT INTO integrations(id,org_id,project_id,name,integration_kind,state,
		installed_by_user_id,provider_tenant_id,provider_account_ref,credential_secret_id,created_at,updated_at)
		VALUES($1,$2,$3,'migration','slack_thread','active',$4,'T_MIGRATION','A_MIGRATION',$5,now(),now())`,
		integrationID, ids.OrgID, ids.ProjectID, ids.ProviderAdminUserID, ids.ProviderSecretID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO project_model_grants(org_id,project_id,configured_model_id,created_at,updated_at)
		VALUES($1,$2,$3,now(),now())`, ids.OrgID, ids.ProjectID, modelID)
	require.NoError(t, err)
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"}
	actor, err := executionstore.IntegrationActorParams(integrationstore.IntegrationRecord{
		IntegrationKind: integrationdefinition.SlackThread, Provider: integrationdefinition.ProviderSlack,
		ProviderTenantID: "T_MIGRATION",
	}, "U_MIGRATION", nil)
	require.NoError(t, err)
	recipient := executionstore.InboxLaunchRecipient{
		AgentID: plannedID,
		LaunchClaim: integrationstore.InboxLaunchClaim{
			IntegrationID: integrationID, Address: address, LaunchKey: "migration",
		},
		Launch: executionstore.InboxLaunchPlan{
			ProfileID: profileID, AgentConfigID: derivedID, DerivedBaseConfigID: baseID,
			LaunchedBy: executionstore.InboxLaunchPrincipal{
				Type: identitystore.PrincipalTypeUser, ID: ids.ProviderAdminUserID,
			},
			IdempotencyKey: "migration",
		},
	}
	recipients := map[string]any{
		"launch": recipient,
		"unchanged": map[string]any{
			"agent_id": uuid.New(), "agent_config_id": baseID,
			"launch": map[string]any{"agent_config_id": winnerID, "derived_base_config_id": winnerID},
		},
		"input": map[string]any{"agent_id": uuid.New()},
	}
	plan := map[string]any{
		"message": executionstore.InboxMessage{
			Scope: integrationdefinition.Scope{
				Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "123.456"},
			},
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"Resume frozen launch"}]`),
			Actor:         &actor, Origin: &executionstore.AgentInputOrigin{IntegrationID: integrationID, Address: address},
			SemanticKey: "migration", DeliveryMode: executionstore.DeliveryModeQueued,
		},
		"recipients": recipients,
		"extra":      json.RawMessage(`{"precise":9007199254740993,"untouched":null}`),
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO integration_inbox(id,project_id,integration_id,receipt_key,payload,plan)
		VALUES($1,$2,$3,'migration','{}',$4::jsonb)`, receiptID, ids.ProjectID, integrationID, raw)
	require.NoError(t, err)
	return func(migrated bool) {
		t.Helper()
		if migrated {
			recipient.Launch.AgentConfigID, recipient.Launch.DerivedBaseConfigID = winnerID, winnerID
			recipients["launch"] = recipient
		}
		expected, err := json.Marshal(plan)
		require.NoError(t, err)
		var matchesExpected bool
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT plan=$2::jsonb FROM integration_inbox WHERE id=$1`, receiptID, expected).Scan(&matchesExpected))
		require.True(t, matchesExpected)
		if !migrated {
			return
		}
		store := storage.NewStore(pool)
		receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		result, err := store.Execution().AdmitInboxLaunchRecipient(ctx, receipt.Lease(), "launch", nil)
		require.NoError(t, err)
		require.True(t, result.Created)
		require.Equal(t, plannedID, result.Agent.ID)
		require.Equal(t, winnerID, result.Agent.CurrentConfigID)
		replayed, err := store.Execution().AdmitInboxLaunchRecipient(ctx, receipt.Lease(), "launch", nil)
		require.NoError(t, err)
		require.False(t, replayed.Created)
		require.Equal(t, plannedID, replayed.Agent.ID)
	}
}
