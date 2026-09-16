//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

const managedSendParamsSchema = `{
  "type":"object",
  "properties":{
    "opaque_number":{"type":"integer"},
    "label":{"type":"string"},
    "annotation":{"type":"string","default":"not inserted"}
  },
  "required":["opaque_number","label"],
  "additionalProperties":false
}`

func TestManagedChannelSendPreservesParamsAndDurableArguments(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	params := json.RawMessage(`{"label":"  Keep Case  ","opaque_number":9007199254740993}`)
	f := newManagedOperationFixtureWithParams(t, ctx, "send-params", params)
	publishManagedSendSchema(t, f, json.RawMessage(managedSendParamsSchema))
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	before, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
	require.NoError(t, err)

	for range 2 {
		access, returned, err := f.Store.Execution().PrepareChannelSend(ctx, prepared)
		require.NoError(t, err)
		require.Equal(t, f.input.ChannelID, access.ChannelID)
		require.Equal(t, string(params), string(returned), "numbers and text survive without defaults or coercion")
	}
	after, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, before.Input, after.Input)
	require.Equal(t, executionstore.ToolCallStateRunning, after.State)
}

func TestManagedChannelSendRechecksCurrentSchemaAndExactChannel(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"changed_schema", "invalid_params", "wrong_channel", "revoked_binding",
		"replaced_binding", "canceled_agent", "settled_call", "deleted_installation",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			params := json.RawMessage(`{"label":"hello","opaque_number":42}`)
			if name == "invalid_params" {
				params = json.RawMessage(`{"label":"hello","opaque_number":"not a number"}`)
			}
			f := newManagedOperationFixtureWithParams(t, ctx, "send-"+name, params)
			definition := publishManagedSendSchema(t, f, json.RawMessage(managedSendParamsSchema))
			f.start(t, ctx)
			if name == "wrong_channel" {
				// Both channels allow sending; only one matches the admitted call.
				target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
					ProjectID: testProjectID, IntegrationInstallID: f.binding.IntegrationInstallID,
					ChannelDefinitionID: definition.ID, ProviderRef: "other", ProviderRefKind: "conversation",
				})
				require.NoError(t, err)
				binding := f.binding
				binding.IntegrationTargetID = target.ID
				_, err = f.Store.Integrations().CreateIntegrationTargetBinding(ctx, binding)
				require.NoError(t, err)
				f.input.ChannelID = target.ID
			}
			prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
			require.NoError(t, err)
			switch name {
			case "changed_schema":
				publishManagedSendSchema(t, f, json.RawMessage(`{"type":"object","additionalProperties":false}`))
			case "revoked_binding", "replaced_binding":
				require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(
					ctx, testProjectID, prepared.Binding().ID))
				if name == "replaced_binding" {
					replacement, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, f.binding)
					require.NoError(t, err)
					require.NotEqual(t, prepared.Binding().ID, replacement.ID)
				}
			case "canceled_agent":
				_, err := f.Store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
				})
				require.NoError(t, err)
			case "settled_call":
				_, err := f.Store.Execution().CompleteRuntimeToolCall(ctx, executionstore.CompleteRuntimeToolCallInput{
					ProjectID: testProjectID, AgentID: f.AgentID, ID: f.input.ToolCallID, RuntimeLockID: f.Lock.ID,
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"already settled"}]`),
				})
				require.NoError(t, err)
			case "deleted_installation":
				require.NoError(t, f.Store.Integrations().DeleteIntegrationInstall(
					ctx, testProjectID, f.binding.IntegrationInstallID))
			}
			before, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
			require.NoError(t, err)
			_, returned, err := f.Store.Execution().PrepareChannelSend(ctx, prepared)
			switch name {
			case "wrong_channel":
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			case "revoked_binding", "replaced_binding", "canceled_agent", "settled_call", "deleted_installation":
				require.Error(t, err)
			default:
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			}
			require.Nil(t, returned, "failed preparation must not return dispatch parameters")
			call, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, f.input.ToolCallID)
			require.NoError(t, err)
			require.Equal(t, before.State, call.State, "preparation does not change the call's state")
			require.Equal(t, before.Input, call.Input, "preparation does not rewrite admitted arguments")
		})
	}
}

func TestManagedChannelSendTreatsSchemaApprovedParamsAsOpaque(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	params := json.RawMessage(`{"publish_review":true,"review_id":"custom-opaque-value"}`)
	f := newManagedOperationFixtureWithParams(t, ctx, "send-custom-params", params)
	publishManagedSendSchema(t, f, json.RawMessage(`{
  "type":"object",
  "properties":{"review_id":{"type":"string"},"publish_review":{"type":"boolean"}},
  "required":["review_id","publish_review"],
  "additionalProperties":false
}`))
	f.start(t, ctx)
	prepared, err := f.Store.Execution().PrepareChannelOperation(ctx, f.input)
	require.NoError(t, err)
	_, returned, err := f.Store.Execution().PrepareChannelSend(ctx, prepared)
	require.NoError(t, err)
	require.Equal(t, string(params), string(returned), "custom connector keys have no built-in Go interpretation")
}

func publishManagedSendSchema(
	t *testing.T, f managedOperationFixture, schema json.RawMessage,
) integrationstore.ChannelDefinition {
	t.Helper()
	definition, err := f.Store.Integrations().PublishConnectorChannelDefinition(t.Context(),
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: testProjectID, IntegrationInstallID: f.binding.IntegrationInstallID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindDiscordThread,
			SendParamsSchema: schema,
			Capabilities: integrationstore.ChannelCapabilities{
				Read: true, Send: true, Text: true, CreatesReplyChannel: true,
			},
			ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
		})
	require.NoError(t, err)
	return definition
}
