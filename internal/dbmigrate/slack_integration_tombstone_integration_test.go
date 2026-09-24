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
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSlackIntegrationCutoverTombstoneNamesAndCredentials(t *testing.T) {
	for _, scenario := range []struct {
		name, sourceFormat                  string
		invalidLiveCredentials, onlyDeleted bool
	}{
		{name: "live-second"}, {name: "invalid-live-credentials", invalidLiveCredentials: true},
		{name: "json-disabled-and-deleted", sourceFormat: "json"},
		{name: "yaml-disabled-and-deleted", sourceFormat: "yaml"},
		{name: "json-only-deleted", sourceFormat: "json", onlyDeleted: true},
		{name: "yaml-only-deleted", sourceFormat: "yaml", onlyDeleted: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			invalidLiveCredentials := scenario.invalidLiveCredentials
			ctx := t.Context()
			pool := integrationdb.OpenUnmigratedPool(t, ctx)
			db := stdlib.OpenDBFromPool(pool)
			t.Cleanup(func() { _ = db.Close() })
			require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 44))
			ids := storagefixture.ProjectIDs{
				OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
				ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
			}
			storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
			exec := func(query string, args ...any) {
				t.Helper()
				_, err := db.ExecContext(ctx, query, args...)
				require.NoError(t, err)
			}
			modelID, revisionID, configID, profileID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			credentialID, credentialVersionID := uuid.New(), uuid.New()
			exec(`WITH secret AS (
				INSERT INTO secrets(id,org_id,management_kind,owner_kind,name,kind,metadata,
                current_version_id,created_at,updated_at)
				VALUES($1,$2,'tenant','org','slack-tombstones','slack_app_credentials','{}',$3,now(),now()))
				INSERT INTO secret_versions(id,org_id,secret_id,version_number,payload_keys,encryption_scheme,key_id,dek_wrapped_by,
				encrypted_dek,encrypted_dek_nonce,nonce,ciphertext,created_at)
				SELECT $3,org_id,$1,1,ARRAY['bot_token','signing_secret'],encryption_scheme,key_id,dek_wrapped_by,
				encrypted_dek,encrypted_dek_nonce,nonce,ciphertext,now() FROM secret_versions WHERE id=$4`,
				credentialID, ids.OrgID, credentialVersionID, ids.ProviderSecretVersionID)
			exec(`WITH model AS (
				INSERT INTO configured_models(id,org_id,model_provider_config_id,name,current_revision_id,
                 management_kind,created_at,updated_at)
				VALUES($1,$2,$3,'test',$4,'tenant',now(),now()))
				INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,provider_model_slug,
				context_window_tokens,max_output_tokens,created_at) VALUES($4,$2,$1,$3,'test',10000,1000,now())`,
				modelID, ids.OrgID, ids.ProviderConfigID, revisionID)
			compiled, err := json.Marshal(map[string]any{
				"instruction": "Review", "model": map[string]any{"configured_model_id": modelID.String()},
				"tools": map[string]any{"send_integration_message": map[string]any{"enabled": scenario.sourceFormat == "", "permission": map[string]any{"mode": "always_allow", "parameters": map[string]any{}}}},
			})
			require.NoError(t, err)
			exec(`INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,compiled_definition,
                 effective_definition_hash,created_at)
				VALUES($1,$2,$3,$4,$5::jsonb,$6,now())`,
				configID, ids.OrgID, ids.ProjectID, modelID, compiled, fmt.Sprintf("%x", sha256.Sum256(compiled)))
			if scenario.sourceFormat != "" {
				source := map[string]any{
					"instruction": "Review",
					"model":       map[string]any{"provider_config": "openai-prod", "name": "test"},
					"tools":       map[string]any{"send_integration_message": map[string]any{"type": "built_in", "enabled": false}}}
				var raw []byte
				if scenario.sourceFormat == "json" {
					raw, err = json.Marshal(source)
				} else {
					raw, err = yaml.Marshal(source)
				}
				require.NoError(t, err)
				exec(`ALTER TABLE agent_configs DISABLE TRIGGER agent_configs_immutable`)
				exec(`UPDATE agent_configs SET source=$2,source_format=$3,source_hash=$4 WHERE id=$1`,
					configID, string(raw), scenario.sourceFormat, fmt.Sprintf("%x", sha256.Sum256(raw)))
				exec(`ALTER TABLE agent_configs ENABLE TRIGGER agent_configs_immutable`)
			}
			exec(`WITH profile AS (
				INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at)
				VALUES($1,$2,'reviewer',$3,now(),now()))
				INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
				VALUES($3,$2,$1,1,$4,now())`, profileID, ids.ProjectID, versionID, configID)

			deletedID := uuid.MustParse("00000000-0000-4000-8000-000000000001")
			liveID := uuid.MustParse("00000000-0000-4000-8000-000000000002")
			for i, integrationID := range []uuid.UUID{deletedID, liveID} {
				exec(`INSERT INTO integration_installs(id,org_id,project_id,agent_profile_id,installed_by_user_id,
					provider,integration_kind,connection_mode,state,provider_tenant_id,provider_account_ref,
					provider_identity,credential_secret_id,created_at,updated_at)
					VALUES($1,$2,$3,$4,$5,'slack','agent_profile','webhook','active',$6,$7,'{"bot_user_id":"U123"}',$8,$9,$9)`,
					integrationID, ids.OrgID, ids.ProjectID, profileID, ids.ProviderAdminUserID,
					fmt.Sprintf("T%d", i+1), fmt.Sprintf("A%d", i+1), credentialID,
					time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC))
			}
			exec(`UPDATE integration_installs SET deleted_at='2026-02-01',credential_secret_id=NULL WHERE id=$1`, deletedID)
			if scenario.sourceFormat != "" {
				exec(`UPDATE integration_installs SET state='disabled' WHERE id=$1`, liveID)
			}
			if scenario.onlyDeleted {
				exec(
					`UPDATE integration_installs SET state='active',deleted_at='2026-02-01',credential_secret_id=NULL WHERE id=$1`,
					liveID,
				)
			}
			if invalidLiveCredentials {
				exec(`UPDATE integration_installs SET credential_secret_id=NULL WHERE id=$1`, liveID)
			}
			agentID, targetID := uuid.New(), uuid.New()
			exec(`INSERT INTO agents(id,org_id,project_id,state,name,agent_profile_id,current_config_id,created_at,updated_at)
				VALUES($1,$2,$3,'active','reviewer',$4,$5,now(),now())`, agentID, ids.OrgID, ids.ProjectID, profileID, configID)
			exec(`INSERT INTO integration_targets(id,project_id,agent_id,integration_install_id,target_ref,
                     provider_ref,provider_ref_kind,created_at,updated_at)
				VALUES($1,$2,$3,$4,'slack','C123:111.222','thread',now(),now())`, targetID, ids.ProjectID, agentID, liveID)
			exec(`UPDATE agents SET integration_target_id=$2 WHERE id=$1`, agentID, targetID)

			err = applyProductionPostgresMigrations(ctx, db)
			if invalidLiveCredentials {
				require.ErrorContains(t, err, liveID.String())
				require.Equal(t, int64(44), currentPostgresMigrationVersion(t, ctx, db))
				var state string
				var credential sql.NullString
				require.NoError(
					t,
					db.QueryRowContext(
						ctx,
						`SELECT state,credential_secret_id::text FROM integration_installs WHERE id=$1`,
						liveID,
					).Scan(
						&state,
						&credential,
					),
				)
				require.Equal(t, "active", state, "invalid active setup must never silently become disconnected")
				require.False(t, credential.Valid)
				exec(`UPDATE integration_installs SET credential_secret_id=$2 WHERE id=$1`, liveID, credentialID)
				require.NoError(t, applyProductionPostgresMigrations(ctx, db))
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, int64(46), currentPostgresMigrationVersion(t, ctx, db))
			var subscriptions int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_subscriptions`).Scan(&subscriptions))
			require.Zero(t, subscriptions, "neither live nor deleted integration history grants receive routes at cutover")
			var assignments int
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT count(*) FROM integration_states WHERE kind='agent_conversation'`).Scan(&assignments))
			if scenario.onlyDeleted {
				require.Zero(t, assignments, "deleted integrations do not assign conversations")
			} else {
				require.Equal(t, 1, assignments, "only targets formerly producing successors assign conversations")
				assertSlackCutoverConversationState(t, db, ids.ProjectID, agentID, liveID,
					integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"})
			}
			var slot sql.NullString
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT selection_slot FROM integration_targets WHERE id=$1`, targetID).Scan(&slot))
			require.False(t, slot.Valid)
			if scenario.sourceFormat != "" {
				assertSlackTombstoneSourceResave(
					t,
					storage.NewStore(
						pool,
					),
					db,
					ids,
					modelID,
					configID,
					profileID,
					liveID,
					scenario.onlyDeleted,
				)
				return
			}
			for _, expected := range []struct {
				id          uuid.UUID
				name, state string
				deleted     bool
			}{{liveID, "slack", "active", false}, {deletedID, "slack-2", "disconnected", true}} {
				var name, state, launcherProfile string
				var credential sql.NullString
				var deleted bool
				require.NoError(t, db.QueryRowContext(ctx, `SELECT name,state,credential_secret_id::text,deleted_at IS NOT NULL,
					settings->'launcher'->'slots'->0->>'agent_profile_id' FROM project_integrations WHERE id=$1`, expected.id).
					Scan(&name, &state, &credential, &deleted, &launcherProfile))
				require.Equal(t, expected.name, name)
				require.Equal(t, expected.state, state)
				require.Equal(t, expected.deleted, deleted)
				require.Equal(t, profileID.String(), launcherProfile)
				require.Equal(t, !deleted, credential.Valid)
				if credential.Valid {
					require.Equal(t, credentialID.String(), credential.String)
				}
			}
			var targetIntegration uuid.UUID
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT integration_id FROM integration_targets WHERE id=$1`,
					targetID,
				).Scan(
					&targetIntegration,
				),
			)
			require.Equal(t, liveID, targetIntegration)
			var successor []byte
			require.NoError(
				t,
				db.QueryRowContext(
					ctx,
					`SELECT config.compiled_definition FROM agents agent JOIN agent_configs config ON config.id=agent.current_config_id WHERE agent.id=$1`,
					agentID,
				).Scan(
					&successor,
				),
			)
			var config map[string]any
			require.NoError(t, json.Unmarshal(successor, &config))
			tools := testutil.RequireType[map[string]any](t, config["tools"])
			tool := testutil.RequireType[map[string]any](t, tools["int__slack__post_message"])
			require.Equal(t, liveID.String(), tool["integration_id"])
			require.NotContains(t, config["tools"], "int__slack-2__post_message")
			require.NotContains(t, config, "interaction_handlers")
		})
	}
}

