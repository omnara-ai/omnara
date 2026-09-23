//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestMachinePoolDiscoveryAndCreationUseStableIDs(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchEnvironment(t, ctx, "pool-discovery", management.Tenant)
	store := fixture.Store.Execution()
	selectedPool := fixture.MachinePool
	otherPool, err := store.CreateMachinePool(ctx, executionstore.CreateMachinePoolInput{
		OrgID: toolsTestOrgID, Name: "Second pool", Provider: selectedPool.Provider,
		ProviderAuthSecretID: selectedPool.ProviderAuthSecretID,
		DefaultMachineCPU:    selectedPool.DefaultMachineCPU, DefaultMachineMemoryMB: selectedPool.DefaultMachineMemoryMB,
		DefaultMachineProviderOptions: selectedPool.DefaultMachineProviderOptions, MaxTotalMachines: 2,
	})
	require.NoError(t, err)
	otherPoolGrant, err := store.CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, MachinePoolID: otherPool.ID, IdempotencyKey: "second-pool",
	})
	require.NoError(t, err)

	var definition agentconfig.Compiled
	require.NoError(t, json.Unmarshal(fixture.Config.CompiledDefinition, &definition))
	definition.MachineSources[0].Description = "Build workers"
	definition.MachineSources = append(definition.MachineSources, agentconfig.MachineSourceCompiled{
		MachinePoolID: poolPublicIDForTest(t, otherPool.ID), MaxMachines: 1, Description: "Test workers",
	})
	createTool := definition.Tools["create_machine"]
	createTool.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	definition.Tools["create_machine"] = createTool
	compiled, err := json.Marshal(definition)
	require.NoError(t, err)
	config, err := store.CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: toolsTestProjectID, ConfiguredModelID: fixture.Config.ConfiguredModelID, CompiledDefinition: compiled,
	})
	require.NoError(t, err)
	launch, err := store.LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, AgentConfigID: config.ID,
		LaunchedBy: toolsTestUserPrincipal(fixture.UserID), IdempotencyKey: "pool-discovery-derived",
	})
	require.NoError(t, err)
	poolID := poolPublicIDForTest(t, selectedPool.ID)
	listEmptyCall := model.ToolCall{ID: "list-empty", Name: "list_machines", Input: json.RawMessage(`{}`)}
	createCall := model.ToolCall{
		ID: "create-selected", Name: "create_machine", Input: json.RawMessage(`{"machine_pool_id":"` + poolID + `"}`),
	}
	listCreatedCall := model.ToolCall{ID: "list-created", Name: "list_machines", Input: json.RawMessage(`{}`)}
	inspectCall := model.ToolCall{ID: "inspect-created", Name: "inspect_machine", Input: json.RawMessage(`{}`)}
	listAfterRevokeCall := model.ToolCall{ID: "list-after-revoke", Name: "list_machines", Input: json.RawMessage(`{}`)}
	records, lock, admitted, modelContext := recordMachineToolCallsForDirectStoreTest(
		t, ctx, fixture.Store, launch.Agent.ID, fixture.UserID, config.ID,
		"pool-discovery", []model.ToolCall{listEmptyCall, createCall, listCreatedCall, inspectCall, listAfterRevokeCall},
		fixture.Now.Add(time.Second),
	)
	var createToolCallID uuid.UUID
	for _, record := range records {
		if record.ProviderCallID == createCall.ID {
			createToolCallID = record.ID
			continue
		}
		_, err := store.MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: toolsTestProjectID, AgentID: launch.Agent.ID, ID: record.ID, RuntimeLockID: lock.ID,
		})
		require.NoError(t, err)
	}
	turn := Turn{
		ProjectID: toolsTestProjectID, AgentID: launch.Agent.ID, SourceEventID: admitted.Events[0].ID,
		RuntimeLockID: lock.ID, ModelCallContextID: modelContext.ID,
		Tools: map[string]ToolSpec{
			"list_machines":   {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)},
			"inspect_machine": {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)},
			"create_machine":  {Permission: createTool.Permission},
		},
	}
	executor := Executor{Store: fixture.Store, Now: func() time.Time { return fixture.Now.Add(2 * time.Second) }}
	dispatch := func(call model.ToolCall) map[string]any {
		t.Helper()
		result, err := executor.Dispatch(ctx, turn, call)
		require.NoError(t, err)
		require.Equal(t, DispatchCompleted, result.Disposition)
		return toolResultMapFromTestParts(t, result.ContentParts)
	}
	renamePool := func(id uuid.UUID, name string) {
		t.Helper()
		_, err := store.UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
			OrgID: toolsTestOrgID, ID: id, Name: &name,
		})
		require.NoError(t, err)
	}
	listed := dispatch(listEmptyCall)
	require.Empty(t, listed["machines"])
	require.ElementsMatch(t, []any{
		map[string]any{"machine_pool_id": poolID, "machine_pool_name": selectedPool.Name, "description": "Build workers"},
		map[string]any{
			"machine_pool_id":   poolPublicIDForTest(t, otherPool.ID),
			"machine_pool_name": otherPool.Name, "description": "Test workers",
		},
	}, listed["machine_pools"])

	renamePool(selectedPool.ID, "Temporary pool")
	renamePool(otherPool.ID, selectedPool.Name)
	renamePool(selectedPool.ID, otherPool.Name)
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, createCall))
	interaction, found, err := store.GetAgentInteractionByToolCallKind(
		ctx, toolsTestProjectID, launch.Agent.ID, createToolCallID, executionstore.AgentInteractionKindPermission,
	)
	require.NoError(t, err)
	require.True(t, found)
	var request toolpermission.Request
	require.NoError(t, json.Unmarshal(interaction.Request, &request))
	require.JSONEq(t, string(createCall.Input), string(request.Authorization.Input))
	requestBody, err := json.Marshal(request.Form)
	require.NoError(t, err)
	require.Contains(t, string(requestBody), otherPool.Name)
	require.Contains(t, string(requestBody), poolID)
	approveToolPermissionForTest(t, ctx, store, interaction, fixture.UserID)

	renamed := "Renamed build pool"
	renamePool(selectedPool.ID, renamed)
	created := dispatch(createCall)
	require.Equal(t, poolID, created["machine_pool_id"])
	require.Equal(t, renamed, created["machine_pool_name"])
	machine, err := store.GetPoolMachineByCreateToolCall(ctx, toolsTestProjectID, launch.Agent.ID, createToolCallID)
	require.NoError(t, err)
	require.Equal(t, selectedPool.ID, machine.Machine.MachinePoolID)
	listed = dispatch(listCreatedCall)
	machines, ok := listed["machines"].([]any)
	require.True(t, ok)
	require.Len(t, machines, 1)
	listedMachine, ok := machines[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, poolID, listedMachine["machine_pool_id"])
	inspected := dispatch(inspectCall)
	require.Equal(t, poolID, inspected["machine_pool_id"])

	_, err = store.DeleteProjectMachinePoolGrant(ctx, toolsTestOrgID, toolsTestProjectID, otherPoolGrant.ID)
	require.NoError(t, err)
	listed = dispatch(listAfterRevokeCall)
	require.Equal(t, []any{
		map[string]any{"machine_pool_id": poolID, "machine_pool_name": renamed, "description": "Build workers"},
	}, listed["machine_pools"])
}
