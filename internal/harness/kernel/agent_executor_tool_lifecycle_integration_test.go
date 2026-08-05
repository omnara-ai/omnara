//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type requeueingToolWorkExecutor struct {
	tools.Executor
	requeueProviderCallID string
	dispatches            map[string]int
}

func TestAgentExecutorDefaultsSkillStoreForPromptAndExecution(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	skill, err := fixture.Store.Skills().CreateSkillRevision(ctx, skillstore.CreateSkillInput{
		OrgID:          kernelTestOrgID,
		OwnerKind:      skillstore.SkillOwnerProject,
		OwnerProjectID: kernelTestProjectID,
		Name:           "release-checklist",
		Description:    "Verify a release before shipping.",
		SkillMd:        "# Release checklist\nConfirm the default skill store is available.",
		ArchiveBytes:   []byte("release checklist archive"),
		Actor:          kernelTestUserPrincipal(kernelTestUserID),
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	skillPublicID, err := publicid.Encode(publicid.KindSkill, skill.ID)
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Skill Defaults",
		"kernel-skill-defaults",
		fmt.Sprintf(`name: Kernel Skill Defaults
instruction: Use attached skills.
model:
  provider_config: openai-prod
  name: kernel-skill-defaults
skills:
  - %s
`, skillPublicID),
		fixture.Now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-skill-defaults-launch",
	})
	if err != nil {
		t.Fatalf("launch skill agent: %v", err)
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-skill-defaults",
		responses: []model.Response{{
			ID:         "resp_skill_defaults",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
				ID:    "call_skill_defaults",
				Name:  toolcatalog.ToolNameSkill,
				Input: json.RawMessage(`{"name":"release-checklist"}`),
			}}),
		}},
	}
	input := fixture.admitContentInputTurn(
		t,
		ctx,
		launch.Agent.ID,
		kernelTestUserID,
		"check the release",
		fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("execute skill-enabled model work: %v", err)
	}
	if modelClient.preparedCount() != 1 ||
		len(modelClient.prepared[0].ToolSpecs) != 1 ||
		modelClient.prepared[0].ToolSpecs[0].Name != toolcatalog.ToolNameSkill {
		t.Fatalf("skill-enabled prompt tools = %+v, want skill", modelClient.prepared)
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, input)
	<-scope.Done()
	if err := scope.Err(); err != nil {
		t.Fatalf("execute skill tool: %v", err)
	}
	completed, err := storagetest.ListCompletedToolCallsForTurn(
		ctx,
		fixture.Store,
		kernelTestProjectID,
		launch.Agent.ID,
		input.TurnID,
	)
	if err != nil {
		t.Fatalf("list completed skill calls: %v", err)
	}
	if len(completed) != 1 ||
		completed[0].ProviderCallID != "call_skill_defaults" ||
		completed[0].Outcome != executionstore.ToolResultOutcomeSucceeded ||
		!strings.Contains(string(completed[0].ResultContentParts), "Confirm the default skill store is available.") {
		t.Fatalf("completed skill calls = %+v, want successful skill content", completed)
	}
}

func (e *requeueingToolWorkExecutor) Dispatch(
	ctx context.Context,
	turn tools.Turn,
	call model.ToolCall,
) (tools.Result, error) {
	e.dispatches[call.ID]++
	if call.ID != e.requeueProviderCallID || e.dispatches[call.ID] > 1 {
		return e.Executor.Dispatch(ctx, turn, call)
	}
	record, found, err := e.Store.Execution().GetToolCallByProviderCall(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.ModelCallContextID,
		call.ID,
	)
	if err != nil {
		return tools.Result{}, err
	}
	if !found {
		return tools.Result{}, fmt.Errorf("tool call %q not found", call.ID)
	}
	if _, err := e.Store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     turn.ProjectID,
			AgentID:       turn.AgentID,
			ToolCallID:    record.ID,
			RuntimeLockID: turn.RuntimeLockID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		},
	); err != nil {
		return tools.Result{}, err
	}
	if err := e.Store.Execution().RequeueRuntimeToolCall(
		ctx,
		executionstore.RequeueRuntimeToolCallInput{
			ProjectID:     turn.ProjectID,
			AgentID:       turn.AgentID,
			ToolCallID:    record.ID,
			RuntimeLockID: turn.RuntimeLockID,
		},
	); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{
		ToolCallID:  call.ID,
		Name:        call.Name,
		Disposition: tools.DispatchDeferred,
	}, nil
}