func assertSlackTombstoneSourceResave(
	t *testing.T,
	store *storage.Store,
	db *sql.DB,
	ids storagefixture.ProjectIDs,
	modelID, configID, profileID, liveID uuid.UUID,
	onlyDeleted bool,
) {
	t.Helper()
	ctx := t.Context()
	var source, format, sourceHash, hash string
	var compiled []byte
	var preservedConfigID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT config.id,config.source,config.source_format,config.source_hash,
        config.compiled_definition,config.effective_definition_hash
        FROM agent_profiles profile JOIN agent_profile_versions version ON version.id=profile.current_version_id
        JOIN agent_configs config ON config.id=version.agent_config_id WHERE profile.id=$1`, profileID).
		Scan(&preservedConfigID, &source, &format, &sourceHash, &compiled, &hash))
	require.Equal(t, configID, preservedConfigID)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(source))), sourceHash)
	contract, err := agentconfig.RuntimeContractFromCompiled(compiled, hash)
	require.NoError(t, err)
	require.Empty(t, contract.InteractionHandlers)
	require.NotContains(t, source, "send_integration_message")
	require.NotContains(t, source, "integration_id")
	require.NotContains(t, source, "int__slack-2__post_message")
	if onlyDeleted {
		require.Empty(
			t,
			contract.IntegrationTools,
			"deleted-only projects remove the old disabled policy without granting a tool",
		)
		require.NotContains(t, source, "int__")
	} else {
		require.Len(t, contract.IntegrationTools, 1)
		tool := contract.IntegrationTools["int__slack__post_message"]
		require.False(t, tool.Enabled)
		require.Equal(t, liveID, tool.IntegrationID)
		integration, err := store.Integrations().GetProjectIntegrationByName(ctx, ids.ProjectID, "slack")
		require.NoError(t, err)
		require.Equal(t, "disconnected", string(integration.State))
	}
	_, err = store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, ConfiguredModelID: modelID,
	})
	require.NoError(t, err)
	source = strings.Replace(source, "Review", "Review edited", 1)
	recompile := func() agentconfig.Compiled {
		t.Helper()
		body, err := agentconfigcompile.Compile(ctx, store, ids.OrgID, ids.ProjectID,
			agentconfig.CompileOptions{}, agentconfig.SourceFormat(format), source)
		require.NoError(t, err)
		_, err = store.Execution().CreateAgentConfig(ctx, body.CreateInput(ids.ProjectID))
		require.NoError(t, err, "the migrated profile source can still be saved")
		var result agentconfig.Compiled
		require.NoError(t, json.Unmarshal(body.CompiledDefinition, &result))
		require.Equal(t, "Review edited", result.Instruction)
		if !onlyDeleted {
			require.Equal(t, liveID, result.Tools["int__slack__post_message"].IntegrationID)
			require.False(t, result.Tools["int__slack__post_message"].Enabled)
		}
		return result
	}
	recompile()
	reusedName := "slack-2"
	if onlyDeleted {
		reusedName = "slack"
	}
	var replacementID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx,
		`INSERT INTO project_integrations(org_id,project_id,name,integration_type,state,created_at,updated_at)
        VALUES($1,$2,$3,'slack_thread','disconnected',now(),now()) RETURNING id`,
		ids.OrgID, ids.ProjectID, reusedName).Scan(
		&replacementID,
	))
	recompiled := recompile()
	require.NotContains(t, recompiled.Tools, "int__"+reusedName+"__post_message")
	for _, tool := range recompiled.Tools {
		if tool.IntegrationID != uuid.Nil {
			require.False(t, tool.Enabled, "recompiling must not grant any integration tool")
		}
	}
}
