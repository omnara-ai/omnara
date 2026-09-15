//go:build integration

package tools

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestApprovedMCPDispatchClassifiesReconstructedToolAvailability(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		present   bool
		errorCode string
		errorText string
	}{
		{"vanished", false, "unsupported", "is not enabled for this agent config"},
		{"missing_schema", true, "malformed", "has no runtime input schema"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-approved-"+test.name, true)
			name := toolcatalog.MCPRuntimeToolName("docs", "greet")
			call := fixture.recordPendingToolCall(t, ctx, "approved-mcp", name,
				` {"name":"Ada","count":9007199254740993} `, fixture.Now.Add(20*time.Second))
			originalInput := bytes.Clone(call.Input)
			turn := fixture.turn()
			spec := turn.Tools[name]
			spec.Permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			turn.Tools[name] = spec
			client := &ownedMCPToolClient{}
			executor := Executor{Store: fixture.Store, MCP: client}
			require.NoError(t, executor.PrepareToolCallPermission(ctx, turn, call))
			toolCallID := fixture.toolCallID(t, ctx, call.ID)
			permission := integrationToolInteraction(t, ctx, fixture, toolCallID, "permission")
			require.Equal(t, executionstore.AgentInteractionStateOpen, permission.State)
			actor, err := executionstore.OmnaraActorParams(toolsTestOrgID,
				identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.User.ID})
			require.NoError(t, err)
			resolved, err := fixture.Store.Execution().ResolveAgentInteraction(ctx,
				executionstore.ResolveAgentInteractionInput{
					ProjectID: toolsTestProjectID, AgentID: fixture.Agent.ID, ID: permission.ID, Actor: actor,
					Resolution: interactionform.Resolution{
						Answers: []interactionform.Answer{{OptionIndices: []int{toolpermission.AllowOptionIndex}}},
					},
				})
			require.NoError(t, err)
			require.Equal(t, executionstore.AgentInteractionStateResolved, resolved.State)
			approved, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
			require.NoError(t, err)
			require.Equal(t, toolcatalog.ToolTypeMCP, approved.Type)
			require.Equal(t, executionstore.ToolCallStateReady, approved.State)

			// Model-context reconstruction may lose a discovered tool after approval.
			// A retained tool with a missing schema is a distinct malformed contract.
			reconstructed := turn
			reconstructed.Tools = map[string]ToolSpec{}
			if test.present {
				spec.InputSchema = nil
				reconstructed.Tools[name] = spec
			}
			result, err := executor.Dispatch(ctx, reconstructed, call)
			require.NoError(t, err)
			require.Equal(t, DispatchCompleted, result.Disposition)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			require.Equal(t, test.errorCode, body["error_code"])
			require.Equal(t, fmt.Sprintf("tool %q %s", name, test.errorText), body["error"])
			require.Zero(t, client.callToolCount.Load())
			require.Zero(t, client.initializeCount.Load())
			require.Equal(t, originalInput, []byte(call.Input), "dispatch must not rewrite approved arguments")
			completed, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
			require.NoError(t, err)
			require.Equal(t, executionstore.ToolCallStateCompleted, completed.State)
			require.Equal(t, executionstore.ToolResultOutcomeFailed, completed.Outcome)
			require.Equal(t, approved.Input, completed.Input)
			require.JSONEq(t, string(completed.ResultContentParts), string(result.ContentParts))

			// Restoring discovery cannot reopen this already terminal call.
			replayed, err := executor.Dispatch(ctx, turn, call)
			require.NoError(t, err)
			require.Equal(t, DispatchCompleted, replayed.Disposition)
			require.Equal(t, completed.ResultContentParts, replayed.ContentParts)
			require.Zero(t, client.callToolCount.Load())
		})
	}
}
