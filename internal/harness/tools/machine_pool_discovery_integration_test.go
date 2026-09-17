//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestMachinePoolDiscoveryAndCreationUseStableIDs(t *testing.T) {
	ctx := context.Background()
	fixture := newMachineDispatchFixture(t, ctx, "pool-discovery")
	store := fixture.Store.Execution()
	first := fixture.MachinePool
	second, err := store.CreateMachinePool(ctx, executionstore.CreateMachinePoolInput{
		OrgID: toolsTestOrgID, Name: "Second pool", Provider: first.Provider,
		ProviderAuthSecretID: first.ProviderAuthSecretID,
		DefaultMachineCPU:    first.DefaultMachineCPU, DefaultMachineMemoryMB: first.DefaultMachineMemoryMB,
		DefaultMachineProviderOptions: first.DefaultMachineProviderOptions, MaxTotalMachines: 2,
	})
	require.NoError(t, err)
	secondGrant, err := store.CreateProjectMachinePoolGrant(ctx, executionstore.CreateProjectMachinePoolGrantInput{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, MachinePoolID: second.ID, IdempotencyKey: "second-pool",
	})
	require.NoError(t, err)

	// Use a source-less compiled config, as derived subagents do.
	var definition agentconfig.Compiled
	require.NoError(t, json.Unmarshal(fixture.Config.CompiledDefinition, &definition))
	definition.MachineSources[0].Description = "Build workers"
	definition.MachineSources = append(definition.MachineSources, agentconfig.MachineSourceCompiled{
		MachinePoolID: poolPublicIDForTest(t, second.ID), MaxMachines: 1, Description: "Test workers",
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
	poolID := poolPublicIDForTest(t, first.ID)
	calls := []model.ToolCall{
		{ID: "list-empty", Name: "list_machines", Input: json.RawMessage(`{}`)},
		{ID: "create-selected", Name: "create_machine", Input: json.RawMessage(`{"machine_pool_id":"` + poolID + `"}`)},
		{ID: "list-created", Name: "list_machines", Input: json.RawMessage(`{}`)},
		{ID: "inspect-created", Name: "inspect_machine", Input: json.RawMessage(`{}`)},
		{ID: "list-after-revoke", Name: "list_machines", Input: json.RawMessage(`{}`)},
	}
	records, lock, admitted, modelContext := recordMachineToolCallsForDirectStoreTest(
		t, ctx, fixture.Store, launch.Agent.ID, fixture.UserID, config.ID,
		"pool-discovery", calls, fixture.Now.Add(time.Second),
	)
	for i, record := range records {
		if i == 1 {
			continue // Creation must go through approval.
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
	listed := dispatch(calls[0])
	require.Empty(t, listed["machines"])
	require.ElementsMatch(t, []any{
		map[string]any{"machine_pool_id": poolID, "machine_pool_name": first.Name, "description": "Build workers"},
		map[string]any{
			"machine_pool_id":   poolPublicIDForTest(t, second.ID),
			"machine_pool_name": second.Name, "description": "Test workers",
		},
	}, listed["machine_pools"])

	// Rename both pools after discovery, before preparing the approval.
	_, err = store.UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: toolsTestOrgID, ID: first.ID, Name: new("Temporary pool"),
	})
	require.NoError(t, err)
	_, err = store.UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: toolsTestOrgID, ID: second.ID, Name: &first.Name,
	})
	require.NoError(t, err)
	_, err = store.UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: toolsTestOrgID, ID: first.ID, Name: &second.Name,
	})
	require.NoError(t, err)
	require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, calls[1]))
	interaction, found, err := store.GetAgentInteractionByToolCallKind(
		ctx, toolsTestProjectID, launch.Agent.ID, records[1].ID, "permission",
	)
	require.NoError(t, err)
	require.True(t, found)
	var request toolpermission.Request
	require.NoError(t, json.Unmarshal(interaction.Request, &request))
	require.JSONEq(t, string(calls[1].Input), string(request.Authorization.Input))
	requestBody, err := json.Marshal(request.Form)
	require.NoError(t, err)
	require.Contains(t, string(requestBody), second.Name)
	require.Contains(t, string(requestBody), poolID)
	actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(fixture.UserID))
	require.NoError(t, err)
	_, err = store.ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID: toolsTestProjectID, AgentID: launch.Agent.ID, ID: interaction.ID, Actor: actor,
		Resolution: interactionform.Resolution{
			Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
		},
	})
	require.NoError(t, err)

	// Another rename after approval still authorizes the same pool identity.
	renamed := "Renamed build pool"
	_, err = store.UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
		OrgID: toolsTestOrgID, ID: first.ID, Name: &renamed,
	})
	require.NoError(t, err)
	created := dispatch(calls[1])
	require.Equal(t, poolID, created["machine_pool_id"])
	require.Equal(t, renamed, created["machine_pool_name"])
	machine, err := store.GetPoolMachineByCreateToolCall(ctx, toolsTestProjectID, launch.Agent.ID, records[1].ID)
	require.NoError(t, err)
	require.Equal(t, first.ID, machine.Machine.MachinePoolID)
	listed = dispatch(calls[2])
	machines, ok := listed["machines"].([]any)
	require.True(t, ok)
	require.Len(t, machines, 1)
	listedMachine, ok := machines[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, poolID, listedMachine["machine_pool_id"])
	inspected := dispatch(calls[3])
	require.Equal(t, poolID, inspected["machine_pool_id"])

	_, err = store.DeleteProjectMachinePoolGrant(ctx, toolsTestOrgID, toolsTestProjectID, secondGrant.ID)
	require.NoError(t, err)
	listed = dispatch(calls[4])
	require.Equal(t, []any{
		map[string]any{"machine_pool_id": poolID, "machine_pool_name": renamed, "description": "Build workers"},
	}, listed["machine_pools"])
}