func TestToolWorkDefersRequeuedAsyncCallToNextClaim(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(
		t,
		ctx,
		"openai/kernel-test",
		fixture.Now,
		"list_processes",
	)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{{
			ID:         "resp_requeued_tool_work",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{
					ID:    "call_requeued_between_dispatch_and_reload",
					Name:  "list_processes",
					Input: json.RawMessage(`{}`),
				},
				{
					ID:    "call_sibling_after_requeue",
					Name:  "list_processes",
					Input: json.RawMessage(`{}`),
				},
			}),
		}},
	}
	input := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise a preflight requeue",
		fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("persist tool work: %v", err)
	}
	work := nextToolWorkExecution(t, ctx, fixture, input)
	coordinator := &requeueingToolWorkExecutor{
		Executor:              tools.Executor{Store: fixture.Store},
		requeueProviderCallID: "call_requeued_between_dispatch_and_reload",
		dispatches:            make(map[string]int),
	}
	if err := executor.executeToolWork(ctx, work, coordinator); err != nil {
		t.Fatalf("execute tool work: %v", err)
	}
	if got := coordinator.dispatches["call_requeued_between_dispatch_and_reload"]; got != 1 {
		t.Fatalf("requeued tool dispatches = %d, want 1 in this claim", got)
	}
	if got := coordinator.dispatches["call_sibling_after_requeue"]; got != 1 {
		t.Fatalf("sibling tool dispatches = %d, want 1", got)
	}
	var requeuedState, siblingState string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT
  max(call.state) FILTER (
    WHERE call.provider_call_id = 'call_requeued_between_dispatch_and_reload'
  ),
  max(call.state) FILTER (
    WHERE call.provider_call_id = 'call_sibling_after_requeue'
  )
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.model_output_id = $3
`, input.ProjectID, input.AgentID, work.ModelOutputID).Scan(
		&requeuedState,
		&siblingState,
	); err != nil {
		t.Fatalf("load tool call states: %v", err)
	}
	if requeuedState != "ready" {
		t.Fatalf("requeued tool state = %q, want ready for the next claim", requeuedState)
	}
	if siblingState != "completed" {
		t.Fatalf("sibling tool state = %q, want completed", siblingState)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		input.ProjectID,
		input.AgentID,
		work.RuntimeLockID,
	); err != nil {
		t.Fatalf("release first tool-work claim: %v", err)
	}
	claim := claimNextAgentWorkForKernelTest(
		t,
		ctx,
		fixture,
		work.AgentID,
		executionstore.AgentWorkTool,
		work.Now.Add(time.Second),
	)
	if claim.ProjectID != work.ProjectID ||
		claim.AgentID != work.AgentID ||
		claim.Tool.TurnID != work.TurnID ||
		claim.Tool.ModelCallContextID != work.ModelCallContextID ||
		claim.Tool.ModelOutputID != work.ModelOutputID ||
		claim.Tool.SourceEventID != work.SourceEventID ||
		claim.RuntimeLock.ID == work.RuntimeLockID {
		t.Fatalf("next claim=%+v, want same tool frontier under a new runtime", claim)
	}
	nextWork := ToolWorkExecution{
		ProjectID:          claim.ProjectID,
		AgentID:            claim.AgentID,
		TurnID:             claim.Tool.TurnID,
		ModelCallContextID: claim.Tool.ModelCallContextID,
		ModelOutputID:      claim.Tool.ModelOutputID,
		SourceEventID:      claim.Tool.SourceEventID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		Now:                work.Now.Add(2 * time.Second),
	}
	if err := executor.executeToolWork(ctx, nextWork, coordinator); err != nil {
		t.Fatalf("execute next tool-work claim: %v", err)
	}
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		nextWork.ProjectID,
		nextWork.AgentID,
		nextWork.RuntimeLockID,
	); err != nil {
		t.Fatalf("release second tool-work claim: %v", err)
	}
	if got := coordinator.dispatches["call_requeued_between_dispatch_and_reload"]; got != 2 {
		t.Fatalf("requeued tool dispatches across claims = %d, want 2", got)
	}
	if got := coordinator.dispatches["call_sibling_after_requeue"]; got != 1 {
		t.Fatalf("sibling tool dispatches across claims = %d, want 1", got)
	}
	if err := fixture.Pool.QueryRow(ctx, `
