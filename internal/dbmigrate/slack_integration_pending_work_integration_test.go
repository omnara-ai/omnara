//go:build integration

package dbmigrate_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestSlackIntegrationCutoverPendingInteractionsRecover(t *testing.T) {
	for _, scenario := range []struct {
		name, kind, scope string
		steer             bool
	}{
		{name: "dashboard_question", kind: "question"},
		{name: "dashboard_permission", kind: "permission"},
		{name: "slack_thread_question", kind: "question", scope: "thread"},
		{name: "slack_thread_steering", kind: "question", scope: "thread", steer: true},
		{name: "slack_dm_steering", kind: "question", scope: "dm", steer: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newSlackPendingWorkFixture(t, scenario.kind, scenario.scope)
			f.migrate(t)
			ctx := t.Context()
			execution := f.store.Execution()
			interaction, found, err := execution.GetAgentInteraction(
				ctx,
				f.ids.ProjectID,
				f.agentID,
				f.interactionID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, interaction.State)
			require.Equal(t, f.turnID, interaction.TurnID)
			require.JSONEq(t, string(f.request), string(interaction.Request))
			require.Empty(
				t,
				interaction.Destination,
				"old interactions keep their original dashboard resolution path",
			)
			_, found, err = execution.ClaimNextAgentWork(ctx, pendingWorkClaimInput())
			require.ErrorIs(t, err, storeerr.ErrNoClaimableAgentWakeup)
			require.False(t, found, "an unanswered interaction must stay parked after config cutover")

			resolution := executionstore.ResolveAgentInteractionInput{
				ProjectID: f.ids.ProjectID, AgentID: f.agentID, ID: f.interactionID, Actor: f.actor,
				Resolution: interactionform.Resolution{
					Answers: []interactionform.Answer{{OptionIndices: []int{0}}},
				},
			}
			if scenario.steer {
				input := f.steer(t)
				interaction, found, err = execution.GetAgentInteraction(
					ctx,
					f.ids.ProjectID,
					f.agentID,
					f.interactionID,
				)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateCanceled, interaction.State)
				require.Equal(t, input.ID, interaction.ResolvedByInputID)
				_, err = execution.ResolveAgentInteraction(ctx, resolution)
				require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			} else {
				resolved, err := execution.ResolveAgentInteraction(ctx, resolution)
				require.NoError(t, err)
				require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
				require.Equal(t, f.turnID, resolved.TurnID)
				require.NotEqual(t, uuid.Nil, resolved.ResolvedByInputID)
				repeated, err := execution.ResolveAgentInteraction(ctx, resolution)
				require.NoError(t, err)
				require.Equal(t, resolved.ResolvedByInputID, repeated.ResolvedByInputID)
			}
			tool, err := execution.GetToolCall(ctx, f.ids.ProjectID, f.agentID, f.toolID)
			require.NoError(t, err)
			if scenario.kind == "permission" {
				require.Equal(t, executionstore.ToolCallStateReady, tool.State)
			} else {
				require.Equal(t, executionstore.ToolCallStateCompleted, tool.State)
				outcome := executionstore.ToolResultOutcomeSucceeded
				if scenario.steer {
					outcome = executionstore.ToolResultOutcomeCanceled
				}
				require.Equal(t, outcome, tool.Outcome)
			}
			claim, found, err := execution.ClaimNextAgentWork(ctx, pendingWorkClaimInput())
			require.NoError(t, err)
			require.True(t, found, "resolution or steering must restore schedulable work")
			require.Equal(t, f.agentID, claim.AgentID)
			if scenario.kind == "permission" {
				require.Equal(t, executionstore.AgentWorkTool, claim.Kind)
				require.Equal(t, f.turnID, claim.Tool.TurnID)
			} else {
				require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
				if scenario.steer {
					require.NotEqual(t, f.turnID, claim.Model.TurnID)
				} else {
					require.Equal(t, f.turnID, claim.Model.TurnID)
				}
				if scenario.scope != "" {
					f.captureNewPrompts(t, claim)
				}
			}
		})
	}
}

