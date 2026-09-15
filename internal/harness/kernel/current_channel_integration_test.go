//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestSteeringChannelReachesNextModelContextAtAdmission(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	agentID, userID := fixture.createAgent(t, ctx, "openai/channel-context-model", fixture.Now)
	app, err := fixture.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: kernelTestOrgID, OwnerProjectID: kernelTestProjectID,
		Provider: "discord", ProviderAppRef: "channel-context", ConnectorKey: channelconnector.BuiltInConnectorKey,
		DisplayName: "Channel context", State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	install, err := fixture.Store.Integrations().UpsertIntegrationInstall(ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: kernelTestOrgID, ProjectID: kernelTestProjectID, IntegrationAppID: app.ID,
			InstalledBy: kernelTestUserPrincipal(userID), Provider: "discord", ProviderAccountRef: "context-bot",
			IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
			State: integrationstore.IntegrationInstallStateActive,
		})
	require.NoError(t, err)
	definition, err := fixture.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: kernelTestProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "test-thread", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object"}`), Capabilities: integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: []channelconnector.Capability{{ConnectorKey: app.ConnectorKey, Provider: app.Provider}},
		})
	require.NoError(t, err)
	target, err := fixture.Store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: kernelTestProjectID, ChannelDefinitionID: definition.ID, IntegrationInstallID: install.ID,
			ProviderRef: "private-thread-context", ProviderRefKind: "thread",
		})
	require.NoError(t, err)
	binding, err := fixture.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: kernelTestProjectID, AgentID: agentID, IntegrationInstallID: install.ID,
			IntegrationTargetID: target.ID, ReceiveAllowed: true, SendAllowed: true, Source: "context-test",
		})
	require.NoError(t, err)
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, target.ID)
	require.NoError(t, err)
	turn := fixture.admitContentInputTurn(t, ctx, agentID, userID, "original request", fixture.Now.Add(time.Millisecond))
	retryAfter := int64(3600)
	client := &sequenceKernelModel{
		providerModelSlug: "channel-context-model",
		errs: []error{model.ProviderError{
			Kind: model.ErrorKindRateLimit, Source: "test-provider", Code: "rate_limited", Message: "retry later",
			RetryAfter: &model.RetryAfter{DeltaSeconds: &retryAfter},
		}},
		responses: []model.Response{{ID: "steered", StopReason: model.StopReasonEndTurn,
			Content: []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: "handled channel input"}}}},
	}
	executor := AgentExecutor{
		Store: fixture.Store, ModelResolver: liveTestModelResolver(fixture.Store, client),
		ToolExecutor: tools.Executor{Store: fixture.Store}, Now: func() time.Time { return fixture.Now.Add(time.Second) },
	}
	require.NoError(t, executor.ExecuteModelWork(ctx, turn))
	if len(client.prepared) != 1 {
		var failure string
		require.NoError(t,
			fixture.Pool.QueryRow(ctx,
				`SELECT coalesce(string_agg(text_content, E'\n'), '') FROM content_blocks WHERE agent_id = $1 AND block_kind = 'error'`,
				agentID).Scan(&failure))
		var contextFailure string
		require.NoError(t,
			fixture.Pool.QueryRow(ctx,
				`SELECT coalesce(string_agg(state || ':' || error_kind || ':' || error_code || ':' || error_message, E'\n'), '') FROM model_call_contexts WHERE agent_id = $1`,
				agentID).Scan(&contextFailure))
		t.Fatalf("model prepared %d requests; durable error: %s; contexts: %s", len(client.prepared), failure, contextFailure)
	}
	require.Empty(t, client.prepared[0].Bundle.CurrentChannelID)
	require.NoError(t,
		fixture.Store.Execution().ReleaseAgentRuntimeLock(ctx,
			kernelTestProjectID,
			agentID,
			turn.RuntimeLockID))
	// Exercise admission/context projection with the already resolved channel
	// and binding. Receipt verification and workflow selection have separate tests.
	input, _, _, err := fixture.Store.Execution().CreateAgentContentInput(ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID: kernelTestProjectID, AgentID: agentID,
			IntegrationTargetID: target.ID, IntegrationTargetBindingID: binding.ID,
			Actor: &executionstore.ActorParams{
				Provider: install.Provider, ProviderTenantID: install.ProviderTenantID,
				ProviderUserID: "channel-author",
			},
			IdempotencyScope: integrationstore.IdempotencyScope(install),
			ContentBlocks:    json.RawMessage(`[{"type":"text","text":"new channel request"}]`),
			DeliveryMode:     executionstore.DeliveryModeSteering, IdempotencyKey: "channel-steering",
		})
	require.NoError(t, err)
	current, err := fixture.Store.Execution().GetAgentCurrentChannelID(ctx, kernelTestProjectID, agentID)
	require.NoError(t, err)
	require.Equal(t, storage.NilID, current, "enqueue does not redirect a running agent")
	work, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx,
		kernelTestClaimInput(fixture.Now.Add(2*time.Second)))
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, work.Kind)
	require.Contains(t, work.Model.InputIDs, input.ID)
	current, err = fixture.Store.Execution().GetAgentCurrentChannelID(ctx, kernelTestProjectID, agentID)
	require.NoError(t, err)
	require.Equal(t, target.ID, current)
	require.NoError(t,
		executor.ExecuteModelWork(ctx,
			modelWorkExecutionFromClaimForKernelTest(work,
				fixture.Now.Add(2*time.Second))))
	require.Len(t, client.prepared, 2)
	bundle := client.prepared[1].Bundle
	require.Equal(t, channelID, bundle.CurrentChannelID)
	var names []string
	for _, spec := range bundle.ToolSpecs {
		names = append(names, spec.Name)
	}
	require.Contains(t, names, toolcatalog.ToolNameSetCurrentChannel)
}