SELECT call.state
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.provider_call_id = 'call_requeued_between_dispatch_and_reload'
`, input.ProjectID, input.AgentID).Scan(&requeuedState); err != nil {
		t.Fatalf("load requeued tool after next claim: %v", err)
	}
	if requeuedState != "completed" {
		t.Fatalf("requeued tool state after next claim = %q, want completed", requeuedState)
	}
}

func TestAgentExecutorStreamsTurnIDAndPreMintedToolCallIdentity(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now, "run_command")
	toolCall := model.ToolCall{
		ID:    "call_streamed_tool",
		Name:  "not_a_tool",
		Input: json.RawMessage(`{}`),
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		streamEvents: []model.StreamEvent{
			{
				Kind:       model.StreamEventBlockStart,
				BlockIndex: 0,
				Block: &model.StreamBlock{
					Kind:       model.StreamBlockToolUse,
					ToolCallID: toolCall.ID,
					ToolName:   toolCall.Name,
				},
			},
			{Kind: model.StreamEventBlockStop, BlockIndex: 0},
		},
		responses: []model.Response{
			{
				ID:         "resp_streamed_tool",
				StopReason: model.StopReasonToolUse,
				Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{toolCall}),
			},
			{
				ID:         "resp_streamed_final",
				Content:    []model.ResponsePart{{Type: "text", Text: "streamed tool continued"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise streamed tool identity",
		fixture.Now.Add(time.Second),
	)
	publisher := &capturingStreamPublisher{}
	executor := AgentExecutor{
		Store:           fixture.Store,
		ModelResolver:   liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:    tools.Executor{Store: fixture.Store},
		StreamPublisher: publisher,
		Now:             func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute turn: %v", err)
	}

	wantTurnID, err := publicid.Encode(publicid.KindAgentTurn, turn.TurnID)
	if err != nil {
		t.Fatalf("encode public turn id: %v", err)
	}
	envelopes := publisher.envelopes(t)
	if len(envelopes) == 0 {
		t.Fatal("expected published stream delta envelopes")
	}
	var framePublicToolCallID string
	for _, envelope := range envelopes {
		if envelope.TurnID != wantTurnID {
			t.Fatalf("envelope turn id = %q, want %q", envelope.TurnID, wantTurnID)
		}
		if envelope.Event.Block != nil && envelope.Event.Block.Kind == model.StreamBlockToolUse {
			framePublicToolCallID = envelope.Event.Block.ToolCallID
		}
	}
	if framePublicToolCallID == "" {
		t.Fatalf("no tool_use block_start frame published: %+v", envelopes)
	}
	frameToolCallID, err := publicid.Decode(publicid.KindToolCall, framePublicToolCallID)
	if err != nil {
		t.Fatalf("frame tool call id %q is not a public tool call id: %v", framePublicToolCallID, err)
	}
	var matched int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection
WHERE project_id = $1
  AND agent_id = $2
  AND provider_call_id = $3
  AND id = $4
`, kernelTestProjectID, agentID, toolCall.ID, frameToolCallID).Scan(&matched); err != nil {
		t.Fatalf("count pre-minted tool call rows: %v", err)
	}
	if matched != 1 {
		t.Fatalf(
			"tool_calls rows with pre-minted id %s = %d, want 1",
			frameToolCallID,
			matched,
		)
	}
}