func TestSlackIntegrationCutoverExpiredRuntimeRecovers(t *testing.T) {
	f := newSlackPendingWorkFixture(t, "", "thread")
	f.migrate(t)
	ctx := t.Context()
	execution := f.store.Execution()
	model, found, err := execution.GetModelCallContext(ctx, f.ids.ProjectID, f.agentID, f.contextID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ModelCallContextStarted, model.State)
	require.Equal(t, f.runtimeID, model.RuntimeLockID)
	require.Equal(
		t,
		f.configID,
		model.AgentConfigID,
		"cutover must not rewrite an in-flight context's config identity",
	)
	reaped, err := execution.ReapExpiredAgentRuntimeLocks(ctx, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, reaped)
	model, found, err = execution.GetModelCallContext(ctx, f.ids.ProjectID, f.agentID, f.contextID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ModelCallContextFailed, model.State)
	require.Equal(t, executionstore.ModelCallRecoveryRetry, model.RecoveryKind)
	require.NotNil(t, model.RetryAt)
	require.Contains(t, string(model.ErrorDetails), `"outcome_ambiguous": true`)
	claim, found, err := execution.ClaimNextAgentWork(ctx, pendingWorkClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
	require.Equal(t, f.agentID, claim.AgentID)
	require.Equal(t, f.turnID, claim.Model.TurnID)
	require.NotEqual(t, f.runtimeID, claim.RuntimeLock.ID)
	require.Equal(t, executionstore.ModelWorkStart, claim.Model.Kind,
		"the cutover config is a later semantic event, so recovery starts from the current frontier")
	resumed := f.claimModel(t, claim)
	require.NotEqual(t, f.contextID, resumed.ID)
	require.NotEqual(t, f.configID, resumed.AgentConfigID)
	require.Greater(t, resumed.InputEventSequence, model.InputEventSequence)
}

func pendingWorkClaimInput() executionstore.ClaimNextAgentWorkInput {
	return executionstore.ClaimNextAgentWorkInput{WorkerProcessID: uuid.New(), LeaseDuration: time.Minute}
}

type slackPendingWorkFixture struct {
	db                                              *sql.DB
	store                                           *storage.Store
	ids                                             storagefixture.ProjectIDs
	agentID, turnID, configID, contextID, runtimeID uuid.UUID
	integrationID, targetID, toolID, interactionID  uuid.UUID
	actor                                           *executionstore.ActorParams
	request                                         json.RawMessage
	scope, ref                                      string
}

func (f *slackPendingWorkFixture) migrate(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, applyProductionPostgresMigrations(ctx, f.db))
	require.Equal(t, latestPostgresMigrationVersion(t), currentPostgresMigrationVersion(t, ctx, f.db))
	if f.scope == "" {
		return
	}
	var turnID, configID uuid.UUID
	var opening bool
	require.NoError(
		t,
		f.db.QueryRowContext(ctx, `SELECT event.turn_id,event.is_opening_event,input.agent_config_id
        FROM agent_inputs input
        JOIN agent_events event ON event.agent_id=input.agent_id AND event.id=input.admitted_event_id
        WHERE input.agent_id=$1 AND input.input_idempotency_key='slack_integration_cutover'`, f.agentID).
			Scan(&turnID, &opening, &configID),
	)
	require.Equal(
		t,
		f.turnID,
		turnID,
		"successor config must be admitted into the pending work's original turn",
	)
	require.False(t, opening)
	require.NotEqual(t, f.configID, configID)
	var owner, pinnedTarget uuid.UUID
	var handler string
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT target.agent_id,agent.interaction_target_id,
        agent.interaction_handler_key
        FROM integration_targets target JOIN agents agent ON agent.id=target.agent_id
        WHERE target.id=$1 AND target.integration_id=$2 AND target.scope_kind=$3 AND target.scope_ref=$4
          AND target.launch_key='default' AND target.deleted_at IS NULL`,
		f.targetID,
		f.integrationID,
		f.scope,
		f.ref).
		Scan(&owner, &pinnedTarget, &handler))
	require.Equal(t, f.agentID, owner)
	require.Equal(t, f.targetID, pinnedTarget)
	require.Equal(t, "slack", handler)
	assertSlackCutoverConversationState(t, f.db, f.ids.ProjectID, f.agentID, f.integrationID,
		integrationstore.ConversationAddress{Kind: f.scope, Ref: f.ref})
	snapshot, err := f.store.Execution().CaptureAgentConfigForModelContext(ctx, f.ids.ProjectID, f.agentID)
	require.NoError(t, err)
	require.Equal(t, configID, snapshot.AgentConfig.ID)
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition, snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	require.NoError(t, err)
	require.Equal(t, f.integrationID, contract.InteractionHandlers[handler].IntegrationID)
	var subscriptions, turns int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM integration_subscriptions
        WHERE agent_id=$1 AND integration_id=$2 AND scope_kind=$3 AND scope_ref=$4`,
		f.agentID, f.integrationID, f.scope, f.ref).Scan(&subscriptions))
	require.Equal(t, 1, subscriptions)
	require.NoError(t, f.db.QueryRowContext(ctx,
		`SELECT count(*) FROM agent_turns WHERE agent_id=$1`, f.agentID).Scan(&turns))
	require.Equal(t, 1, turns)
	var wakeupCovered bool
	require.NoError(
		t,
		f.db.QueryRowContext(ctx,
			`SELECT CASE WHEN EXISTS(SELECT 1 FROM agent_runtime_locks WHERE agent_id=head.agent_id)
 THEN wake.agent_id IS NULL WHEN head.logical_ready_at IS NULL THEN wake.agent_id IS NULL
 ELSE wake.agent_id IS NOT NULL AND wake.ready_at <= head.logical_ready_at END
 FROM agent_execution_state head LEFT JOIN agent_wakeups wake ON wake.agent_id=head.agent_id
 WHERE head.agent_id=$1`, f.agentID).Scan(&wakeupCovered),
	)
	require.True(t, wakeupCovered, "cutover must reconcile the scheduler index with the actual pending work")
}

