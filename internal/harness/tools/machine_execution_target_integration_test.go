//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var (
	toolsTestOrgID     = toolsTestID("org")
	toolsTestProjectID = toolsTestID("project")
	toolsTestWorkerID  = toolsTestID("worker_process_instance")
)

func toolsTestClaimInput() executionstore.ClaimNextAgentWorkInput {
	return executionstore.ClaimNextAgentWorkInput{
		WorkerProcessID: toolsTestWorkerID,
		LeaseDuration:   executionstore.AgentRuntimeLockLeaseDuration,
	}
}

func toolsTestID(seed string) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-tools-integration:"+seed))
}

type toolsTestMachinePoolProviders struct{}

func (toolsTestMachinePoolProviders) ResolveMachineProviderOptions(
	_ string,
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	var merged map[string]json.RawMessage
	for _, overlay := range []map[string]json.RawMessage{
		defaultOptions,
		projectOptions,
		agentOptions,
	} {
		if overlay != nil && merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range overlay {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged, nil
}

func (toolsTestMachinePoolProviders) ValidatePool(
	_ string,
	_ executionstore.MachinePoolProviderPolicy,
) error {
	return nil
}

func (providers toolsTestMachinePoolProviders) BuildMachineProvisioningIntent(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := providers.ValidatePool(provider, policy); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func TestResolveMachineExecutionTargetUsesMachineRefSelection(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	)
	now := time.Date(2026, 6, 5, 9, 0, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: "tools-target@example.com", DisplayName: "Tools Target"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at) VALUES ($1, 'Tools Test Org', 'tools-test-org', $2, $2)`,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, 'Tools Test Project', 'tools-test-project', $3, $3)`,
		toolsTestProjectID,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)

	firstBinding := createExecutableBinding(t, ctx, store, user.ID, "first", now.Add(time.Second))
	secondBinding := createExecutableBinding(t, ctx, store, user.ID, "second", now.Add(2*time.Second))
	launch := createToolsRuntimeAgentWithMachineSources(
		t,
		ctx,
		store,
		user.ID,
		"tools-target",
		[]toolsAgentMachineSource{
			{MachineName: firstBinding.DisplayName, Cwd: "/first"},
			{MachineName: secondBinding.DisplayName, Cwd: "/second"},
		},
		now.Add(3*time.Second),
	)
	if len(launch.MachineBindings) != 2 {
		t.Fatalf("machine bindings = %+v, want two", launch.MachineBindings)
	}
	agent := launch.Agent
	firstAgentBinding := launch.MachineBindings[0]
	secondAgentBinding := launch.MachineBindings[1]

	executor := Executor{Store: store}
	turn := Turn{ProjectID: toolsTestProjectID, AgentID: agent.ID}
	if _, err := executor.ResolveMachineExecutionTarget(ctx, turn, ""); !errors.Is(err, ErrMachineSelectionRequired) {
		t.Fatalf("omitted selector with multiple bindings error = %v, want %v", err, ErrMachineSelectionRequired)
	}
	binding, err := executor.ResolveMachineExecutionTarget(ctx, turn, firstAgentBinding.MachineRef)
	if err != nil {
		t.Fatalf("resolve explicit first binding: %v", err)
	}
	if binding.ID != firstAgentBinding.ID || binding.Cwd != "/first" {
		t.Fatalf("resolved wrong explicit binding: %+v want %s", binding, firstAgentBinding.ID)
	}
	if _, err := executor.ResolveMachineExecutionTarget(ctx, turn, "mchr-missing"); !errors.Is(
		err,
		ErrMachineRefUnavailable,
	) {
		t.Fatalf("unavailable selector error = %v, want %v", err, ErrMachineRefUnavailable)
	}
	singleLaunch := createToolsRuntimeAgentWithMachineSources(
		t,
		ctx,
		store,
		user.ID,
		"tools-target-single",
		[]toolsAgentMachineSource{{MachineName: firstBinding.DisplayName, Cwd: "/only"}},
		now.Add(6*time.Second),
	)
	if len(singleLaunch.MachineBindings) != 1 {
		t.Fatalf("single machine bindings = %+v, want one", singleLaunch.MachineBindings)
	}
	singleAgent := singleLaunch.Agent
	singleBinding := singleLaunch.MachineBindings[0]
	binding, err = executor.ResolveMachineExecutionTarget(
		ctx,
		Turn{ProjectID: toolsTestProjectID, AgentID: singleAgent.ID},
		"",
	)
	if err != nil {
		t.Fatalf("resolve omitted single binding: %v", err)
	}
	if binding.ID != singleBinding.ID || binding.MachineRef == "" {
		t.Fatalf("resolved wrong single binding: %+v want %s", binding, singleBinding.ID)
	}
	_ = secondAgentBinding
}

func TestMachineObservationToolsIncludeAttachedBYOMachine(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchFixture(t, ctx, "observe-byo")
	byo := createExecutableBinding(
		t,
		ctx,
		fixture.Store,
		fixture.UserID,
		"observe-byo",
		fixture.Now.Add(5*time.Second),
	)
	machineRef := "mchr-byo001"
	if _, err := fixture.Pool.Exec(
		ctx,
		`INSERT INTO agent_machine_bindings(
		   org_id, project_id, agent_id, machine_id, machine_ref, binding_kind, state,
		   description, cwd, created_at, updated_at
		 )
		 VALUES ($1, $2, $3, $4, $5, 'explicit', 'attached', $6, $7, $8, $8)`,
		toolsTestOrgID,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		byo.MachineID,
		machineRef,
		"local checkout",
		"/checkout",
		fixture.Now.Add(6*time.Second),
	); err != nil {
		t.Fatalf("attach BYO machine: %v", err)
	}

	listCall := model.ToolCall{ID: "call_observe-byo-list", Name: "list_machines", Input: json.RawMessage(`{}`)}
	inspectCall := model.ToolCall{ID: "call_observe-byo-inspect", Name: "inspect_machine", Input: json.RawMessage(`{}`)}
	mixedInspectCall := model.ToolCall{
		ID:    "call_observe-byo-mixed-inspect",
		Name:  "inspect_machine",
		Input: json.RawMessage(`{}`),
	}
	createPoolCall := model.ToolCall{ID: "call_observe-byo-create-pool", Name: "create_machine", Input: json.RawMessage(`{}`)}
	toolCalls, lock, admitted, contextRecord := recordMachineToolCallsForDirectStoreTest(
		t,
		ctx,
		fixture.Store,
		fixture.Launch.Agent.ID,
		fixture.UserID,
		fixture.Config.ID,
		"observe-byo",
		[]model.ToolCall{listCall, inspectCall, mixedInspectCall, createPoolCall},
		fixture.Now.Add(7*time.Second),
	)
	for _, index := range []int{0, 3} {
		if _, err := fixture.Store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     toolsTestProjectID,
				AgentID:       fixture.Launch.Agent.ID,
				ID:            toolCalls[index].ID,
				RuntimeLockID: lock.ID,
			},
		); err != nil {
			t.Fatalf("allow %s: %v", toolCalls[index].ProviderCallID, err)
		}
	}
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            fixture.Launch.Agent.ID,
		SourceEventID:      admitted.Events[0].ID,
		RuntimeLockID:      lock.ID,
		ModelCallContextID: contextRecord.ID,
		Tools: map[string]ToolSpec{
			"list_machines": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
			"inspect_machine": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
			},
		},
	}
	executor := Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(8 * time.Second) },
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, inspectCall); err != nil {
		t.Fatalf("prepare inspect_machine permission: %v", err)
	}

	listResult, err := executor.Dispatch(ctx, turn, listCall)
	if err != nil {
		t.Fatalf("dispatch list_machines: %v", err)
	}
	listBody := toolResultMapFromTestParts(t, listResult.ContentParts)
	machines, ok := listBody["machines"].([]any)
	if !ok || len(machines) != 1 {
		t.Fatalf("list_machines result = %+v, want one machine", listBody)
	}
	listed, ok := machines[0].(map[string]any)
	if !ok {
		t.Fatalf("list_machines entry = %+v", machines[0])
	}
	assertBYOMachineObservationResult(t, listed, machineRef, byo.DisplayName)

	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCalls[1].ID,
		"permission",
	)
	if err != nil {
		t.Fatalf("load inspect_machine permission: %v", err)
	}
	if !found {
		t.Fatal("inspect_machine permission interaction not found")
	}
	resolvedBy, err := executionstore.OmnaraActorParams(
		toolsTestOrgID,
		toolsTestUserPrincipal(fixture.UserID),
	)
	if err != nil {
		t.Fatalf("build inspect_machine permission actor: %v", err)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: toolsTestProjectID,
			AgentID:   fixture.Launch.Agent.ID,
			ID:        interaction.ID,
			Resolution: interactionform.Resolution{
				Answers: []interactionform.Answer{{
					OptionIndices: []int{toolpermission.AllowOptionIndex},
				}},
			},
			Actor: resolvedBy,
		},
	); err != nil {
		t.Fatalf("approve inspect_machine permission: %v", err)
	}
	executor.Now = func() time.Time { return fixture.Now.Add(9 * time.Second) }
	inspectResult, err := executor.Dispatch(ctx, turn, inspectCall)
	if err != nil {
		t.Fatalf("dispatch inspect_machine: %v", err)
	}
	inspected := toolResultMapFromTestParts(t, inspectResult.ContentParts)
	assertBYOMachineObservationResult(t, inspected, machineRef, byo.DisplayName)

	if _, err := storagetest.ExecuteToolCallCommand[executionstore.CreatePoolMachineResult](
		ctx,
		fixture.Store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       fixture.Launch.Agent.ID,
			ToolCallID:    toolCalls[3].ID,
			RuntimeLockID: lock.ID,
		},
		executionstore.CreatePoolMachineForToolCall(
			executionstore.CreatePoolMachineInput{MachinePoolID: fixture.MachinePool.ID},
			func(executionstore.CreatePoolMachineResult) (executionstore.ToolCallCompletionInput, error) {
				return executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"accepted"}]`),
				}, nil
			},
		),
	); err != nil {
		t.Fatalf("create pool machine for mixed observation: %v", err)
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, mixedInspectCall); err != nil {
		t.Fatalf("prepare mixed-source inspect_machine permission: %v", err)
	}
	mixedResult, err := executor.Dispatch(ctx, turn, mixedInspectCall)
	if err != nil {
		t.Fatalf("dispatch mixed-source inspect_machine: %v", err)
	}
	mixedBody := toolResultMapFromTestParts(t, mixedResult.ContentParts)
	if mixedBody["error_code"] != ErrMachineSelectionRequired.Error() ||
		mixedBody["error"] != "machine_ref is required when multiple machines are available" {
		t.Fatalf("mixed-source inspect_machine result = %+v", mixedBody)
	}
}

func assertBYOMachineObservationResult(
	t *testing.T,
	result map[string]any,
	machineRef, displayName string,
) {
	t.Helper()
	if result["machine_ref"] != machineRef || result["source_kind"] != "byo" ||
		result["binding_kind"] != "explicit" || result["binding_state"] != "attached" ||
		result["display_name"] != displayName || result["cwd"] != "/checkout" ||
		result["connection_state"] != "online" || result["executable"] != true {
		t.Fatalf("BYO machine observation = %+v", result)
	}
	if _, found := result["machine_pool_name"]; found {
		t.Fatalf("BYO machine observation includes machine_pool_name: %+v", result)
	}
}

func TestApprovedImplicitMachineTargetChangeFailsTerminally(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchFixture(t, ctx, "approved-machine-target-change")
	first := createExecutableBinding(
		t,
		ctx,
		fixture.Store,
		fixture.UserID,
		"approved-target-first",
		fixture.Now.Add(5*time.Second),
	)
	second := createExecutableBinding(
		t,
		ctx,
		fixture.Store,
		fixture.UserID,
		"approved-target-second",
		fixture.Now.Add(6*time.Second),
	)
	firstRef := "mchr-aprvd1"
	secondRef := "mchr-aprvd2"
	if _, err := fixture.Pool.Exec(
		ctx,
		`INSERT INTO agent_machine_bindings(
		   org_id, project_id, agent_id, machine_id, machine_ref, binding_kind, state, created_at, updated_at
		 )
		 VALUES ($1, $2, $3, $4, $5, 'explicit', 'attached', $6, $6)`,
		toolsTestOrgID,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		first.MachineID,
		firstRef,
		fixture.Now.Add(7*time.Second),
	); err != nil {
		t.Fatalf("attach first machine: %v", err)
	}

	toolCallID, lock, admitted, contextRecord := recordMachineToolCallForDirectStoreTest(
		t,
		ctx,
		fixture.Store,
		fixture.Launch.Agent.ID,
		fixture.UserID,
		fixture.Config.ID,
		"approved-machine-target-change",
		"run_command",
		json.RawMessage(`{"command":"pwd"}`),
		fixture.Now.Add(8*time.Second),
	)
	call := model.ToolCall{
		ID:    "call_approved-machine-target-change",
		Name:  "run_command",
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            fixture.Launch.Agent.ID,
		SourceEventID:      admitted.Events[0].ID,
		RuntimeLockID:      lock.ID,
		ModelCallContextID: contextRecord.ID,
		Tools: map[string]ToolSpec{
			"run_command": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
			},
		},
	}
	executor := Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(9 * time.Second) },
	}
	if err := executor.PrepareToolCallPermission(ctx, turn, call); err != nil {
		t.Fatalf("prepare run permission: %v", err)
	}
	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
		"permission",
	)
	if err != nil {
		t.Fatalf("list run permission: %v", err)
	}
	if !found {
		t.Fatal("run permission interaction not found")
	}
	resolution := interactionform.Resolution{
		Answers: []interactionform.Answer{{
			OptionIndices: []int{toolpermission.AllowOptionIndex},
		}},
	}
	resolvedBy, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(fixture.UserID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	if _, err := fixture.Store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID:  toolsTestProjectID,
			AgentID:    fixture.Launch.Agent.ID,
			ID:         interaction.ID,
			Resolution: resolution,
			Actor:      resolvedBy,
		},
	); err != nil {
		t.Fatalf("approve run permission: %v", err)
	}
	if _, err := fixture.Pool.Exec(
		ctx,
		`UPDATE agent_machine_bindings
		 SET state = 'released', updated_at = $1
		 WHERE project_id = $2 AND agent_id = $3 AND machine_id = $4`,
		fixture.Now.Add(11*time.Second),
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		first.MachineID,
	); err != nil {
		t.Fatalf("release approved machine target: %v", err)
	}
	if _, err := fixture.Pool.Exec(
		ctx,
		`INSERT INTO agent_machine_bindings(
		   org_id, project_id, agent_id, machine_id, machine_ref, binding_kind, state, created_at, updated_at
		 )
		 VALUES ($1, $2, $3, $4, $5, 'explicit', 'attached', $6, $6)`,
		toolsTestOrgID,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		second.MachineID,
		secondRef,
		fixture.Now.Add(11*time.Second),
	); err != nil {
		t.Fatalf("attach replacement machine target: %v", err)
	}

	executor.Now = func() time.Time { return fixture.Now.Add(12 * time.Second) }
	result, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("dispatch after approved target change: %v", err)
	}
	var parts []struct {
		Value struct {
			ErrorCode string `json:"error_code"`
		} `json:"value"`
	}
	if err := json.Unmarshal(result.ContentParts, &parts); err != nil {
		t.Fatalf("decode invalidated authorization result: %v", err)
	}
	if len(parts) != 1 ||
		parts[0].Value.ErrorCode != ErrToolAuthorizationInvalidated.Error() {
		t.Fatalf("invalidated authorization result = %s", result.ContentParts)
	}
	record, err := fixture.Store.Execution().GetToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load invalidated tool call: %v", err)
	}
	var retainedRuntimeOwnership bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT runtime_lock_id IS NOT NULL FROM tool_calls WHERE id = $1`,
		toolCallID,
	).Scan(&retainedRuntimeOwnership); err != nil {
		t.Fatalf("load invalidated tool ownership: %v", err)
	}
	if record.State != "completed" || retainedRuntimeOwnership {
		t.Fatalf(
			"invalidated tool call state=%q retained_runtime_ownership=%t, want terminal without ownership",
			record.State,
			retainedRuntimeOwnership,
		)
	}
}