func TestAgentExecutorCompletesInvalidToolCallsAndContinues(t *testing.T) {
	tests := []struct {
		name     string
		toolCall model.ToolCall
	}{
		{
			name: "unsupported",
			toolCall: model.ToolCall{
				ID:    "call_unsupported_tool",
				Name:  "not_a_tool",
				Input: json.RawMessage(`{}`),
			},
		},
		{
			name: "malformed",
			toolCall: model.ToolCall{
				ID:    "call_malformed_tool",
				Name:  "run_command",
				Input: json.RawMessage(`{}`),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newKernelFixture(t, ctx)
			agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now, "run_command")
			modelClient := &sequenceKernelModel{
				providerModelSlug: "kernel-test",
				responses: []model.Response{
					{
						ID:         "resp_" + tt.name + "_tool",
						StopReason: model.StopReasonToolUse,
						Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{tt.toolCall}),
					},
					{
						ID:         "resp_" + tt.name + "_final",
						Content:    []model.ResponsePart{{Type: "text", Text: tt.name + " tool result continued"}},
						StopReason: model.StopReasonEndTurn,
					},
				},
			}
			turn := fixture.admitContentInputTurn(
				t,
				ctx,
				agentID,
				userID,
				"exercise "+tt.name+" tool call",
				fixture.Now.Add(time.Second),
			)
			executor := AgentExecutor{
				Store:         fixture.Store,
				ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
				ToolExecutor:  tools.Executor{Store: fixture.Store},
				Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
			}
			if err := executor.ExecuteModelWork(ctx, turn); err != nil {
				t.Fatalf("execute turn: %v", err)
			}
			scope := executeNextToolWork(t, ctx, fixture, executor, turn)
			<-scope.Done()
			if err := scope.Err(); err != nil {
				t.Fatalf("execute invalid tool work: %v", err)
			}
			executeNextModelWork(t, ctx, fixture, executor, turn)
			if modelClient.preparedCount() != 2 {
				t.Fatalf("prepared %d requests, want invalid tool call plus continuation", modelClient.preparedCount())
			}
			var resultEvents int
			if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
JOIN tool_call_results result
  ON result.agent_id = call.agent_id
 AND result.tool_call_id = call.id
JOIN agent_events event
  ON event.agent_id = result.agent_id
 AND event.tool_call_result_id = result.id
JOIN content_blocks block
  ON block.agent_id = result.agent_id
 AND block.owner_tool_call_result_id = result.id
 AND block.block_kind = 'structured_data'
WHERE call.project_id = $1
	  AND call.agent_id = $2
	  AND call.provider_call_id = $3
	  AND call.state = 'completed'
	  AND result.outcome = 'failed'
	  AND coalesce(block.structured_data->>'error', '') <> ''
	  AND event.event_kind = 'tool_result'
`, kernelTestProjectID, agentID, tt.toolCall.ID).Scan(&resultEvents); err != nil {
				t.Fatalf("count invalid tool result events: %v", err)
			}
			if resultEvents != 1 {
				t.Fatalf("invalid tool result events = %d, want 1", resultEvents)
			}
			var finalOutputs int
			if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = $3
`, kernelTestProjectID, agentID, tt.name+" tool result continued").Scan(&finalOutputs); err != nil {
				t.Fatalf("count final output: %v", err)
			}
			if finalOutputs != 1 {
				t.Fatalf("final outputs = %d, want 1", finalOutputs)
			}
		})
	}
}