func newSlackPendingWorkFixture(t *testing.T, kind, scope string) slackPendingWorkFixture {
	t.Helper()
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, applyProductionPostgresMigrationsThrough(t, ctx, db, 47))
	f := slackPendingWorkFixture{
		db: db,
		store: storage.NewStore(
			pool,
			storage.WithModelCallRetryBackoff(func(int, string) time.Duration { return 0 }),
		),
		ids: storagefixture.ProjectIDs{
			OrgID:                   uuid.New(),
			ProjectID:               uuid.New(),
			ProviderAdminUserID:     uuid.New(),
			ProviderSecretID:        uuid.New(),
			ProviderSecretVersionID: uuid.New(),
			ProviderConfigID:        uuid.New(),
		},
		agentID: uuid.New(), turnID: uuid.New(), configID: uuid.New(), contextID: uuid.New(), runtimeID: uuid.New(),
		integrationID: uuid.New(), targetID: uuid.New(), toolID: uuid.New(), interactionID: uuid.New(),
		scope: scope, ref: "C123:111.222",
	}
	if scope == "dm" {
		f.ref = "D123"
	}
	storagefixture.SeedProject(t, ctx, pool, f.ids, time.Now())
	actor, err := executionstore.OmnaraActorParams(f.ids.OrgID, identitystore.PrincipalRecord{
		ID: f.ids.ProviderAdminUserID, Type: identitystore.PrincipalTypeUser,
	})
	require.NoError(t, err)
	f.actor = actor
	modelID, revisionID, profileID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	credentialID, credentialVersionID, inputID, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := tx.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	exec(`WITH secret AS (
        INSERT INTO secrets(id,org_id,management_kind,owner_kind,name,kind,metadata,current_version_id,
        created_at,updated_at)
        VALUES($1,$2,'tenant','org','pending-slack','slack_app_credentials','{}',$3,now(),now()))
        INSERT INTO secret_versions(id,org_id,secret_id,version_number,payload_keys,encryption_scheme,key_id,
        dek_wrapped_by,
        encrypted_dek,encrypted_dek_nonce,nonce,ciphertext,created_at)
        SELECT $3,org_id,$1,1,ARRAY['bot_token','signing_secret'],encryption_scheme,key_id,dek_wrapped_by,
        encrypted_dek,encrypted_dek_nonce,nonce,ciphertext,now() FROM secret_versions WHERE id=$4`,
		credentialID, f.ids.OrgID, credentialVersionID, f.ids.ProviderSecretVersionID)
	exec(`WITH model AS (
        INSERT INTO configured_models(id,org_id,model_provider_config_id,name,current_revision_id,
        management_kind,created_at,updated_at)
        VALUES($1,$2,$3,'test',$4,'tenant',now(),now()))
        INSERT INTO configured_model_revisions(id,org_id,configured_model_id,model_provider_config_id,
        provider_model_slug,
        context_window_tokens,max_output_tokens,created_at) VALUES($4,$2,$1,$3,'test',10000,1000,now())`,
		modelID, f.ids.OrgID, f.ids.ProviderConfigID, revisionID)
	exec(`INSERT INTO project_model_grants(org_id,project_id,configured_model_id,created_at,updated_at)
        VALUES($1,$2,$3,now(),now())`, f.ids.OrgID, f.ids.ProjectID, modelID)
	compiled := fmt.Sprintf(`{"instruction":"Review","model":{"configured_model_id":%q},"tools":{
        "send_integration_message":{"enabled":true,"permission":{"mode":"always_allow","parameters":{}}},
        "ask_question":{"enabled":true,"permission":{"mode":"always_allow","parameters":{}}},
        "read_process":{"enabled":true,"permission":{"mode":"always_ask","parameters":{}}}}}`,
		modelID.String())
	var compiledObject map[string]any
	require.NoError(t, json.Unmarshal([]byte(compiled), &compiledObject))
	canonical, err := json.Marshal(compiledObject)
	require.NoError(t, err)
	compiled = string(canonical)
	exec(`INSERT INTO agent_configs(id,org_id,project_id,configured_model_id,compiled_definition,
        effective_definition_hash,created_at)
        VALUES($1,$2,$3,$4,$5::jsonb,$6,now())`, f.configID, f.ids.OrgID, f.ids.ProjectID, modelID,
		compiled, fmt.Sprintf("%x", sha256.Sum256([]byte(compiled))))
	exec(`WITH profile AS (
        INSERT INTO agent_profiles(id,project_id,name,current_version_id,created_at,updated_at)
        VALUES($1,$2,'reviewer',$3,now(),now()))
        INSERT INTO agent_profile_versions(id,project_id,profile_id,generation,agent_config_id,created_at)
        VALUES($3,$2,$1,1,$4,now())`, profileID, f.ids.ProjectID, versionID, f.configID)
	exec(
		`INSERT INTO integration_installs(id,org_id,project_id,agent_profile_id,installed_by_user_id,provider,
        integration_kind,
        connection_mode,state,provider_tenant_id,provider_account_ref,provider_identity,credential_secret_id,
        created_at,updated_at)
        VALUES($1,$2,$3,$4,$5,'slack','agent_profile','webhook','active','T123','A123',
        '{"bot_user_id":"U123"}',$6,now(),now())`,
		f.integrationID,
		f.ids.OrgID,
		f.ids.ProjectID,
		profileID,
		f.ids.ProviderAdminUserID,
		credentialID,
	)
	exec(`INSERT INTO agents(id,org_id,project_id,state,name,agent_profile_id,current_config_id,
        next_event_sequence,created_at,updated_at)
        VALUES($1,$2,$3,'active','reviewer',$4,$5,3,now(),now())`,
		f.agentID, f.ids.OrgID, f.ids.ProjectID, profileID, f.configID)
	exec(
		`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,agent_config_id,queued_at)
        VALUES($1,$2,$3,'received','config_change','immediate',$4,now())`,
		inputID,
		f.ids.ProjectID,
		f.agentID,
		f.configID,
	)
	exec(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,
        is_opening_event,created_at)
        VALUES($1,$2,$3,1,'agent_input',$4,$5,true,now())`,
		eventID, f.agentID, f.turnID, "agent_input:"+inputID.String(), inputID)
	exec(`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
        VALUES($1,$2,1,$3,$3)`, f.turnID, f.agentID, eventID)
	exec(
		`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now() WHERE id=$1`,
		inputID,
		eventID,
	)
	contentID, contentEventID := uuid.New(), uuid.New()
	exec(`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,queued_at)
        VALUES($1,$2,$3,'received','content','queued',now())`, contentID, f.ids.ProjectID, f.agentID)
	exec(
		`INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,ordinal,block_kind,text_content,created_at)
        VALUES($1,'agent_input',$2,0,'text','Review the change',now())`,
		f.agentID,
		contentID,
	)
	exec(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,agent_input_id,
        is_opening_event,created_at)
        VALUES($1,$2,$3,2,'agent_input',$4,$5,true,now())`,
		contentEventID, f.agentID, f.turnID, "agent_input:"+contentID.String(), contentID)
	exec(
		`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=now(),resolved_at=now() WHERE id=$1`,
		contentID,
		contentEventID,
	)
	exec(
		`UPDATE agent_turns SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1`,
		f.turnID,
		contentEventID,
	)

	if scope != "" {
		exec(
			`INSERT INTO integration_targets(id,project_id,agent_id,integration_install_id,target_ref,provider_ref,
        provider_ref_kind,created_at,updated_at)
            VALUES($1,$2,$3,$4,'slack',$5,$6,now(),now())`,
			f.targetID,
			f.ids.ProjectID,
			f.agentID,
			f.integrationID,
			f.ref,
			scope,
		)
		exec(`UPDATE agents SET integration_target_id=$2 WHERE id=$1`, f.agentID, f.targetID)
		exec(
			`INSERT INTO agent_wakeups(agent_id,ready_at,updated_at,metadata) VALUES($1,now(),now(),'{}')`,
			f.agentID,
		)
	}
	exec(
		`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,agent_config_id,
        configured_model_revision_id,input_event_sequence,runtime_lock_id,state,created_at)
        VALUES($1,$2,$3,$4,'normal',1,$5,$6,2,$7,'started',now())`,
		f.contextID,
		f.ids.OrgID,
		f.ids.ProjectID,
		f.agentID,
		f.configID,
		revisionID,
		f.runtimeID,
	)
	if kind == "" {
		exec(
			`INSERT INTO agent_runtime_locks(id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
            VALUES($1,$2,$3,now()-interval '2 minutes',now()-interval '2 minutes',now()-interval '1 minute')`,
			f.runtimeID,
			f.agentID,
			uuid.New(),
		)
	} else {
		f.seedInteraction(t, exec, kind)
	}
	require.NoError(t, tx.Commit())
	return f
}

