package tools

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestInteractionDestinationRendering(t *testing.T) {
	t.Parallel()
	result, err := renderInteractionDestinations(executionstore.InteractionDestinations{})
	require.NoError(t, err)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.JSONEq(t, `{"current":null,"destinations":[]}`, string(raw))
	targetID, connectionID := uuid.New(), uuid.New()
	target, err := publicid.Encode(publicid.KindIntegrationTarget, targetID)
	require.NoError(t, err)
	connection, err := publicid.Encode(publicid.KindIntegrationConnection, connectionID)
	require.NoError(t, err)
	result, err = renderInteractionDestinations(executionstore.InteractionDestinations{
		Current: executionstore.InteractionSelection{IntegrationTargetID: targetID, ResourceKey: "chat"},
		Destinations: []executionstore.InteractionDestinationOption{{
			Destination: executionstore.InteractionDestination{
				IntegrationTargetID: targetID, ConnectionID: connectionID, ResourceKey: "chat",
				HandlerDefinition: appdefinition.SlackInteractions,
				Address:           integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
			},
			DisplayName: "Support thread",
		}},
	})
	require.NoError(t, err)
	raw, err = json.Marshal(result)
	require.NoError(t, err)
	require.JSONEq(t, `{"current":{"target_id":"`+target+`","resource":"chat"},"destinations":[{
		"target_id":"`+target+`","resource":"chat","connection_id":"`+connection+`",
		"handler_definition":"`+appdefinition.SlackInteractions+`",
		"scope":{"kind":"thread","ref":"C123:111.222"},"display_name":"Support thread"}]}`, string(raw))
	require.NotContains(t, string(raw), targetID.String())
	require.NotContains(t, string(raw), connectionID.String())
}

func TestInteractionDestinationRenderingHidesIneligibleCurrent(t *testing.T) {
	t.Parallel()
	stored := executionstore.InteractionSelection{IntegrationTargetID: uuid.New(), ResourceKey: "chat"}
	for _, scenario := range []string{"no eligible options", "other resource", "other target"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			input := executionstore.InteractionDestinations{Current: stored}
			if scenario != "no eligible options" {
				destination := executionstore.InteractionDestination{
					IntegrationTargetID: stored.IntegrationTargetID, ResourceKey: stored.ResourceKey,
					ConnectionID: uuid.New(), HandlerDefinition: appdefinition.SlackInteractions,
					Address: integrationstore.ConversationAddress{Kind: "channel", Ref: "C123"},
				}
				if scenario == "other resource" {
					destination.ResourceKey = "other"
				} else {
					destination.IntegrationTargetID = uuid.New()
				}
				input.Destinations = []executionstore.InteractionDestinationOption{{Destination: destination}}
			}
			result, err := renderInteractionDestinations(input)
			require.NoError(t, err)
			require.Nil(t, result.Current)
			require.Len(t, result.Destinations, len(input.Destinations))
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"current":null`)
		})
	}
}

func interactionImplementationForTest(t *testing.T, name string) toolImplementation {
	t.Helper()
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	entry, found := catalog.Lookup(name)
	require.True(t, found)
	validator, err := jsonschema.Compile(entry.InputSchema)
	require.NoError(t, err)
	for _, registration := range interactionToolRegistrations() {
		if registration.name == name {
			require.NoError(t, validatePermissionModeHandlers(registration.permissionModes, entry.PermissionModes))
			return toolImplementation{toolRegistration: registration, inputSchemaValidator: validator}
		}
	}
	t.Fatalf("missing interaction tool registration %s", name)
	return toolImplementation{}
}

func TestInteractionToolRegistrationValidation(t *testing.T) {
	t.Parallel()
	for _, name := range toolcatalog.InteractionDestinationToolNames() {
		registered := 0
		for _, registration := range builtInToolRegistrations() {
			if registration.name == name {
				registered++
			}
		}
		require.Equal(t, 1, registered)
		implementation := interactionImplementationForTest(t, name)
		require.NotNil(t, implementation.handler.Transactional)
		require.Nil(t, implementation.handler.Async)
		valid := json.RawMessage(`{}`)
		if name == toolcatalog.ToolNameSetInteractionDestination {
			valid = json.RawMessage(`{"destination":null}`)
		}
		require.NoError(t, implementation.validateInput(valid))
		require.Error(t, implementation.validateInput(json.RawMessage(`{"connection_id":"inject"}`)))
	}
}