func TestAgentExecutorUsesConfigChangedDuringInFlightModelCallForNextRound(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now)
	configManager, err := storagetest.CreateVerifiedUser(
		ctx,
		fixture.Pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "kernel-config-manager@example.com",
			DisplayName: "Kernel Config Manager",
		},
	)
	if err != nil {
		t.Fatalf("create config manager: %v", err)
	}
	if _, err := fixture.Store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: kernelTestOrgID, UserID: configManager.ID, Role: "member"},
	); err != nil {
		t.Fatalf("add config manager org membership: %v", err)
	}
	if _, err := fixture.Store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     kernelTestOrgID,
			ProjectID: kernelTestProjectID,
			UserID:    configManager.ID,
			Role:      "developer",
		},
	); err != nil {
		t.Fatalf("add config manager project membership: %v", err)
	}
	nextConfig := fixture.kernelAgentConfigInput(t, ctx, "Kernel Test", "kernel-test-next")
	currentConfig := fixture.currentAgentConfig(t, ctx, agentID)
	currentRevisionID := currentRevisionIDForKernelConfig(t, ctx, fixture.Store, currentConfig)
	nextRevisionID := currentRevisionIDForKernelConfiguredModelID(t, ctx, fixture.Store, nextConfig.ConfiguredModelID)
	var changeResult executionstore.ChangeAgentConfigResult
	var changeErr error
	changed := false
	firstModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_inflight_config_tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
					ID:    "call_inflight_config_unsupported",
					Name:  toolcatalog.MCPRuntimeToolName("missing", "not_a_tool"),
					Input: json.RawMessage(`{}`),
				}}),
			},
		},
		afterRespond: func(model.Response) {
			if changed {
				return
			}
			changed = true
			changeResult, changeErr = fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
				CreateAgentConfigInput: nextConfig,
				AgentID:                agentID,
				ActorType:              identitystore.PrincipalTypeUser,
				ActorID:                configManager.ID,
				Reason:                 "user_update",
				IdempotencyKey:         "kernel-inflight-config-change",
			})
		},
	}
	secondModel := &sequenceKernelModel{
		providerModelSlug: "kernel-test-next",
		responses: []model.Response{
			{ID: "resp_inflight_config_final", Content: []model.ResponsePart{{Type: "text", Text: "continued with changed config"}}, StopReason: model.StopReasonEndTurn},
		},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"change config while first model call is in flight",
		fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{
		Store: fixture.Store,
		ModelResolver: staticTestModelResolver{Clients: []model.ResolvedClient{
			{
				Client:                    firstModel,
				ConfiguredModelRevisionID: currentRevisionID.String(),
			},
			{Client: secondModel, ConfiguredModelRevisionID: nextRevisionID.String()},
		}},
		ToolExecutor: tools.Executor{Store: fixture.Store},
		Now:          func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		if changeErr != nil {
			t.Fatalf("change config from in-flight hook: %v", changeErr)
		}
		t.Fatalf("execute turn: %v", err)
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, turn)
	<-scope.Done()
	if err := scope.Err(); err != nil {
		t.Fatalf("execute unsupported tool work: %v", err)
	}
	var unsupportedToolType string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT type
FROM tool_call_read_projection
WHERE project_id = $1
  AND agent_id = $2
  AND provider_call_id = 'call_inflight_config_unsupported'
`, kernelTestProjectID, agentID).Scan(&unsupportedToolType); err != nil {
		t.Fatalf("load unsupported mcp tool call: %v", err)
	}
	if unsupportedToolType != toolcatalog.ToolTypeMCP {
		t.Fatalf("unsupported mcp tool type = %q, want %q", unsupportedToolType, toolcatalog.ToolTypeMCP)
	}
	executeNextModelWork(t, ctx, fixture, executor, turn)
	if changeErr != nil {
		t.Fatalf("change config from in-flight hook: %v", changeErr)
	}
	if !changed {
		t.Fatal("model hook did not change the agent config")
	}
	if firstModel.preparedCount() != 1 || secondModel.preparedCount() != 1 {
		t.Fatalf("prepared counts first=%d second=%d, want 1/1", firstModel.preparedCount(), secondModel.preparedCount())
	}
	rows, err := fixture.Pool.Query(ctx, `
	SELECT context.input_event_sequence, provider_config.api_format, revision.provider_model_slug, context.agent_config_id
	FROM model_call_contexts context
	JOIN configured_model_revisions revision
	  ON revision.org_id = context.org_id
	 AND revision.id = context.configured_model_revision_id
	JOIN model_provider_configs provider_config
	  ON provider_config.org_id = revision.org_id
	 AND provider_config.id = revision.model_provider_config_id
	WHERE context.project_id = $1
  AND context.agent_id = $2
ORDER BY context.input_event_sequence, context.attempt_number
`, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("query model contexts: %v", err)
	}
	defer rows.Close()
	type contextConfig struct {
		sequence          int64
		apiFormat         string
		providerModelSlug string
		configID          storage.ID
	}
	var contexts []contextConfig
	for rows.Next() {
		var row contextConfig
		if err := rows.Scan(&row.sequence, &row.apiFormat, &row.providerModelSlug, &row.configID); err != nil {
			t.Fatalf("scan model context: %v", err)
		}
		contexts = append(contexts, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate model contexts: %v", err)
	}
	if len(contexts) != 2 {
		t.Fatalf("model contexts = %+v, want two rounds", contexts)
	}
	if contexts[0].sequence >= contexts[1].sequence || contexts[0].apiFormat != "openai-responses" ||
		contexts[0].providerModelSlug != "kernel-test" {
		t.Fatalf("first model context = %+v, want launch config provider model slug", contexts[0])
	}
	if contexts[1].apiFormat != "openai-responses" ||
		contexts[1].providerModelSlug != "kernel-test-next" {
		t.Fatalf("second model context = %+v, want changed config provider model slug", contexts[1])
	}
	if contexts[0].configID == changeResult.AgentConfig.ID {
		t.Fatalf("first model context used changed config: %+v change=%+v", contexts[0], changeResult)
	}
	if contexts[1].configID != changeResult.AgentConfig.ID {
		t.Fatalf("second model context config = %+v, want changed config %s", contexts[1], changeResult.AgentConfig.ID)
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = 'continued with changed config'
`, kernelTestProjectID, agentID).Scan(&finalOutputs); err != nil {
		t.Fatalf("count changed-config final output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("changed-config final outputs = %d, want 1", finalOutputs)
	}
}