func TestProcessToolMachineSelectionFailureKeepsStructuredPayload(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	)
	now := time.Date(2026, 6, 5, 9, 30, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: "tools-selection@example.com", DisplayName: "Tools Selection"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at) VALUES ($1, 'Tools Test Org', 'tools-test-org-selection', $2, $2)`,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, 'Tools Test Project', 'tools-test-project-selection', $3, $3)`,
		toolsTestProjectID,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)
	firstBinding := createExecutableBinding(t, ctx, store, user.ID, "selection-first", now.Add(time.Second))
	secondBinding := createExecutableBinding(t, ctx, store, user.ID, "selection-second", now.Add(2*time.Second))
	launch := createToolsRuntimeAgentWithMachineSources(
		t,
		ctx,
		store,
		user.ID,
		"tools-selection",
		[]toolsAgentMachineSource{
			{MachineName: firstBinding.DisplayName},
			{MachineName: secondBinding.DisplayName},
		},
		now.Add(3*time.Second),
	)
	if len(launch.MachineBindings) != 2 {
		t.Fatalf("machine bindings = %+v, want two", launch.MachineBindings)
	}
	agent := launch.Agent

	producer, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(user.ID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      toolsTestProjectID,
			AgentID:        agent.ID,
			Actor:          producer,
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"run pwd"}]`),
			IdempotencyKey: "tools-selection-input",
		},
	)
	if err != nil {
		t.Fatalf("create agent input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel {
		t.Fatal("expected input admission")
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		toolsTestProjectID,
		agent.ID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	modelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		agent.ID,
		lock,
		[]storage.ID{input.ID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
		storage.NilID,
	)
	contextRecord := modelCall.Context
	call := model.ToolCall{ID: "call_selection", Name: "run_command", Input: json.RawMessage(`{"command":"pwd"}`)}
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"tools-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_selection",
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{call}),
		},
	)
	if err != nil {
		t.Fatalf("provider response: %v", err)
	}
	_, records, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            agent.ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{
					ProviderCallID: call.ID,
					Type:           toolcatalog.ToolTypeBuiltIn,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("record tool call source: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded tool calls = %d, want 1", len(records))
	}
	if _, err := store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       agent.ID,
			ID:            records[0].ID,
			RuntimeLockID: lock.ID,
		},
	); err != nil {
		t.Fatalf("mark permission allowed: %v", err)
	}
	result, err := (Executor{Store: store, Now: func() time.Time { return now.Add(13 * time.Second) }}).Dispatch(
		ctx,
		Turn{
			ProjectID:          toolsTestProjectID,
			AgentID:            agent.ID,
			SourceEventID:      admitted.Events[0].ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
		},
		call,
	)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	completed, err := store.Execution().GetToolCall(ctx, toolsTestProjectID, agent.ID, records[0].ID)
	if err != nil {
		t.Fatalf("load completed tool call: %v", err)
	}
	if completed.State != executionstore.ToolCallStateCompleted ||
		completed.Outcome != executionstore.ToolResultOutcomeFailed {
		t.Fatalf("tool call = %+v, want failed completion", completed)
	}
	var resultContent any
	if err := json.Unmarshal(result.ContentParts, &resultContent); err != nil {
		t.Fatalf("decode dispatch result parts: %v; raw=%s", err, result.ContentParts)
	}
	var completedContent any
	if err := json.Unmarshal(completed.ResultContentParts, &completedContent); err != nil {
		t.Fatalf("decode completed result parts: %v; raw=%s", err, completed.ResultContentParts)
	}
	if !reflect.DeepEqual(resultContent, completedContent) {
		t.Fatalf("dispatch result parts = %s, completed parts = %s", result.ContentParts, completed.ResultContentParts)
	}
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(result.ContentParts, &parts); err != nil {
		t.Fatalf("decode result content parts: %v; raw=%s", err, result.ContentParts)
	}
	if len(parts) != 1 || parts[0].Type != "structured_data" {
		t.Fatalf("unexpected result content parts: %+v", parts)
	}
	var body map[string]any
	if unmarshalErr := json.Unmarshal(parts[0].Value, &body); unmarshalErr != nil {
		t.Fatalf("decode result content: %v; raw=%s", unmarshalErr, parts[0].Value)
	}
	if body["error_code"] != ErrMachineSelectionRequired.Error() ||
		body["error"] != "machine_ref is required when multiple machines are available" {
		t.Fatalf("unexpected structured machine selection result: %+v", body)
	}
}

func TestCreateMachineCompletesWithDurableProvisioningIntent(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchFixture(t, ctx, "create-boundary")
	call := model.ToolCall{ID: "call_create-boundary", Name: "create_machine", Input: json.RawMessage(`{}`)}
	badCall := model.ToolCall{
		ID:    "call_create-boundary-invalid-pool",
		Name:  "create_machine",
		Input: json.RawMessage(`{"machine_pool_name":"missing"}`),
	}
	toolCalls, lock, admitted, contextRecord := createMachineToolCallsForDirectStoreTest(
		t,
		ctx,
		fixture.Store,
		fixture.Launch.Agent.ID,
		fixture.UserID,
		fixture.Config.ID,
		"create-boundary",
		[]model.ToolCall{call, badCall},
		fixture.Now.Add(5*time.Second),
	)
	toolCallID := toolCalls[0].ID
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            fixture.Launch.Agent.ID,
		SourceEventID:      admitted.Events[0].ID,
		RuntimeLockID:      lock.ID,
		ModelCallContextID: contextRecord.ID,
		Tools: map[string]ToolSpec{
			"create_machine": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
		},
	}
	provisionCalls := make(chan storage.ID, 1)
	manager := testPoolMachineManager{
		provision: func(ctx context.Context, orgID, machineID storage.ID) error {
			if orgID != toolsTestOrgID {
				return errors.New("provision manager received the wrong organization")
			}
			record, err := fixture.Store.Execution().GetPoolMachineByCreateToolCall(
				ctx,
				toolsTestProjectID,
				fixture.Launch.Agent.ID,
				toolCallID,
			)
			if err != nil {
				return err
			}
			if record.Machine.ID != machineID || record.Machine.LifecycleState != "provisioning" {
				return errors.New("machine intent was not committed before reconciliation")
			}
			var completedWithoutRuntime bool
			if err := fixture.Pool.QueryRow(
				ctx,
				`SELECT state = 'completed' AND runtime_lock_id IS NULL FROM tool_calls WHERE id = $1`,
				toolCallID,
			).Scan(&completedWithoutRuntime); err != nil {
				return err
			}
			if !completedWithoutRuntime {
				return errors.New("reconciliation started before transactional tool completion")
			}
			provisionCalls <- machineID
			return nil
		},
	}
	backgroundRunner, err := NewBackgroundExecutionRunner(ctx, nil, 1)
	if err != nil {
		t.Fatalf("new background runner: %v", err)
	}
	defer backgroundRunner.Shutdown()
	executor := Executor{
		Store:              fixture.Store,
		MachinePoolManager: manager,
		BackgroundRunner:   backgroundRunner,
		Now:                func() time.Time { return fixture.Now.Add(6 * time.Second) },
	}
	result, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("dispatch create machine: %v", err)
	}
	if result.Disposition != DispatchCompleted {
		t.Fatal("create machine retained runtime ownership for reconciliation")
	}
	body := toolResultMapFromTestParts(t, result.ContentParts)
	if body["lifecycle_state"] != "provisioning" ||
		body["connection_state"] != "offline" || body["ready"] != false {
		t.Fatalf("create machine result = %+v", body)
	}
	machine, err := fixture.Store.Execution().GetPoolMachineByCreateToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load provisioning machine: %v", err)
	}
	if machine.Machine.LifecycleState != "provisioning" || machine.Machine.ProvisionAttempts != 0 {
		t.Fatalf("machine intent = %+v", machine.Machine)
	}
	select {
	case machineID := <-provisionCalls:
		if machineID != machine.Machine.ID {
			t.Fatalf("provision machine = %s, want %s", machineID, machine.Machine.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create machine did not start background reconciliation")
	}

	badResult, err := executor.Dispatch(ctx, turn, badCall)
	if err != nil {
		t.Fatalf("dispatch invalid create machine: %v", err)
	}
	if badResult.Disposition != DispatchCompleted {
		t.Fatal("transactionally rejected create machine started async execution")
	}
	select {
	case <-provisionCalls:
		t.Fatal("transactionally rejected create machine invoked manager")
	default:
	}
}

func TestManagedWorkAdmissionProducesDurableToolFailures(t *testing.T) {
	t.Run("create machine", func(t *testing.T) {
		ctx := context.Background()
		fixture := newManagedMachineDispatchFixture(t, ctx, "managed-create-denied")
		call := model.ToolCall{
			ID:    "call_managed-create-denied",
			Name:  "create_machine",
			Input: json.RawMessage(`{}`),
		}
		toolCalls, lock, admitted, contextRecord := createMachineToolCallsForDirectStoreTest(
			t,
			ctx,
			fixture.Store,
			fixture.Launch.Agent.ID,
			fixture.UserID,
			fixture.Config.ID,
			"managed-create-denied",
			[]model.ToolCall{call},
			fixture.Now.Add(5*time.Second),
		)
		closeManagedWorkAdmissionForToolsTest(t, ctx, fixture.Pool)
		result, err := (Executor{
			Store: fixture.Store,
			Now:   func() time.Time { return fixture.Now.Add(6 * time.Second) },
		}).Dispatch(ctx, Turn{
			ProjectID:          toolsTestProjectID,
			AgentID:            fixture.Launch.Agent.ID,
			SourceEventID:      admitted.Events[0].ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
			Tools: map[string]ToolSpec{
				"create_machine": {
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				},
			},
		}, call)
		if err != nil {
			t.Fatalf("dispatch denied create_machine: %v", err)
		}
		assertManagedWorkAdmissionToolFailure(
			t,
			ctx,
			fixture,
			toolCalls[0].ID,
			result,
		)
		var machines int
		if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM machines
WHERE org_id = $1 AND machine_pool_id = $2 AND deleted_at IS NULL
`, toolsTestOrgID, fixture.MachinePool.ID).Scan(&machines); err != nil {
			t.Fatalf("count machines after denied create_machine: %v", err)
		}
		if machines != 0 {
			t.Fatalf("machines after denied create_machine = %d, want 0", machines)
		}
	})

	t.Run("run command", func(t *testing.T) {
		ctx := context.Background()
		fixture := newManagedMachineDispatchFixture(t, ctx, "managed-run-denied")
		createCall := model.ToolCall{
			ID:    "call_managed-run-machine",
			Name:  "create_machine",
			Input: json.RawMessage(`{}`),
		}
		runCall := model.ToolCall{
			ID:    "call_managed-run-denied",
			Name:  "run_command",
			Input: json.RawMessage(`{"command":"true"}`),
		}
		toolCalls, lock, admitted, contextRecord := createMachineToolCallsForDirectStoreTest(
			t,
			ctx,
			fixture.Store,
			fixture.Launch.Agent.ID,
			fixture.UserID,
			fixture.Config.ID,
			"managed-run-denied",
			[]model.ToolCall{createCall, runCall},
			fixture.Now.Add(5*time.Second),
		)
		turn := Turn{
			ProjectID:          toolsTestProjectID,
			AgentID:            fixture.Launch.Agent.ID,
			SourceEventID:      admitted.Events[0].ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
			Tools: map[string]ToolSpec{
				"create_machine": {
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				},
				"run_command": {
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				},
			},
		}
		executor := Executor{
			Store: fixture.Store,
			Now:   func() time.Time { return fixture.Now.Add(6 * time.Second) },
		}
		if _, err := executor.Dispatch(ctx, turn, createCall); err != nil {
			t.Fatalf("dispatch admitted create_machine: %v", err)
		}
		closeManagedWorkAdmissionForToolsTest(t, ctx, fixture.Pool)
		activateProvisioningMachineForToolsTest(
			t,
			ctx,
			fixture,
			toolCalls[0].ID,
			"managed-run-denied",
		)
		result, err := executor.Dispatch(ctx, turn, runCall)
		if err != nil {
			t.Fatalf("dispatch denied run_command: %v", err)
		}
		assertManagedWorkAdmissionToolFailure(
			t,
			ctx,
			fixture,
			toolCalls[1].ID,
			result,
		)
		var processes int
		if err := fixture.Pool.QueryRow(ctx, `
SELECT count(*)::integer
FROM processes
WHERE project_id = $1 AND agent_id = $2
`, toolsTestProjectID, fixture.Launch.Agent.ID).Scan(&processes); err != nil {
			t.Fatalf("count processes after denied run_command: %v", err)
		}
		if processes != 0 {
			t.Fatalf("processes after denied run_command = %d, want 0", processes)
		}
	})
}

func closeManagedWorkAdmissionForToolsTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO org_managed_work_admission(org_id, new_managed_work_allowed)
VALUES ($1, false)
`, toolsTestOrgID); err != nil {
		t.Fatalf("close managed work admission: %v", err)
	}
}

func activateProvisioningMachineForToolsTest(
	t *testing.T,
	ctx context.Context,
	fixture machineDispatchFixture,
	toolCallID storage.ID,
	label string,
) {
	t.Helper()
	provisioning, err := fixture.Store.Execution().GetPoolMachineByCreateToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load admitted provisioning machine: %v", err)
	}
	claim, found, err := fixture.Store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		toolsTestOrgID,
		provisioning.Machine.ID,
	)
	if err != nil || !found {
		t.Fatalf("claim admitted provisioning machine found=%v err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().AdmitPoolMachineProvisioning(
		ctx,
		executionstore.AdmitPoolMachineProvisioningInput{
			OrgID:            toolsTestOrgID,
			MachineID:        claim.Machine.ID,
			MachinePoolID:    claim.Machine.MachinePoolID,
			ProvisionAttempt: claim.Machine.ProvisionAttempts,
			Facts: executionstore.MachineResourceFacts{
				CPU:      claim.Machine.CPU,
				MemoryMB: claim.Machine.MemoryMB,
			},
		},
	); err != nil {
		t.Fatalf("admit claimed machine provisioning: %v", err)
	}
	start, err := fixture.Store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            toolsTestOrgID,
			MachineID:        claim.Machine.ID,
			ProvisionAttempt: claim.Machine.ProvisionAttempts,
			TokenName:        "tools managed admission",
		},
	)
	if err != nil {
		t.Fatalf("begin admitted provider provisioning: %v", err)
	}
	providerResourceID := "provider-" + label
	if _, err := fixture.Store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              toolsTestOrgID,
			MachineID:          claim.Machine.ID,
			ProviderResourceID: providerResourceID,
			ProvisionAttempt:   claim.Machine.ProvisionAttempts,
		},
	); err != nil {
		t.Fatalf("record admitted provider resource: %v", err)
	}
	if err := fixture.Store.Execution().CompletePoolMachineProvisioning(
		ctx,
		toolsTestOrgID,
		claim.Machine.ID,
		providerResourceID,
		"",
		claim.Machine.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete admitted provider provisioning: %v", err)
	}
	if _, err := fixture.Store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            toolsTestOrgID,
			MachineID:        claim.Machine.ID,
			DaemonTokenID:    start.DaemonToken.Record.ID,
			DaemonInstanceID: toolsTestID("daemon-" + label),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	); err != nil {
		t.Fatalf("register admitted provisioning machine runtime: %v", err)
	}
	active, err := fixture.Store.Execution().GetPoolMachineByCreateToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("reload admitted provisioning machine: %v", err)
	}
	if active.Machine.LifecycleState != executionstore.MachineLifecycleStateActive ||
		active.Machine.ConnectionState != executionstore.MachineConnectionStateOnline {
		t.Fatalf("admitted provisioning machine = %+v, want active and online", active.Machine)
	}
}

func assertManagedWorkAdmissionToolFailure(
	t *testing.T,
	ctx context.Context,
	fixture machineDispatchFixture,
	toolCallID storage.ID,
	result Result,
) {
	t.Helper()
	if result.Disposition != DispatchCompleted {
		t.Fatalf("denied tool disposition = %d, want completed", result.Disposition)
	}
	body := toolResultMapFromTestParts(t, result.ContentParts)
	if body["error_code"] != storeerr.ManagedWorkAdmissionDeniedCode ||
		body["error"] != managedWorkAdmissionDeniedMessage ||
		body["message"] != managedWorkAdmissionDeniedMessage ||
		body["retryable"] != false {
		t.Fatalf("denied tool result = %+v", body)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load denied tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted ||
		toolCall.Outcome != executionstore.ToolResultOutcomeFailed ||
		toolCall.RuntimeLockID != storage.NilID ||
		!reflect.DeepEqual(
			toolResultMapFromTestParts(t, toolCall.ResultContentParts),
			body,
		) {
		t.Fatalf("durable denied tool call = %+v", toolCall)
	}
}

func TestMissedMachineBackgroundProvisioningCanBeReconciled(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchFixture(t, ctx, "provisioning-interruption")
	toolCallID, lock, admitted, contextRecord := createMachineToolCallForDirectStoreTest(
		t,
		ctx,
		fixture.Store,
		fixture.Launch.Agent.ID,
		fixture.UserID,
		fixture.Config.ID,
		"provisioning-interruption",
		"create_machine",
		json.RawMessage(`{}`),
		fixture.Now.Add(5*time.Second),
	)
	call := model.ToolCall{
		ID:    "call_provisioning-interruption",
		Name:  "create_machine",
		Input: json.RawMessage(`{}`),
	}
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            fixture.Launch.Agent.ID,
		SourceEventID:      admitted.Events[0].ID,
		RuntimeLockID:      lock.ID,
		ModelCallContextID: contextRecord.ID,
		Tools: map[string]ToolSpec{
			"create_machine": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
		},
	}
	executor := Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(6 * time.Second) },
	}
	result, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("dispatch create machine: %v", err)
	}
	if result.Disposition != DispatchCompleted {
		t.Fatal("create machine retained runtime ownership for reconciliation")
	}
	provisioning, err := fixture.Store.Execution().GetPoolMachineByCreateToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load provisioning machine: %v", err)
	}
	claim, ok, err := fixture.Store.Execution().ClaimPoolMachineForProvisioning(
		ctx,
		provisioning.Machine.OrgID,
		provisioning.Machine.ID,
	)
	if err != nil || !ok {
		t.Fatalf("claim machine after missed background dispatch ok=%v err=%v", ok, err)
	}
	claimed := claim.Machine
	if _, err := fixture.Store.Execution().BeginPoolMachineProviderProvisioning(
		ctx,
		executionstore.BeginPoolMachineProviderProvisioningInput{
			OrgID:            claimed.OrgID,
			MachineID:        claimed.ID,
			ProvisionAttempt: claimed.ProvisionAttempts,
			TokenName:        "test reconciliation",
		},
	); err != nil {
		t.Fatalf("begin provider reconciliation: %v", err)
	}
	if _, err := fixture.Store.Execution().RecordPoolMachineProvisioningResource(
		ctx,
		executionstore.RecordPoolMachineProvisioningResourceInput{
			OrgID:              claimed.OrgID,
			MachineID:          claimed.ID,
			ProviderResourceID: "provider-after-reconciliation",
			ProvisionAttempt:   claimed.ProvisionAttempts,
		},
	); err != nil {
		t.Fatalf("record provider resource after reconciliation: %v", err)
	}
	if err := fixture.Store.Execution().CompletePoolMachineProvisioning(
		ctx,
		claimed.OrgID,
		claimed.ID,
		"provider-after-reconciliation",
		"",
		claimed.ProvisionAttempts,
	); err != nil {
		t.Fatalf("complete machine provisioning after missed background dispatch: %v", err)
	}
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load accepted create tool call: %v", err)
	}
	accepted := toolResultMapFromTestParts(t, toolCall.ResultContentParts)
	if toolCall.State != "completed" ||
		accepted["lifecycle_state"] != "provisioning" || accepted["ready"] != false {
		t.Fatalf("accepted create tool call = %+v result=%+v", toolCall, accepted)
	}
	machine, err := fixture.Store.Execution().GetPoolMachineByCreateToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Launch.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load provisioned machine by tool call: %v", err)
	}
	if machine.Machine.LifecycleState != "active" ||
		machine.Machine.ProviderResourceID != "provider-after-reconciliation" {
		t.Fatalf("machine after reconciliation = %+v", machine.Machine)
	}
}

func TestMissedMachineBackgroundDeletionCanBeReconciled(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchFixture(t, ctx, "runtime-interruption")
	pool := fixture.Pool
	store := fixture.Store
	now := fixture.Now
	launch := fixture.Launch
	config := fixture.Config
	machinePool := fixture.MachinePool
	userID := fixture.UserID
	var err error
	createToolCallID, lock, admitted, createContext := createMachineToolCallForDirectStoreTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		userID,
		config.ID,
		"delete-replay-create",
		"create_machine",
		json.RawMessage(`{}`),
		now.Add(5500*time.Millisecond),
	)
	created, err := storagetest.ExecuteToolCallCommand[executionstore.CreatePoolMachineResult](
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       launch.Agent.ID,
			ToolCallID:    createToolCallID,
			RuntimeLockID: lock.ID,
		},
		executionstore.CreatePoolMachineForToolCall(
			executionstore.CreatePoolMachineInput{
				MachinePoolID: machinePool.ID,
			},
			func(executionstore.CreatePoolMachineResult) (executionstore.ToolCallCompletionInput, error) {
				return executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"accepted"}]`),
				}, nil
			},
		),
	)
	if err != nil {
		t.Fatalf("create pool machine: %v", err)
	}
	continuationFrontier, err := store.Execution().MaxEventSequence(
		ctx,
		toolsTestProjectID,
		launch.Agent.ID,
	)
	if err != nil {
		t.Fatalf("load delete continuation frontier: %v", err)
	}
	modelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		launch.Agent.ID,
		lock,
		[]storage.ID{admitted.Inputs[0].ID},
		config.ID,
		continuationFrontier,
		createContext.ID,
	)
	contextRecord := modelCall.Context
	call := model.ToolCall{
		ID:    "call_delete_replay",
		Name:  "delete_machine",
		Input: json.RawMessage(`{"machine_ref":"` + created.Machine.Binding.MachineRef + `"}`),
	}
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"tools-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_delete_replay",
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{call}),
		},
	)
	if err != nil {
		t.Fatalf("build delete provider response: %v", err)
	}
	_, records, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            launch.Agent.ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{
					ProviderCallID: call.ID,
					Type:           toolcatalog.ToolTypeBuiltIn,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("record delete tool call: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("recorded delete tool calls = %d, want 1", len(records))
	}
	if _, err := store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       launch.Agent.ID,
			ID:            records[0].ID,
			RuntimeLockID: lock.ID,
		},
	); err != nil {
		t.Fatalf("mark permission allowed: %v", err)
	}
	turn := Turn{
		ProjectID:          toolsTestProjectID,
		AgentID:            launch.Agent.ID,
		SourceEventID:      admitted.Events[0].ID,
		RuntimeLockID:      lock.ID,
		ModelCallContextID: contextRecord.ID,
		Tools: map[string]ToolSpec{
			"delete_machine": {
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			},
		},
	}
	executor := Executor{
		Store: store,
		Now:   func() time.Time { return now.Add(12 * time.Second) },
	}
	dispatchResult, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("dispatch delete machine: %v", err)
	}
	if dispatchResult.Disposition != DispatchCompleted {
		t.Fatal("delete machine retained runtime ownership for reconciliation")
	}
	deleted, err := store.Execution().GetPoolMachineByDeleteToolCall(
		ctx,
		toolsTestProjectID,
		launch.Agent.ID,
		records[0].ID,
	)
	if err != nil {
		t.Fatalf("load deleting machine: %v", err)
	}
	var completedWithoutRuntime bool
	if err := pool.QueryRow(
		ctx,
		`SELECT state = 'completed' AND runtime_lock_id IS NULL FROM tool_calls WHERE id = $1`,
		records[0].ID,
	).Scan(&completedWithoutRuntime); err != nil {
		t.Fatalf("load delete tool ownership: %v", err)
	}
	if !completedWithoutRuntime {
		t.Fatal("delete manager started before transactional tool completion")
	}
	deleting, ok, err := store.Execution().ClaimPoolMachineDeletion(
		ctx,
		executionstore.MachineDeletingInput{
			OrgID:                    toolsTestOrgID,
			MachineID:                created.Machine.Machine.ID,
			LifecycleReasonCode:      "machine_tool_delete",
			LifecycleReasonMessage:   "deleted by machine tool",
			ExpectedLifecycleVersion: deleted.Machine.LifecycleVersion,
		},
	)
	if err != nil || !ok {
		t.Fatalf("claim machine deletion after missed background dispatch ok=%v err=%v", ok, err)
	}
	if err := store.Execution().CompletePoolMachineDeletion(
		ctx,
		toolsTestOrgID,
		created.Machine.Machine.ID,
		deleting.Machine.DeleteAttempts,
	); err != nil {
		t.Fatalf("complete pool machine deletion after missed background dispatch: %v", err)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, toolsTestProjectID, launch.Agent.ID, records[0].ID)
	if err != nil {
		t.Fatalf("load accepted delete tool call: %v", err)
	}
	if toolCall.State != "completed" {
		t.Fatalf("interrupted delete tool call = %+v", toolCall)
	}
	accepted := toolResultMapFromTestParts(t, toolCall.ResultContentParts)
	if accepted["lifecycle_state"] != "deleting" ||
		accepted["deletion_in_progress"] != true || accepted["deleted"] != false {
		t.Fatalf("accepted delete result = %+v", accepted)
	}
	machine, err := store.Execution().GetPoolMachineByDeleteToolCall(
		ctx,
		toolsTestProjectID,
		launch.Agent.ID,
		records[0].ID,
	)
	if err != nil {
		t.Fatalf("load deleted machine by tool call: %v", err)
	}
	if machine.Machine.LifecycleState != "deleted" {
		t.Fatalf("machine lifecycle after reconciliation = %q, want deleted", machine.Machine.LifecycleState)
	}
	result, err := executor.Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("read accepted delete result: %v", err)
	}
	var parts []struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(result.ContentParts, &parts); err != nil {
		t.Fatalf("decode replayed delete result parts: %v; raw=%s", err, result.ContentParts)
	}
	if len(parts) != 1 || parts[0].Type != "structured_data" {
		t.Fatalf("unexpected replayed delete result parts: %+v", parts)
	}
	var body map[string]any
	if err := json.Unmarshal(parts[0].Value, &body); err != nil {
		t.Fatalf("decode replayed delete result body: %v; raw=%s", err, parts[0].Value)
	}
	if body["lifecycle_state"] != "deleting" ||
		body["deletion_in_progress"] != true || body["deleted"] != false {
		t.Fatalf("unexpected accepted delete result body: %+v", body)
	}
}

