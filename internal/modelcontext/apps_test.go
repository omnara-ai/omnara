package modelcontext

import (
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestBuildAppScopeAndInteractionSelectionWithoutImplicitSend(t *testing.T) {
	connection, err := publicid.Encode(publicid.KindIntegrationConnection, testIDN(940))
	require.NoError(t, err)
	base := testAgentConfigRecord()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(base.CompiledDefinition, &compiled))
	compiled.AppResources = map[string]agentconfig.AppResourceCompiled{
		"approvals": {
			Definition:   appdefinition.Slack,
			Enabled:      true,
			ConnectionID: connection,
			Scope: &appdefinition.Scope{
				Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"},
			},
			InteractionHandler: &appdefinition.InteractionHandler{Definition: appdefinition.SlackInteractions},
		},
	}
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	base.CompiledDefinition, base.EffectiveDefinitionHash = encoded.CanonicalJSON, encoded.Hash
	targetID := testIDN(941)
	destinations := executionstore.InteractionDestinations{
		Current: executionstore.InteractionSelection{ResourceKey: "approvals", IntegrationTargetID: targetID},
		Destinations: []executionstore.InteractionDestinationOption{{
			TargetRef: "slack-abcd", Destination: executionstore.InteractionDestination{
				ResourceKey:         "approvals",
				HandlerDefinition:   appdefinition.SlackInteractions,
				IntegrationTargetID: targetID,
			},
		}},
	}
	store := &fakeContextStore{watermark: 1, hasConfig: true, config: base, interactionDestinations: destinations}
	build := func() Bundle {
		t.Helper()
		bundle, err := (Builder{Store: store}).Build(
			t.Context(),
			BuildInput{
				Now:             time.Now(),
				ProjectID:       testProjectID,
				AgentID:         testAgentID,
				TurnID:          testTurnID,
				OpeningInputIDs: []uuid.UUID{testInputID},
			},
		)
		require.NoError(t, err)
		return bundle
	}
	bundle := build()
	require.Contains(t, bundle.SystemPrompt, `"channel_id":"C123"`)
	require.Contains(t, bundle.SystemPrompt, `"thread_ts":"111.222"`)
	publicTarget, err := publicid.Encode(publicid.KindIntegrationTarget, targetID)
	require.NoError(t, err)
	require.Equal(
		t,
		&InteractionDestinationRef{Resource: "approvals", TargetID: publicTarget},
		bundle.InteractionRouting.Destination,
	)
	require.False(t, HasTool(bundle.ToolSpecs, "send_integration_message"))
	require.False(t, HasTool(bundle.ToolSpecs, "slack_post_message"), "a handler does not grant sending tools")
	require.NotContains(t, InteractionRoutingContent(bundle.InteractionRouting), targetID.String())
	// A revoked destination cannot be represented as usable merely because the
	// selection pointer remains. Storage's eligible list is current authority.
	store.interactionDestinations.Destinations = nil
	bundle = build()
	require.Nil(t, bundle.InteractionRouting.Destination)
	require.Contains(t, InteractionRoutingContent(bundle.InteractionRouting), "dashboard only")
}
