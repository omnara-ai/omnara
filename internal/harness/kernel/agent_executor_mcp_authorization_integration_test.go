//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/mcpauthorization"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const kernelMCPAuthorizationSource = `
instruction: Use MCP tools.
model:
  provider_config: openai-prod
  name: test-model
mcp:
  calendar:
    url: https://calendar.example.com/mcp
    permission:
      mode: always_allow
`

func TestAgentExecutorParksMCPToolOnAuthorizationAndResumesAfterGrant(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	profile := fixture.createConfigAndProfileBookmark(
		t, ctx, "Kernel MCP Authorization", "kernel-mcp-authorization", kernelMCPAuthorizationSource,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-mcp-authorization-launch",
	})
	require.NoError(t, err)

	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{
			{
				ID:         "resp-mcp-authorization-tool",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
					ID:    "call_mcp_authorization",
					Name:  toolcatalog.MCPRuntimeToolName("calendar", "create_event"),
					Input: json.RawMessage(`{"title":"Standup"}`),
				}}),
			},
			{
				ID:         "resp-mcp-authorization-final",
				Content:    []model.ResponsePart{{Type: "text", Text: "done"}},
				StopReason: model.StopReasonEndTurn,
			},
		},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion:    mcp.LegacyProtocolVersion,
		initializeAgentIDs: []string{"remote-session-1"},
		tools: []*sdkmcp.Tool{
			{Name: "create_event", Description: "create an event", InputSchema: map[string]any{"type": "object"}},
		},
		callToolErrors: []error{&mcp.HTTPError{
			Status: http.StatusUnauthorized,
			WWWAuthenticate: []string{
				`Bearer scope="calendar.events", resource_metadata="https://calendar.example.com/.well-known/oauth-protected-resource"`,
			},
		}},
		callToolResult: &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "created"}},
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		MCP:           mcpClient,
		ToolExecutor:  tools.Executor{Store: fixture.Store, MCP: mcpClient},
		Now:           func() time.Time { return now.Add(3 * time.Millisecond) },
	}
	input := fixture.admitContentInputTurn(
		t, ctx, launch.Agent.ID, kernelTestUserID, "add a standup to my calendar", now.Add(2*time.Millisecond),
	)
	require.NoError(t, executor.ExecuteModelWork(ctx, input))
	waitForKernelAsyncScope(t, executeNextToolWork(t, ctx, fixture, executor, input))

	interactions, err := fixture.Store.Execution().ListAgentInteractionsForAgent(ctx,
		executionstore.ListAgentInteractionsForAgentInput{
			ProjectID: kernelTestProjectID,
			AgentID:   launch.Agent.ID,
			State:     executionstore.AgentInteractionStateOpen,
			Limit:     10,
		})
	require.NoError(t, err)
	require.Len(t, interactions.Interactions, 1)
	interaction := interactions.Interactions[0]
	require.Equal(t, executionstore.AgentInteractionKindAuthorization, interaction.InteractionKind)
	request, err := interaction.AuthorizationRequest()
	require.NoError(t, err)
	require.Equal(t, "calendar", request.ServerKey)
	require.Equal(t, "create_event", request.ToolName)
	require.Nil(t, request.SecretID)
	publicAgentID, err := publicid.Encode(publicid.KindAgent, launch.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, "calendar-oauth-"+publicAgentID, request.SecretName)
	require.Equal(t,
		"https://calendar.example.com/.well-known/oauth-protected-resource", request.ResourceMetadataURL)
	require.Equal(t, []string{"calendar.events"}, request.RequiredScopes)
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx, kernelTestProjectID, launch.Agent.ID, interaction.ToolCallID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateWaiting, toolCall.State)

	// Granting access stores the token, attaches it to the agent's config, and
	// resolves the interaction, as the OAuth callback does.
	secret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          kernelTestOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: kernelTestProjectID,
		Name:           request.SecretName,
		Material: secrets.OAuthTokenSetMaterial{
			AccessToken: "granted-token",
			MCPURL:      request.MCPURL,
			Scopes:      "calendar.events",
			TokenType:   "Bearer",
		},
		Actor: kernelTestUserPrincipal(kernelTestUserID),
	})
	require.NoError(t, err)
	publicSecretID, err := publicid.Encode(publicid.KindSecret, secret.ID)
	require.NoError(t, err)
	authorizedSource := strings.Replace(kernelMCPAuthorizationSource,
		"    url: https://calendar.example.com/mcp\n",
		"    url: https://calendar.example.com/mcp\n    auth:\n      type: oauth\n      secret_id: "+publicSecretID+"\n", 1)
	compiled := fixture.compileAgentYAMLResolved(t, ctx, authorizedSource)
	_, err = fixture.Store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               kernelTestProjectID,
			Source:                  authorizedSource,
			SourceFormat:            "yaml",
			ConfiguredModelID:       parseConfiguredModelID(t, compiled),
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        launch.Agent.ID,
		Reason:         "mcp_authorization",
		IdempotencyKey: "kernel-mcp-authorization-config",
	})
	require.NoError(t, err)
	actor, err := executionstore.OmnaraActorParams(kernelTestOrgID, kernelTestUserPrincipal(kernelTestUserID))
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID:  kernelTestProjectID,
		AgentID:    launch.Agent.ID,
		ID:         interaction.ID,
		Resolution: mcpauthorization.GrantResolution(),
		Actor:      actor,
	})
	require.NoError(t, err)
	toolCall, err = fixture.Store.Execution().GetToolCall(
		ctx, kernelTestProjectID, launch.Agent.ID, interaction.ToolCallID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateReady, toolCall.State)

	claim := claimNextAgentWorkForKernelTest(t, ctx, fixture, launch.Agent.ID, executionstore.AgentWorkTool)
	resumed := tools.NewAsyncExecutionScope(nil)
	require.NoError(t, executor.ExecuteToolWork(tools.WithAsyncExecutionScope(ctx, resumed), ToolWorkExecution{
		Prepared:           claim.Tool.Prepared,
		ProjectID:          claim.ProjectID,
		AgentID:            claim.AgentID,
		TurnID:             claim.Tool.TurnID,
		ModelCallContextID: claim.Tool.ModelCallContextID,
		ModelOutputID:      claim.Tool.ModelOutputID,
		SourceEventID:      claim.Tool.SourceEventID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		Now:                now.Add(5 * time.Second),
	}))
	resumed.Seal()
	waitForKernelAsyncScope(t, resumed)
	require.Equal(t, 2, mcpClient.callToolCount)
	require.Empty(t, mcpClient.callToolConns[0].BearerToken)
	require.Equal(t, "granted-token", mcpClient.callToolConns[1].BearerToken)
	toolCall, err = fixture.Store.Execution().GetToolCall(
		ctx, kernelTestProjectID, launch.Agent.ID, interaction.ToolCallID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, toolCall.State)
	require.Equal(t, executionstore.ToolResultOutcomeSucceeded, toolCall.Outcome)
}

func TestAgentExecutorCompletesDeclinedMCPAuthorization(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	profile := fixture.createConfigAndProfileBookmark(
		t, ctx, "Kernel MCP Authorization Decline", "kernel-mcp-authorization-decline", kernelMCPAuthorizationSource,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-mcp-authorization-decline-launch",
	})
	require.NoError(t, err)
	modelClient := &sequenceKernelModel{
		providerModelSlug: "test-model",
		responses: []model.Response{{
			ID:         "resp-mcp-authorization-decline-tool",
			StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
				ID:    "call_mcp_authorization_decline",
				Name:  toolcatalog.MCPRuntimeToolName("calendar", "create_event"),
				Input: json.RawMessage(`{}`),
			}}),
		}},
	}
	mcpClient := &fakeKernelMCPClient{
		protocolVersion:    mcp.LegacyProtocolVersion,
		initializeAgentIDs: []string{"remote-session-1"},
		tools: []*sdkmcp.Tool{
			{Name: "create_event", Description: "create an event", InputSchema: map[string]any{"type": "object"}},
		},
		callToolErrors: []error{&mcp.HTTPError{
			Status:          http.StatusForbidden,
			WWWAuthenticate: []string{`Bearer error="insufficient_scope", scope="calendar.events"`},
		}},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		MCP:           mcpClient,
		ToolExecutor:  tools.Executor{Store: fixture.Store, MCP: mcpClient},
		Now:           func() time.Time { return now.Add(3 * time.Millisecond) },
	}
	input := fixture.admitContentInputTurn(
		t, ctx, launch.Agent.ID, kernelTestUserID, "add a standup", now.Add(2*time.Millisecond),
	)
	require.NoError(t, executor.ExecuteModelWork(ctx, input))
	waitForKernelAsyncScope(t, executeNextToolWork(t, ctx, fixture, executor, input))

	interactions, err := fixture.Store.Execution().ListAgentInteractionsForAgent(ctx,
		executionstore.ListAgentInteractionsForAgentInput{
			ProjectID: kernelTestProjectID,
			AgentID:   launch.Agent.ID,
			State:     executionstore.AgentInteractionStateOpen,
			Limit:     10,
		})
	require.NoError(t, err)
	require.Len(t, interactions.Interactions, 1)
	interaction := interactions.Interactions[0]
	actor, err := executionstore.OmnaraActorParams(kernelTestOrgID, kernelTestUserPrincipal(kernelTestUserID))
	require.NoError(t, err)
	_, err = fixture.Store.Execution().ResolveAgentInteraction(ctx, executionstore.ResolveAgentInteractionInput{
		ProjectID:  kernelTestProjectID,
		AgentID:    launch.Agent.ID,
		ID:         interaction.ID,
		Resolution: interactionDeclineResolution(),
		Actor:      actor,
	})
	require.NoError(t, err)
	toolCall, err := fixture.Store.Execution().GetToolCall(
		ctx, kernelTestProjectID, launch.Agent.ID, interaction.ToolCallID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, toolCall.State)
	require.Equal(t, executionstore.ToolResultOutcomeDenied, toolCall.Outcome)
	require.Equal(t, 1, mcpClient.callToolCount)
}

func interactionDeclineResolution() interactionform.Resolution {
	return interactionform.Resolution{
		Answers: []interactionform.Answer{{OptionIndices: []int{mcpauthorization.DeclineOptionIndex}}},
	}
}

func waitForKernelAsyncScope(t *testing.T, scope *tools.AsyncExecutionScope) {
	t.Helper()
	select {
	case <-scope.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("async tool work did not finish")
	}
	require.NoError(t, scope.Err())
}