func (f kernelFixture) kernelAgentConfigInput(
	t *testing.T,
	ctx context.Context,
	name, configuredModelName string,
) executionstore.CreateAgentConfigInput {
	t.Helper()
	sourceYAML := "name: " + name + "\ninstruction: Help the user make progress.\nmodel:\n  provider_config: openai-prod\n  name: " + configuredModelName + "\n"
	compiled := f.compileAgentYAMLResolved(t, ctx, sourceYAML, f.Now)
	return executionstore.CreateAgentConfigInput{
		ProjectID:               kernelTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	}
}

func TestAgentExecutorBlocksForCustomToolAndContinuesAfterResult(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	sourceYAML := `name: Kernel Custom Tool Test
instruction: Use the custom tool when needed.
model:
  provider_config: openai-prod
  name: kernel-test
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
  lookup_customer:
    type: custom
    permission:
      mode: always_ask
      parameters: {}
    description: Look up a customer by email.
    input_schema:
      type: object
      properties:
        email:
          type: string
      required: [email]
  notify_sales:
    type: custom
    permission:
      mode: always_allow
      parameters: {}
    description: Notify sales about a customer lookup.
    input_schema:
      type: object
`
	compiled := fixture.compileAgentYAMLResolved(t, ctx, sourceYAML, fixture.Now)
	config, err := fixture.Store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               kernelTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create custom tool config: %v", err)
	}
	agent, err := fixture.Store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       kernelTestProjectID,
		Name:            "Kernel Custom Tool Test",
		CurrentConfigID: config.ID,
		IdempotencyKey:  "kernel-custom-tool-agent",
	})
	if err != nil {
		t.Fatalf("create custom tool agent: %v", err)
	}
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      agent.ID,
		AgentConfigID:  agent.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-custom-tool-launch",
	})
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	runtimeAgent := launch.Agent
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_custom_tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
					{
						ID:    "call_lookup_customer",
						Name:  "lookup_customer",
						Input: json.RawMessage(`{"email":"ada@example.com"}`),
					},
					{
						ID:    "call_notify_sales",
						Name:  "notify_sales",
						Input: json.RawMessage(`{}`),
					},
					{
						ID:    "call_run_command",
						Name:  "run_command",
						Input: json.RawMessage(`{}`),
					},
				}),
			},
			{
				ID:         "resp_custom_tool_final",
				Content:    []model.ResponsePart{{Type: "text", Text: "customer lookup finished"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		runtimeAgent.ID,
		kernelTestUserID,
		"look up Ada",
		fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("persist custom tool output: %v", err)
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, turn)
	<-scope.Done()
	if err := scope.Err(); err != nil {
		t.Fatalf("execute custom tool work: %v", err)
	}
	var callID storage.ID
	var callName, callType, callState string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT call.id, call.name, call.type, call.state
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.provider_call_id = 'call_lookup_customer'
	`, kernelTestProjectID, runtimeAgent.ID).Scan(
		&callID,
		&callName,
		&callType,
		&callState,
	); err != nil {
		t.Fatalf("load custom tool call: %v", err)
	}
	if callName != "lookup_customer" || callType != toolcatalog.ToolTypeCustom ||
		callState != string(executionstore.ToolCallStateAwaitingPermission) {
		t.Fatalf(
			"custom call name=%q type=%q state=%q",
			callName,
			callType,
			callState,
		)
	}
	var fallbackCallID storage.ID
	var fallbackName, fallbackType, fallbackState string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT call.id, call.name, call.type, call.state
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.provider_call_id = 'call_notify_sales'
	`, kernelTestProjectID, runtimeAgent.ID).Scan(
		&fallbackCallID,
		&fallbackName,
		&fallbackType,
		&fallbackState,
	); err != nil {
		t.Fatalf("load fallback custom tool call: %v", err)
	}
	if fallbackName != "notify_sales" || fallbackType != toolcatalog.ToolTypeCustom ||
		fallbackState != string(executionstore.ToolCallStateReady) {
		t.Fatalf(
			"fallback custom call name=%q type=%q state=%q",
			fallbackName,
			fallbackType,
			fallbackState,
		)
	}
	var builtInOrdinal int
	var builtInState string
	if err := fixture.Pool.QueryRow(ctx, `
SELECT coalesce(block.ordinal, -1), call.state
FROM tool_call_read_projection call
LEFT JOIN content_blocks block
  ON block.agent_id = call.agent_id
 AND block.tool_call_id = call.id
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.provider_call_id = 'call_run_command'
	`, kernelTestProjectID, runtimeAgent.ID).Scan(&builtInOrdinal, &builtInState); err != nil {
		t.Fatalf("load built-in tool call after custom call: %v", err)
	}
	if builtInOrdinal != 2 || builtInState != "completed" {
		t.Fatalf("built-in call ordinal=%d state=%q, want ordinal 2 and completed", builtInOrdinal, builtInState)
	}
	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		kernelTestProjectID,
		runtimeAgent.ID,
		callID,
		"permission",
	)
	if err != nil {
		t.Fatalf("load custom tool permission interaction: %v", err)
	}
	if !found {
		t.Fatal("custom tool permission interaction was not created")
	}
	resolvedBy, err := executionstore.OmnaraActorParams(kernelTestOrgID, kernelTestUserPrincipal(kernelTestUserID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: kernelTestProjectID,
		AgentID:   runtimeAgent.ID,
		ID:        interaction.ID,
		Resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
			OptionIndices: []int{toolpermission.AllowOptionIndex},
		}}},
		Actor: resolvedBy,
	}); err != nil {
		t.Fatalf("allow custom tool: %v", err)
	}
	allowedCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		kernelTestProjectID,
		runtimeAgent.ID,
		callID,
	)
	if err != nil {
		t.Fatalf("load allowed custom tool call: %v", err)
	}
	if allowedCall.State != executionstore.ToolCallStateReady {
		t.Fatalf(
			"allowed custom call state=%q, want ready",
			allowedCall.State,
		)
	}
	fallbackCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		kernelTestProjectID,
		runtimeAgent.ID,
		fallbackCallID,
	)
	if err != nil {
		t.Fatalf("load activated fallback custom tool call: %v", err)
	}
	if fallbackCall.State != executionstore.ToolCallStateReady {
		t.Fatalf("fallback custom tool call = %+v, want ready", fallbackCall)
	}
	if _, err := fixture.Store.Execution().CompleteCustomToolCall(ctx, executionstore.CompleteCustomToolCallInput{
		ProjectID:     kernelTestProjectID,
		AgentID:       runtimeAgent.ID,
		ID:            callID,
		Outcome:       executionstore.ToolResultOutcomeSucceeded,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Customer found."}]`),
	}); err != nil {
		t.Fatalf("complete custom tool call: %v", err)
	}
	if _, err := fixture.Store.Execution().CompleteCustomToolCall(ctx, executionstore.CompleteCustomToolCallInput{
		ProjectID:     kernelTestProjectID,
		AgentID:       runtimeAgent.ID,
		ID:            fallbackCallID,
		Outcome:       executionstore.ToolResultOutcomeSucceeded,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Sales notified."}]`),
	}); err != nil {
		t.Fatalf("complete fallback custom tool call: %v", err)
	}
	executeNextModelWork(t, ctx, fixture, executor, turn)
	if modelClient.preparedCount() != 2 {
		t.Fatalf("prepared %d requests, want initial custom call plus continuation", modelClient.preparedCount())
	}
	var finalOutputs int
	if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_events event
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = 'customer lookup finished'
	`, kernelTestProjectID, runtimeAgent.ID).Scan(&finalOutputs); err != nil {
		t.Fatalf("count final output: %v", err)
	}
	if finalOutputs != 1 {
		t.Fatalf("final outputs = %d, want 1", finalOutputs)
	}
}

func TestAgentExecutorPersistsInterleavedToolCallOrdinals(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/kernel-test", fixture.Now, "run_command")
	firstCallID := "call_run_first"
	secondCallID := "call_run_second"
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-test",
		responses: []model.Response{
			{
				ID:         "resp_interleaved_tool_calls",
				StopReason: model.StopReasonToolUse,
				Content: []model.ResponsePart{
					{Type: "text", Text: "thinking before first call"},
					{
						Type:           "tool_call",
						ProviderCallID: firstCallID,
						ToolName:       "run_command",
						ToolInput:      json.RawMessage(`{"command":"first"}`),
					},
					{Type: "text", Text: "thinking between calls"},
					{
						Type:           "tool_call",
						ProviderCallID: secondCallID,
						ToolName:       "run_command",
						ToolInput:      json.RawMessage(`{"command":"second"}`),
					},
				},
			},
			{
				ID:         "resp_interleaved_final",
				Content:    []model.ResponsePart{{Type: "text", Text: "interleaved tool calls done"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	turn := fixture.admitContentInputTurn(
		t,
		ctx,
		agentID,
		userID,
		"exercise interleaved tool ordinals",
		fixture.Now.Add(time.Second),
	)
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return fixture.Now.Add(2 * time.Second) },
	}
	if err := executor.ExecuteModelWork(ctx, turn); err != nil {
		t.Fatalf("execute interleaved turn: %v", err)
	}
	rows, err := fixture.Pool.Query(ctx, `