func (f *slackPendingWorkFixture) seedInteraction(t *testing.T, exec func(string, ...any), kind string) {
	t.Helper()
	request, toolName, toolInput := pendingWorkRequest(t, kind)
	f.request = request
	outputID, eventID := uuid.New(), uuid.New()
	exec(`INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at)
        VALUES($1,$2,$3,'tool_use',now())`, outputID, f.agentID, f.contextID)
	exec(
		`INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
        VALUES($1,$2,$3,'pending-review',$4,$5,'built_in','awaiting_authorization',now())`,
		f.toolID,
		f.agentID,
		outputID,
		toolName,
		toolInput,
	)
	exec(
		`INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,created_at)
        VALUES($1,'model_output',$2,0,'tool_call',$3,now())`,
		f.agentID,
		outputID,
		f.toolID,
	)
	exec(`UPDATE model_call_contexts SET state='succeeded',api_format='openai',api_variant='responses',
        completed_at=now() WHERE id=$1`,
		f.contextID)
	exec(`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,idempotency_key,model_output_id,
        is_opening_event,created_at)
        VALUES($1,$2,$3,3,'model_output',$4,$5,false,now())`,
		eventID, f.agentID, f.turnID, "model_output:"+outputID.String(), outputID)
	exec(`UPDATE agents SET next_event_sequence=4 WHERE id=$1`, f.agentID)
	exec(
		`UPDATE agent_turns SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1`,
		f.turnID,
		eventID,
	)
	exec(`INSERT INTO agent_interactions(id,agent_id,tool_call_id,interaction_kind,state,request,created_at)
        VALUES($1,$2,$3,$4,'open',$5,now())`, f.interactionID, f.agentID, f.toolID, kind, f.request)
	if kind == "permission" {
		exec(`UPDATE tool_calls SET state='awaiting_permission' WHERE id=$1`, f.toolID)
	} else {
		exec(`UPDATE tool_calls SET state='ready' WHERE id=$1`, f.toolID)
		exec(`UPDATE tool_calls SET state='waiting' WHERE id=$1`, f.toolID)
	}
}

