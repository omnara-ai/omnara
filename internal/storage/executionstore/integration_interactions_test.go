package executionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInteractionSnapshotAndReceiptBounds(t *testing.T) {
	t.Parallel()
	destination := InteractionDestination{
		IntegrationType:     integrationdefinition.SlackThread,
		HandlerKey:          "chat",
		IntegrationID:       uuid.New(),
		IntegrationTargetID: uuid.New(),
		Address:             integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
	}
	raw, err := json.Marshal(destination)
	require.NoError(t, err)
	parsed, err := (AgentInteractionRecord{Destination: raw}).CapturedDestination()
	require.NoError(t, err)
	require.Equal(t, destination, *parsed)
	parsed, err = (AgentInteractionRecord{}).CapturedDestination()
	require.NoError(t, err)
	require.Nil(t, parsed)
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{} {}`, `{"unexpected":true}`,
		strings.Repeat(" ", InteractionDestinationMaxBytes) + `{}`,
	} {
		_, err := (AgentInteractionRecord{Destination: json.RawMessage(raw)}).CapturedDestination()
		require.Error(t, err)
	}
	for _, change := range []func(*InteractionDestination){
		func(d *InteractionDestination) { d.IntegrationID = uuid.Nil },
		func(d *InteractionDestination) { d.HandlerKey = "chat__alias" },
		func(d *InteractionDestination) { d.IntegrationType = integrationdefinition.GitHubPR },
		func(d *InteractionDestination) { d.IntegrationType = "slack_unregistered" },
		func(d *InteractionDestination) { d.IntegrationType = "" },
		func(d *InteractionDestination) { d.Address.Ref = "not-a-thread" },
		func(d *InteractionDestination) { d.Address.Kind = "unknown" },
	} {
		changed := destination
		change(&changed)
		require.Error(t, changed.validate())
	}
	for _, raw := range []string{
		`null`, `[]`, `{} {}`, `{"id":"bad\u0000id"}`,
		`{"id":"` + strings.Repeat("x", InteractionReceiptMaxBytes) + `"}`,
	} {
		require.Error(
			t,
			validateInteractionObject(json.RawMessage(raw), InteractionReceiptMaxBytes),
		)
	}
	require.NoError(
		t,
		validateInteractionObject(
			json.RawMessage(`{"channel_id":"C123","message_ts":"111.222"}`),
			InteractionReceiptMaxBytes,
		),
	)
}

func TestIntegrationInteractionPendingSelectionToolPermission(t *testing.T) {
	t.Parallel()
	original := agentconfig.RuntimeContract{
		Tools: []agentconfig.RuntimeTool{
			{
				Name:       toolcatalog.ToolNameSetInteractionHandler,
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
			},
		},
	}
	require.True(t, interactionSelectionToolAuthorized(original, original))
	require.False(t, interactionSelectionToolAuthorized(original, agentconfig.RuntimeContract{}))
	for _, mode := range []string{toolpermission.ModeAlwaysDeny, toolpermission.ModeAlwaysAllow} {
		current := agentconfig.RuntimeContract{
			Tools: []agentconfig.RuntimeTool{
				{
					Name:       toolcatalog.ToolNameSetInteractionHandler,
					Permission: toolpermission.DefaultSelection(mode),
				},
			},
		}
		require.False(t, interactionSelectionToolAuthorized(original, current))
	}
}