func TestReadProcessAfterTerminalWakesAsleepMachine(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	)
	now := time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{Email: "tools-replay@example.com", DisplayName: "Tools Replay"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at) VALUES ($1, 'Tools Test Org', 'tools-test-org-replay', $2, $2)`,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at) VALUES ($1, $2, 'Tools Test Project', 'tools-test-project-replay', $3, $3)`,
		toolsTestProjectID,
		toolsTestOrgID,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)
	binding := createExecutableBinding(t, ctx, store, user.ID, "replay", now.Add(time.Second))
	launch := createToolsRuntimeAgentWithMachineSources(
		t,
		ctx,
		store,
		user.ID,
		"tools-replay",
		[]toolsAgentMachineSource{{MachineName: binding.DisplayName, Cwd: "/replay"}},
		now.Add(2*time.Second),
	)
	if len(launch.MachineBindings) != 1 {
		t.Fatalf("machine bindings = %+v, want one", launch.MachineBindings)
	}
	agent := launch.Agent
	agentBinding := launch.MachineBindings[0]
	producer, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(user.ID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      toolsTestProjectID,
			AgentID:        agent.ID,
			Actor:          producer,
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"read process"}]`),
			IdempotencyKey: "tools-replay-input",
		},
	)
	if err != nil {
		t.Fatalf("create agent input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel {
		t.Fatal("expected input admission")
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		toolsTestProjectID,
		agent.ID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture config snapshot: %v", err)
	}
	runModelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		agent.ID,
		lock,
		[]storage.ID{input.ID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
		storage.NilID,
	)
	contextRecord := runModelCall.Context
	runCall := model.ToolCall{
		ID:    "call_replay_run",
		Name:  "run_command",
		Input: json.RawMessage(`{"command":"printf done"}`),
	}
	runProviderResponse, err := model.NewResponseEnvelopeForStorage(
		"tools-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_replay_run",
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{runCall}),
		},
	)
	if err != nil {
		t.Fatalf("run provider response: %v", err)
	}
	_, runRecords, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            agent.ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
			ProviderResponse:   runProviderResponse,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{
					ProviderCallID: runCall.ID,
					Type:           toolcatalog.ToolTypeBuiltIn,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("record run tool call: %v", err)
	}
	if len(runRecords) != 1 {
		t.Fatalf("recorded run tool calls = %d, want 1", len(runRecords))
	}
	if _, err := store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       agent.ID,
			ID:            runRecords[0].ID,
			RuntimeLockID: lock.ID,
		},
	); err != nil {
		t.Fatalf("mark run permission allowed: %v", err)
	}
	process, err := storagetest.StartProcessForToolCall(
		ctx,
		store,
		executionstore.ExecuteToolCallInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       agent.ID,
			ToolCallID:    runRecords[0].ID,
			RuntimeLockID: lock.ID,
		},
		executionstore.CreateProcessInput{
			AgentMachineBindingID: agentBinding.ID,
			IOMode:                "pipe",
			Command:               "printf done",
			ShellSelector:         "sh",
			Cwd:                   "/replay",
		},
	)
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	accepted, found, err := store.Execution().AcceptDaemonProcess(
		ctx,
		executionstore.AcceptDaemonProcessInput{
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           toolsTestOrgID,
				MachineID:       binding.MachineID,
				DaemonRuntimeID: binding.RuntimeID,
				DaemonTokenID:   binding.DaemonTokenID,
			},
			ProcessID: process.ID,
		},
	)
	if err != nil {
		t.Fatalf("accept process: %v", err)
	}
	if !found {
		t.Fatal("expected process offer")
	}
	if _, err := store.Execution().MarkProcessStarted(
		ctx,
		executionstore.MarkProcessStartedInput{
			ProjectID: toolsTestProjectID,
			AgentID:   agent.ID,
			ID:        accepted.Process.ID,
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           toolsTestOrgID,
				MachineID:       binding.MachineID,
				DaemonRuntimeID: binding.RuntimeID,
				DaemonTokenID:   binding.DaemonTokenID,
			},
			SourceStartedAt: now.Add(12 * time.Second),
		},
	); err != nil {
		t.Fatalf("mark process started: %v", err)
	}
	endedAt := now.Add(13 * time.Second)
	exitCode := 0
	if _, err := store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID: toolsTestProjectID,
			AgentID:   agent.ID,
			ID:        accepted.Process.ID,
			Authority: executionstore.DaemonRuntimeAuthority{
				OrgID:           toolsTestOrgID,
				MachineID:       binding.MachineID,
				DaemonRuntimeID: binding.RuntimeID,
				DaemonTokenID:   binding.DaemonTokenID,
			},
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			Result:        json.RawMessage(`{"output":"done"}`),
			SourceEndedAt: endedAt,
		},
	); err != nil {
		t.Fatalf("complete daemon process: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE machines SET sandbox_url = 'https://tools-terminal-read.test/' WHERE org_id = $1 AND id = $2`,
		toolsTestOrgID,
		binding.MachineID,
	); err != nil {
		t.Fatalf("set machine sandbox url: %v", err)
	}
	if _, err := store.Execution().SleepDaemonRuntime(ctx, executionstore.DaemonRuntimeAuthority{
		OrgID:           toolsTestOrgID,
		MachineID:       binding.MachineID,
		DaemonRuntimeID: binding.RuntimeID,
		DaemonTokenID:   binding.DaemonTokenID,
	}); err != nil {
		t.Fatalf("sleep daemon runtime: %v", err)
	}
	processHandle, err := encodeProcessID(accepted.Process.ID)
	if err != nil {
		t.Fatalf("encode process id: %v", err)
	}
	readFrontier, err := store.Execution().MaxEventSequence(ctx, toolsTestProjectID, agent.ID)
	if err != nil {
		t.Fatalf("load read continuation frontier: %v", err)
	}
	readModelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		agent.ID,
		lock,
		[]storage.ID{input.ID},
		snapshot.AgentConfig.ID,
		readFrontier,
		contextRecord.ID,
	)
	readContextRecord := readModelCall.Context
	readCall := model.ToolCall{
		ID:    "call_replay_read",
		Name:  "read_process",
		Input: json.RawMessage(`{"process_id":"` + processHandle + `","cursor":0,"max_bytes":4096}`),
	}
	readProviderResponse, err := model.NewResponseEnvelopeForStorage(
		"tools-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_replay_read",
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls([]model.ToolCall{readCall}),
		},
	)
	if err != nil {
		t.Fatalf("read provider response: %v", err)
	}
	_, readRecords, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            agent.ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: readContextRecord.ID,
			ProviderResponse:   readProviderResponse,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{
					ProviderCallID: readCall.ID,
					Type:           toolcatalog.ToolTypeBuiltIn,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("record read tool call: %v", err)
	}
	if len(readRecords) != 1 {
		t.Fatalf("recorded read tool calls = %d, want 1", len(readRecords))
	}
	if _, err := store.Execution().MarkToolCallReady(
		ctx,
		executionstore.MarkToolCallReadyInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       agent.ID,
			ID:            readRecords[0].ID,
			RuntimeLockID: lock.ID,
		},
	); err != nil {
		t.Fatalf("mark read permission allowed: %v", err)
	}
	wakeCalls := 0
	manager := testPoolMachineManager{wake: func(
		_ context.Context,
		orgID, machineID storage.ID,
	) (bool, error) {
		if orgID != toolsTestOrgID || machineID != binding.MachineID {
			return false, storeerr.ErrNotFound
		}
		wakeCalls++
		return true, nil
	}}
	result, err := (Executor{
		Store:              store,
		MachinePoolManager: manager,
		BackgroundRunner:   immediateIntegrationBackgroundRunner(ctx),
		Now:                func() time.Time { return endedAt.Add(time.Second) },
	}).Dispatch(
		ctx,
		Turn{
			ProjectID:          toolsTestProjectID,
			AgentID:            agent.ID,
			SourceEventID:      admitted.Events[0].ID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: readContextRecord.ID,
			Tools: map[string]ToolSpec{
				"read_process": {
					Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
				},
			},
		},
		readCall,
	)
	if err != nil {
		t.Fatalf("dispatch read action after process terminal: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf(
			"terminal read disposition = %d, want deferred; content=%s",
			result.Disposition,
			result.ContentParts,
		)
	}
	if wakeCalls != 1 {
		t.Fatalf("wake calls = %d, want 1", wakeCalls)
	}
	action, found, err := store.Execution().GetProcessActionByToolCall(
		ctx,
		toolsTestProjectID,
		agent.ID,
		readRecords[0].ID,
	)
	if err != nil {
		t.Fatalf("load terminal read action: %v", err)
	}
	if !found ||
		action.ActionKind != executionstore.ProcessActionKindRead ||
		action.State != executionstore.ProcessActionStateQueued {
		t.Fatalf("terminal read action found=%t action=%+v", found, action)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, toolsTestProjectID, agent.ID, readRecords[0].ID)
	if err != nil {
		t.Fatalf("load read tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("read tool call state=%q, want waiting", toolCall.State)
	}
	wakeErr := errors.New("wake failed")
	manager.wake = func(context.Context, storage.ID, storage.ID) (bool, error) {
		return false, wakeErr
	}
	cleanupErr := wakeReadProcess(ctx, backgroundToolContext{
		Executor: Executor{
			Store:              store,
			MachinePoolManager: manager,
		},
		Turn:       Turn{ProjectID: toolsTestProjectID, AgentID: agent.ID},
		Call:       readCall,
		ToolCallID: readRecords[0].ID,
	})
	if !errors.Is(cleanupErr, wakeErr) {
		t.Fatalf("read wake cleanup error = %v, want wake failure", cleanupErr)
	}
	action, found, err = store.Execution().GetProcessActionByToolCall(
		ctx,
		toolsTestProjectID,
		agent.ID,
		readRecords[0].ID,
	)
	if err != nil {
		t.Fatalf("load failed terminal read action: %v", err)
	}
	if !found || action.State != executionstore.ProcessActionStateFailed ||
		action.StateReasonCode != executionstore.ProcessToolReasonMachineUnreachable {
		t.Fatalf("failed terminal read action found=%t action=%+v", found, action)
	}
	toolCall, err = store.Execution().GetToolCall(ctx, toolsTestProjectID, agent.ID, readRecords[0].ID)
	if err != nil {
		t.Fatalf("load failed read tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted {
		t.Fatalf("failed read tool call state=%q, want completed", toolCall.State)
	}
}

type machineDispatchFixture struct {
	Pool        *pgxpool.Pool
	Store       *storage.Store
	UserID      storage.ID
	MachinePool executionstore.MachinePoolRecord
	Config      executionstore.AgentConfigRecord
	Launch      executionstore.LaunchAgentResult
	Now         time.Time
}

type testPoolMachineManager struct {
	provision func(context.Context, storage.ID, storage.ID) error
	delete    func(context.Context, executionstore.PoolMachineCleanupCandidate) error
	wake      func(context.Context, storage.ID, storage.ID) (bool, error)
}

func (m testPoolMachineManager) ProvisionMachine(
	ctx context.Context,
	orgID, machineID storage.ID,
) error {
	if m.provision == nil {
		return nil
	}
	return m.provision(ctx, orgID, machineID)
}

func (m testPoolMachineManager) DeleteMachine(
	ctx context.Context,
	candidate executionstore.PoolMachineCleanupCandidate,
) error {
	if m.delete == nil {
		return nil
	}
	return m.delete(ctx, candidate)
}

func (m testPoolMachineManager) WakeMachine(
	ctx context.Context,
	orgID, machineID storage.ID,
) (bool, error) {
	if m.wake == nil {
		return false, nil
	}
	return m.wake(ctx, orgID, machineID)
}

func newMachineDispatchFixture(
	t *testing.T,
	ctx context.Context,
	label string,
) machineDispatchFixture {
	t.Helper()
	return newMachineDispatchFixtureWithManagement(t, ctx, label, management.Tenant)
}

func newManagedMachineDispatchFixture(
	t *testing.T,
	ctx context.Context,
	label string,
) machineDispatchFixture {
	t.Helper()
	return newMachineDispatchFixtureWithManagement(t, ctx, label, management.Cluster)
}

func newMachineDispatchFixtureWithManagement(
	t *testing.T,
	ctx context.Context,
	label string,
	managementKind management.Kind,
) machineDispatchFixture {
	t.Helper()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationToolKeyWrapper(t)),
		storage.WithMachinePoolProviders(toolsTestMachinePoolProviders{}),
	)
	now := time.Date(2026, 6, 5, 9, 45, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(
		ctx,
		pool,
		storagetest.CreateVerifiedUserInput{
			Email:       "tools-machine-dispatch-" + label + "@example.com",
			DisplayName: "Tools Machine Dispatch",
		},
	)
	if err != nil {
		t.Fatalf("create machine dispatch user: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
VALUES ($1, 'Tools Test Org', $2, $3, $3)`,
		toolsTestOrgID,
		"tools-machine-dispatch-org-"+label,
		now,
	); err != nil {
		t.Fatalf("seed machine dispatch org: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Tools Test Project', $3, $4, $4)`,
		toolsTestProjectID,
		toolsTestOrgID,
		"tools-machine-dispatch-project-"+label,
		now,
	); err != nil {
		t.Fatalf("seed machine dispatch project: %v", err)
	}
	ensureToolsProjectOperator(t, ctx, store, user.ID, now)
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: toolsTestOrgID, UserID: user.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("promote machine dispatch org membership: %v", err)
	}
	machinePoolName := "Machine Dispatch Pool " + label
	var machinePool executionstore.MachinePoolRecord
	if managementKind == management.Cluster {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin managed machine dispatch pool provisioning: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := store.Execution().ProvisionOrganizationDefaultsTx(
			ctx,
			tx,
			toolsTestOrgID,
			toolsTestProjectID,
			[]executionstore.DefaultMachinePoolTemplate{{
				Name:                          machinePoolName,
				Provider:                      "test.provider",
				DefaultMachineCPU:             intPtrForToolsTest(1),
				DefaultMachineMemoryMB:        intPtrForToolsTest(1024),
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"machine-dispatch"}`),
				ProviderAuthEnvVar:            "TOOLS_TEST_PROVIDER_TOKEN",
				MaxTotalMachines:              2,
				MaxTotalCPU:                   intPtrForToolsTest(2),
				MaxTotalMemoryMB:              intPtrForToolsTest(2048),
				MaxMachineCPU:                 intPtrForToolsTest(1),
				MaxMachineMemoryMB:            intPtrForToolsTest(1024),
			}},
		); err != nil {
			t.Fatalf("provision managed machine dispatch pool: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit managed machine dispatch pool: %v", err)
		}
		var machinePoolID storage.ID
		if err := pool.QueryRow(ctx, `
SELECT id
FROM machine_pools
WHERE org_id = $1 AND name = $2 AND deleted_at IS NULL
`, toolsTestOrgID, machinePoolName).Scan(&machinePoolID); err != nil {
			t.Fatalf("load managed machine dispatch pool id: %v", err)
		}
		machinePool, err = store.Execution().GetMachinePool(ctx, toolsTestOrgID, machinePoolID)
		if err != nil {
			t.Fatalf("load managed machine dispatch pool: %v", err)
		}
	} else {
		providerAuthSecret, _, err := store.Secrets().CreateSecret(
			ctx,
			secretstore.CreateSecretInput{
				OrgID:     toolsTestOrgID,
				OwnerKind: secretstore.SecretOwnerOrg,
				Name:      "tools-machine-dispatch-provider-auth-" + label,
				Material:  secrets.GenericMaterial{Value: "test-token"},
				Actor:     toolsTestUserPrincipal(user.ID),
			},
		)
		if err != nil {
			t.Fatalf("create machine dispatch provider auth secret: %v", err)
		}
		machinePool, err = store.Execution().CreateMachinePool(
			ctx,
			executionstore.CreateMachinePoolInput{
				OrgID:                         toolsTestOrgID,
				Name:                          machinePoolName,
				Provider:                      "test.provider",
				DefaultMachineCPU:             intPtrForToolsTest(1),
				DefaultMachineMemoryMB:        intPtrForToolsTest(1024),
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"machine-dispatch"}`),
				ProviderAuthSecretID:          providerAuthSecret.ID,
				MaxTotalMachines:              2,
				MaxTotalCPU:                   intPtrForToolsTest(2),
				MaxTotalMemoryMB:              intPtrForToolsTest(2048),
				MaxMachineCPU:                 intPtrForToolsTest(1),
				MaxMachineMemoryMB:            intPtrForToolsTest(1024),
			},
		)
		if err != nil {
			t.Fatalf("create machine dispatch pool: %v", err)
		}
		if _, err := store.Execution().CreateProjectMachinePoolGrant(
			ctx,
			executionstore.CreateProjectMachinePoolGrantInput{
				OrgID:          toolsTestOrgID,
				ProjectID:      toolsTestProjectID,
				MachinePoolID:  machinePool.ID,
				IdempotencyKey: "tools-machine-dispatch-pool-grant-" + label,
			},
		); err != nil {
			t.Fatalf("create machine dispatch pool grant: %v", err)
		}
	}
	sourceYAML := `instruction: Test machine dispatch.
model:
  provider_config: openai-prod
  name: tools-test
machine_sources:
  - machine_pool_name: ` + machinePool.Name + `
    max_machines: 1
    initial_num_machines: 0
tools:
  create_machine:
    permission:
      mode: always_allow
      parameters: {}
  delete_machine:
    permission:
      mode: always_allow
      parameters: {}
  list_machines:
    permission:
      mode: always_allow
      parameters: {}
  inspect_machine:
    permission:
      mode: always_allow
      parameters: {}
`
	compiled := compileToolsAgentYAMLResolved(t, ctx, store, user.ID, sourceYAML, now.Add(3*time.Second))
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               toolsTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create machine dispatch config: %v", err)
	}
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      toolsTestProjectID,
			AgentConfigID:  config.ID,
			LaunchedBy:     toolsTestUserPrincipal(user.ID),
			IdempotencyKey: "tools-machine-dispatch-launch-" + label,
		},
	)
	if err != nil {
		t.Fatalf("launch machine dispatch agent: %v", err)
	}
	return machineDispatchFixture{
		Pool:        pool,
		Store:       store,
		UserID:      user.ID,
		MachinePool: machinePool,
		Config:      config,
		Launch:      launch,
		Now:         now,
	}
}