func pendingWorkRequest(t *testing.T, kind string) (json.RawMessage, string, json.RawMessage) {
	t.Helper()
	var requestJSON json.RawMessage
	form, err := interactionform.New("Review question", nil, []interactionform.Question{{
		Prompt: "Continue the review?", Options: []interactionform.Option{{Label: "Yes"}},
	}})
	require.NoError(t, err)
	requestJSON, err = json.Marshal(form)
	require.NoError(t, err)
	toolName, toolInput := "ask_question", requestJSON
	if kind == "permission" {
		processID, err := publicid.Encode(publicid.KindProcess, uuid.New())
		require.NoError(t, err)
		toolName, toolInput = "read_process", json.RawMessage(fmt.Sprintf(`{"process_id":%q}`, processID))
		authorization, err := toolpermission.NewAuthorization(toolName, toolInput)
		require.NoError(t, err)
		form, err := toolpermission.NewAllowDenyForm("Allow reading the process?", nil)
		require.NoError(t, err)
		mode, found := toolpermission.FindMode(
			toolpermission.CommonModeDescriptors(),
			toolpermission.ModeAlwaysAsk,
		)
		require.True(t, found)
		request, err := toolpermission.NewRequest(
			mode, toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk), authorization, form,
		)
		require.NoError(t, err)
		requestJSON, err = json.Marshal(request)
		require.NoError(t, err)
	}
	return requestJSON, toolName, toolInput
}