SELECT block.ordinal, block.block_kind, coalesce(block.text_content, ''), coalesce(call.provider_call_id, '')
FROM agent_events event
JOIN content_blocks block
  ON block.agent_id = event.agent_id
 AND block.owner_model_output_id = event.model_output_id
LEFT JOIN tool_calls call
  ON call.agent_id = block.agent_id
 AND call.id = block.tool_call_id
JOIN agents agent ON agent.id = event.agent_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
ORDER BY block.ordinal ASC
`, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("query interleaved blocks: %v", err)
	}
	defer rows.Close()
	type blockRow struct {
		Ordinal        int32
		BlockKind      string
		TextContent    string
		ProviderCallID string
	}
	var got []blockRow
	for rows.Next() {
		var b blockRow
		if err := rows.Scan(&b.Ordinal, &b.BlockKind, &b.TextContent, &b.ProviderCallID); err != nil {
			t.Fatalf("scan interleaved block: %v", err)
		}
		got = append(got, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate interleaved blocks: %v", err)
	}
	want := []blockRow{
		{Ordinal: 0, BlockKind: "text", TextContent: "thinking before first call"},
		{Ordinal: 1, BlockKind: "tool_call", ProviderCallID: firstCallID},
		{Ordinal: 2, BlockKind: "text", TextContent: "thinking between calls"},
		{Ordinal: 3, BlockKind: "tool_call", ProviderCallID: secondCallID},
	}
	if len(got) != len(want) {
		t.Fatalf("interleaved blocks = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("block[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