type executableBindingFixture struct {
	GrantID       storage.ID
	MachineID     storage.ID
	RuntimeID     storage.ID
	DaemonTokenID storage.ID
	DisplayName   string
}

func createMachineToolCallForDirectStoreTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID, userID, configID storage.ID,
	label, name string,
	inputJSON json.RawMessage,
	now time.Time,
) (storage.ID, executionstore.AgentRuntimeLockRecord, executionstore.AdmittedAgentInputTurn, executionstore.ModelCallContextRecord) {
	t.Helper()
	call := model.ToolCall{ID: "call_" + label, Name: name, Input: inputJSON}
	toolCalls, lock, admitted, contextRecord := createMachineToolCallsForDirectStoreTest(
		t,
		ctx,
		store,
		agentID,
		userID,
		configID,
		label,
		[]model.ToolCall{call},
		now,
	)
	return toolCalls[0].ID, lock, admitted, contextRecord
}

func createMachineToolCallsForDirectStoreTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID, userID, configID storage.ID,
	label string,
	calls []model.ToolCall,
	now time.Time,
) ([]executionstore.ToolCallRecord, executionstore.AgentRuntimeLockRecord, executionstore.AdmittedAgentInputTurn, executionstore.ModelCallContextRecord) {
	t.Helper()
	toolCalls, lock, admitted, contextRecord := recordMachineToolCallsForDirectStoreTest(
		t,
		ctx,
		store,
		agentID,
		userID,
		configID,
		label,
		calls,
		now,
	)
	for index, toolCall := range toolCalls {
		ready, err := store.Execution().MarkToolCallReady(
			ctx,
			executionstore.MarkToolCallReadyInput{
				ProjectID:     toolsTestProjectID,
				AgentID:       agentID,
				ID:            toolCall.ID,
				RuntimeLockID: lock.ID,
			},
		)
		if err != nil {
			t.Fatalf("allow machine tool call %s: %v", toolCall.ProviderCallID, err)
		}
		toolCalls[index] = ready
	}
	return toolCalls, lock, admitted, contextRecord
}

func recordMachineToolCallForDirectStoreTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID, userID, configID storage.ID,
	label, name string,
	inputJSON json.RawMessage,
	now time.Time,
) (storage.ID, executionstore.AgentRuntimeLockRecord, executionstore.AdmittedAgentInputTurn, executionstore.ModelCallContextRecord) {
	t.Helper()
	call := model.ToolCall{ID: "call_" + label, Name: name, Input: inputJSON}
	toolCalls, lock, admitted, contextRecord := recordMachineToolCallsForDirectStoreTest(
		t,
		ctx,
		store,
		agentID,
		userID,
		configID,
		label,
		[]model.ToolCall{call},
		now,
	)
	return toolCalls[0].ID, lock, admitted, contextRecord
}

func recordMachineToolCallsForDirectStoreTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	agentID, userID, configID storage.ID,
	label string,
	calls []model.ToolCall,
	now time.Time,
) ([]executionstore.ToolCallRecord, executionstore.AgentRuntimeLockRecord, executionstore.AdmittedAgentInputTurn, executionstore.ModelCallContextRecord) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("machine tool fixture requires at least one tool proposal")
	}
	producer, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(userID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:      toolsTestProjectID,
			AgentID:        agentID,
			Actor:          producer,
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"seed machine tool call"}]`),
			IdempotencyKey: "tools-machine-tool-input-" + label,
		},
	)
	if err != nil {
		t.Fatalf("create machine tool seed input: %v", err)
	}
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	if err != nil {
		t.Fatalf("claim machine tool seed input: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel {
		t.Fatal("expected machine tool seed input admission")
	}
	lock := claim.RuntimeLock
	admitted := claim.Model.AdmittedInputTurn
	modelCall := claimNormalModelCallForToolsTest(
		t,
		ctx,
		store,
		toolsTestProjectID,
		agentID,
		lock,
		[]storage.ID{input.ID},
		configID,
		admitted.Events[0].Sequence,
		storage.NilID,
	)
	contextRecord := modelCall.Context
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		"tools-test",
		modelprotocol.APIFormatOpenAIResponses,
		modelprotocol.APIVariantDefault,
		model.Response{
			ID:         "resp_tools_machine_tool_" + label,
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls(calls),
		},
	)
	if err != nil {
		t.Fatalf("build machine tool provider response: %v", err)
	}
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(calls))
	for _, call := range calls {
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: call.ID,
			Type:           toolcatalog.ToolTypeBuiltIn,
		})
	}
	_, records, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          toolsTestProjectID,
			AgentID:            agentID,
			RuntimeLockID:      lock.ID,
			ModelCallContextID: contextRecord.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   bindings,
		},
	)
	if err != nil {
		t.Fatalf("record machine tool seed output: %v", err)
	}
	if len(records) != len(calls) {
		t.Fatalf("recorded machine tool calls = %d, want %d", len(records), len(calls))
	}
	return records, lock, admitted, contextRecord
}

func createToolsRuntimeAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	name string,
	now time.Time,
) executionstore.LaunchAgentResult {
	t.Helper()
	return createToolsRuntimeAgentWithMachineSources(t, ctx, store, userID, name, nil, now)
}

type toolsAgentMachineSource struct {
	MachineName string
	Cwd         string
}

func createToolsRuntimeAgentWithMachineSources(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	name string,
	machineSources []toolsAgentMachineSource,
	now time.Time,
) executionstore.LaunchAgentResult {
	t.Helper()
	return createToolsRuntimeAgentWithMachineSourcesAndSkills(
		t,
		ctx,
		store,
		userID,
		name,
		machineSources,
		nil,
		now,
	)
}

func createToolsRuntimeAgentWithMachineSourcesAndSkills(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	name string,
	machineSources []toolsAgentMachineSource,
	skillIDs []string,
	now time.Time,
) executionstore.LaunchAgentResult {
	t.Helper()
	sourceYAML := `instruction: Test tool execution.