func (f *slackPendingWorkFixture) steer(t *testing.T) executionstore.AgentInputRecord {
	t.Helper()
	ctx := t.Context()
	integrations := f.store.Integrations()
	setup, err := integrations.GetIntegration(ctx, f.ids.ProjectID, f.integrationID)
	require.NoError(t, err)
	actor, err := executionstore.IntegrationActorParams(setup, "U_REVIEWER", nil)
	require.NoError(t, err)
	scope := integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"}
	if f.scope == "dm" {
		scope = integrationdefinition.SlackScope{ChannelID: "D123"}
	}
	address := integrationstore.ConversationAddress{Kind: f.scope, Ref: f.ref}
	_, created, err := integrations.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, IntegrationID: f.integrationID,
		ReceiptKey: "cutover-steering", Payload: json.RawMessage(`{"verified":true}`),
	})
	require.NoError(t, err)
	require.True(t, created)
	receipt, found, err := integrations.ClaimIntegrationInbox(
		ctx,
		integrationstore.ClaimIntegrationInboxInput{
			ProjectID: f.ids.ProjectID, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, found)
	router := integration.NewIntegrationRouter(f.store.Execution(), integrations)
	plan, created, err := router.Freeze(ctx, receipt.Lease(), &integration.IntegrationEvent{
		Event: integrationdefinition.Event{
			Kind:      integrationdefinition.EventMessage,
			Mentioned: false,
			Scope:     integrationdefinition.Scope{Slack: &scope},
		},
		Actor: actor, SemanticKey: "cutover-steering", DeliveryMode: executionstore.DeliveryModeSteering,
		ContentBlocks: json.RawMessage(
			`[{"type":"text","text":"Use the new instructions instead"}]`,
		),
		CancelOpenInteractions: true,
	}, nil)
	require.NoError(t, err)
	require.True(t, created)
	require.Len(t, plan.Recipients, 1)
	for _, recipient := range plan.Recipients {
		require.Equal(t, f.agentID, recipient.AgentID)
		require.Nil(t, recipient.Launch)
		require.Nil(t, recipient.LaunchClaim)
		require.NotNil(t, recipient.Subscription)
		require.Equal(t, []integrationstore.ConversationAddress{address}, recipient.Subscription.Alternatives)
	}
	results, err := router.Admit(ctx, receipt.Lease(), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Nil(t, results[0].Launch)
	result := results[0].Input
	require.NotNil(t, result)
	require.True(t, result.Created)
	require.Equal(t, f.targetID, result.AgentInput.IntegrationTargetID)
	require.ElementsMatch(t, []uuid.UUID{f.interactionID}, result.CanceledInteractionIDs)
	return result.AgentInput
}

func (f *slackPendingWorkFixture) claimModel(
	t *testing.T, work executionstore.ClaimedAgentWork,
) executionstore.ModelCallContextRecord {
	t.Helper()
	ctx := t.Context()
	var sequence int64
	require.NoError(t, f.db.QueryRowContext(ctx,
		`SELECT max(sequence) FROM agent_events WHERE agent_id=$1`, f.agentID).Scan(&sequence))

	prepared1, err := f.store.Execution().
		PrepareNormalModelCall(ctx, executionstore.PrepareNormalModelCallInput{
			ProjectID: f.ids.ProjectID, AgentID: f.agentID, RuntimeLockID: work.RuntimeLock.ID,
			OpeningInputIDs:          work.Model.InputIDs,
			SourceModelCallContextID: work.Model.SourceModelCallContextID,
			SourceModelOutputID:      work.Model.SourceModelOutputID,
		})
	claim := prepared1.Claim

	require.NoError(t, err)
	require.True(t, claim.Created)
	require.True(t, claim.Claimed)
	var turnID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx,
		`SELECT turn_id FROM model_call_contexts WHERE id=$1`, claim.Context.ID).Scan(&turnID))
	require.Equal(t, work.Model.TurnID, turnID)
	return claim.Context
}

