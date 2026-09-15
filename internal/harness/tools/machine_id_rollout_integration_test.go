//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestMachineIDRolloutResumesLegacyApprovals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tool      string
		input     string
		errorCode string
	}{
		{"explicit-run", "run_command", `{"command":"pwd","machine_ref":"mchr-old123"}`, "malformed"},
		{"implicit-run", "run_command", `{"command":"pwd"}`, ""},
		{"implicit-inspect", "inspect_machine", `{}`, "tool_authorization_invalidated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newMachineDispatchFixture(t, ctx, "id-rollout-"+tc.name)
			machine := createExecutableBinding(
				t, ctx, fixture.Store, fixture.UserID, tc.name, fixture.Now.Add(time.Second),
			)
			var bindingID storage.ID
			err := fixture.Pool.QueryRow(ctx, `
INSERT INTO agent_machine_bindings(
    org_id, project_id, agent_id, machine_id, binding_kind, state, created_at, updated_at
)
VALUES ($1, $2, $3, $4, 'explicit', 'attached', $5, $5)
RETURNING id`,
				toolsTestOrgID, toolsTestProjectID, fixture.Launch.Agent.ID, machine.MachineID, fixture.Now,
			).Scan(&bindingID)
			require.NoError(t, err)

			label := "id-rollout-" + tc.name
			call := model.ToolCall{ID: "call_" + label, Name: tc.tool, Input: json.RawMessage(tc.input)}
			toolCallID, lock, admitted, modelContext := recordMachineToolCallForDirectStoreTest(
				t, ctx, fixture.Store, fixture.Launch.Agent.ID, fixture.UserID, fixture.Config.ID,
				label, tc.tool, call.Input, fixture.Now.Add(2*time.Second),
			)
			selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			turn := Turn{
				ProjectID: toolsTestProjectID, AgentID: fixture.Launch.Agent.ID,
				SourceEventID: admitted.Events[0].ID, RuntimeLockID: lock.ID, ModelCallContextID: modelContext.ID,
				Tools: map[string]ToolSpec{tc.tool: {Permission: selection}},
			}

			legacyAuthorizationInput := json.RawMessage(`{"mode":"inspect","machine_ref":"mchr-old123"}`)
			if tc.tool == "run_command" {
				legacyAuthorizationInput, err = runCommandAuthorizationInput(bindingID, resolvedRunCommandRequest{
					Command: "pwd", Selector: processcmd.ShellDefault, IOMode: processcmd.IOModePipe,
				})
				require.NoError(t, err)
			}
			descriptor, found := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), selection.Mode)
			require.True(t, found)
			request, err := permissionChallenge(
				call, permissionModeContext{selection: selection, descriptor: descriptor}, legacyAuthorizationInput,
			)
			require.NoError(t, err)
			interaction, err := fixture.Store.Execution().CreatePermissionInteraction(
				ctx, executionstore.CreatePermissionInteractionInput{
					ProjectID: toolsTestProjectID, AgentID: fixture.Launch.Agent.ID,
					ToolCallID: toolCallID, RuntimeLockID: lock.ID, Request: request,
				},
			)
			require.NoError(t, err)
			record, err := fixture.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, toolCallID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateAwaitingPermission, record.State)

			actor, err := executionstore.OmnaraActorParams(toolsTestOrgID, toolsTestUserPrincipal(fixture.UserID))
			require.NoError(t, err)
			_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
				ProjectID: turn.ProjectID, AgentID: turn.AgentID, ID: interaction.ID, Actor: actor,
				Resolution: interactionform.Resolution{
					Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
				},
			})
			require.NoError(t, err)

			executor := Executor{Store: fixture.Store}
			result, err := executor.Dispatch(ctx, turn, call)
			require.NoError(t, err, "a legacy approval must not leave tool work retrying an execution error")
			process, processFound, err := fixture.Store.Execution().GetProcessByToolCall(
				ctx, turn.ProjectID, turn.AgentID, toolCallID,
			)
			require.NoError(t, err)
			if tc.errorCode == "" {
				require.Equal(t, DispatchDeferred, result.Disposition)
				require.True(t, processFound)
				require.Equal(t, bindingID, process.AgentMachineBindingID)
				return
			}
			require.False(t, processFound, "the rejected legacy call must never start a command")
			require.Equal(t, DispatchCompleted, result.Disposition)
			var parts []struct {
				Value struct {
					ErrorCode string `json:"error_code"`
					Error     string `json:"error"`
				} `json:"value"`
			}
			require.NoError(t, json.Unmarshal(result.ContentParts, &parts))
			require.Len(t, parts, 1)
			require.Equal(t, tc.errorCode, parts[0].Value.ErrorCode)
			if tc.errorCode == "malformed" {
				require.Contains(t, parts[0].Value.Error, "machine_ref")
			}
			record, err = fixture.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, toolCallID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
			var retainedOwnership bool
			require.NoError(t, fixture.Pool.QueryRow(
				ctx, `SELECT runtime_lock_id IS NOT NULL FROM tool_calls WHERE id = $1`, toolCallID,
			).Scan(&retainedOwnership))
			require.False(t, retainedOwnership)
			replayed, err := executor.Dispatch(ctx, turn, call)
			require.NoError(t, err)
			require.JSONEq(t, string(result.ContentParts), string(replayed.ContentParts))
		})
	}
}