model:
  provider_config: openai-prod
  name: tools-test
`
	if len(machineSources) > 0 {
		sourceYAML += "machine_sources:\n"
		for _, source := range machineSources {
			sourceYAML += "  - machine_name: " + source.MachineName + "\n"
			if source.Cwd != "" {
				sourceYAML += "    cwd: " + source.Cwd + "\n"
			}
		}
	}
	if len(skillIDs) > 0 {
		sourceYAML += "skills:\n"
		for _, skillID := range skillIDs {
			sourceYAML += "  - " + skillID + "\n"
		}
	}
	sourceYAML += `tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
`
	compiled := compileToolsAgentYAMLResolved(t, ctx, store, userID, sourceYAML, now)
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               toolsTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create %s config: %v", name, err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       toolsTestProjectID,
		Name:            name,
		CurrentConfigID: config.ID,
		IdempotencyKey:  "tools-profile-" + name,
	})
	if err != nil {
		t.Fatalf("create %s profile: %v", name, err)
	}
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      toolsTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     toolsTestUserPrincipal(userID),
		IdempotencyKey: "tools-launch-" + name,
	})
	if err != nil {
		t.Fatalf("launch %s agent: %v", name, err)
	}
	return launch
}

func ensureToolsProjectOperator(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	now time.Time,
) {
	t.Helper()
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: toolsTestOrgID, UserID: userID, Role: "admin"},
	); err != nil {
		t.Fatalf("add tools org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     toolsTestOrgID,
			ProjectID: toolsTestProjectID,
			UserID:    userID,
			Role:      "operator",
		},
	); err != nil {
		t.Fatalf("add tools project membership: %v", err)
	}
}

func createExecutableBinding(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	userID storage.ID,
	name string,
	now time.Time,
) executableBindingFixture {
	t.Helper()
	machine, err := store.Execution().CreateDaemonMachine(
		ctx,
		executionstore.CreateDaemonMachineInput{
			OrgID:          toolsTestOrgID,
			DisplayName:    name + " machine",
			IdempotencyKey: "tools-target-machine-" + name,
		},
	)
	if err != nil {
		t.Fatalf("create %s machine: %v", name, err)
	}
	grant, _, err := store.Execution().CreateProjectMachineGrant(
		ctx,
		executionstore.CreateProjectMachineGrantInput{
			OrgID:          toolsTestOrgID,
			ProjectID:      toolsTestProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: "tools-target-grant-" + name,
		},
	)
	if err != nil {
		t.Fatalf("grant %s machine: %v", name, err)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(
		ctx,
		executionstore.CreateBYOMachineDaemonTokenInput{
			OrgID:     toolsTestOrgID,
			MachineID: machine.ID,
			Name:      name + " daemon",
		},
	)
	if err != nil {
		t.Fatalf("create %s token: %v", name, err)
	}
	registration, err := store.Execution().RegisterDaemonRuntimeWithReconciliation(
		ctx,
		executionstore.RegisterDaemonRuntimeInput{
			OrgID:            toolsTestOrgID,
			MachineID:        machine.ID,
			DaemonTokenID:    token.Record.ID,
			DaemonInstanceID: toolsTestID("daemon-tools-" + name),
			DaemonVersion:    "1.0.0",
			LeaseTimeout:     time.Hour,
		},
	)
	if err != nil {
		t.Fatalf("register %s runtime: %v", name, err)
	}
	runtime := registration.Runtime
	return executableBindingFixture{
		GrantID:       grant.ID,
		MachineID:     machine.ID,
		RuntimeID:     runtime.ID,
		DaemonTokenID: token.Record.ID,
		DisplayName:   machine.DisplayName,
	}
}