func (f *slackPendingWorkFixture) captureNewPrompts(t *testing.T, work executionstore.ClaimedAgentWork) {
	t.Helper()
	ctx := t.Context()
	model := f.claimModel(t, work)
	questionJSON, questionTool, questionInput := pendingWorkRequest(t, "question")
	permissionJSON, permissionTool, permissionInput := pendingWorkRequest(t, "permission")
	_, calls, err := f.store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: f.ids.ProjectID, AgentID: f.agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: model.ID,
			ProviderResponse: modelenvelope.ResponseEnvelope{
				RequestedProviderModelSlug: "test", ServedProviderModelSlug: "test",
				APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
				Normalized: modelenvelope.ResponseNormalized{
					ID: "resp_cutover", StopReason: modelenvelope.StopReasonToolUse,
					Content: []modelenvelope.ResponsePart{
						{Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "question",
							ToolName: questionTool, ToolInput: questionInput},
						{Type: modelenvelope.ResponsePartTypeToolCall, ProviderCallID: "permission",
							ToolName: permissionTool, ToolInput: permissionInput},
					},
				},
			},
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ID: uuid.New(), ProviderCallID: "question", Type: "built_in"},
				{ID: uuid.New(), ProviderCallID: "permission", Type: "built_in"},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 2)
	_, err = f.store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID: f.ids.ProjectID, AgentID: f.agentID, ID: calls[0].ID, RuntimeLockID: work.RuntimeLock.ID,
	})
	require.NoError(t, err)
	var form interactionform.Form
	require.NoError(t, json.Unmarshal(questionJSON, &form))
	question, err := f.store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
		ProjectID: f.ids.ProjectID, AgentID: f.agentID, ToolCallID: calls[0].ID, RuntimeLockID: work.RuntimeLock.ID,
	}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
		return executionstore.CreateQuestionForToolCall(
			executionstore.CreateQuestionInteractionInput{Form: form},
		), nil
	})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallDispositionWaiting, question.Disposition)
	questionRecord, ok := question.CommandResult.(executionstore.AgentInteractionRecord)
	require.True(t, ok)
	request, err := toolpermission.ParseRequest(permissionJSON)
	require.NoError(t, err)
	permission, err := f.store.Execution().CreatePermissionInteraction(ctx,
		executionstore.CreatePermissionInteractionInput{
			ProjectID: f.ids.ProjectID, AgentID: f.agentID, ToolCallID: calls[1].ID,
			RuntimeLockID: work.RuntimeLock.ID, Request: request,
		})
	require.NoError(t, err)
	for _, prompt := range []executionstore.AgentInteractionRecord{questionRecord, permission} {
		require.Equal(t, work.Model.TurnID, prompt.TurnID)
		require.Equal(t, executionstore.AgentInteractionStateOpen, prompt.State)
		destination, err := prompt.CapturedDestination()
		require.NoError(t, err)
		require.NotNil(t, destination)
		require.Equal(t, f.integrationID, destination.IntegrationID)
		require.Equal(t, f.targetID, destination.IntegrationTargetID)
		require.Equal(t, "slack", destination.HandlerKey)
		require.Equal(t, integrationstore.ConversationAddress{Kind: f.scope, Ref: f.ref}, destination.Address)
	}
}
