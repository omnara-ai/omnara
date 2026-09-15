package slack

import (
	"encoding/json"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/interactionform"
)

// The gateway test compares actual TS rendering and a callback built from those
// rendered IDs/options against this same fixture. Keep the core action consumer pinned
// without requiring Node during ordinary Go unit tests.
func TestGatewayInteractionFixturesRoundTrip(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/gateway_interactions.json")
	require.NoError(t, err)
	var fixtures []struct {
		Name       string                              `json:"name"`
		Operation  channelconnector.InteractionPayload `json:"operation"`
		Rendered   json.RawMessage                     `json:"rendered"`
		Callback   json.RawMessage                     `json:"callback"`
		Resolution interactionform.Resolution          `json:"resolution"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.Len(t, fixtures, 3)
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, fixture.Operation.Validate())
			want := PromptActionValue{
				Type:                PromptType,
				InteractionID:       fixture.Operation.InteractionID,
				AgentID:             fixture.Operation.AgentID,
				IntegrationTargetID: fixture.Operation.ChannelID,
			}
			text, blocks := InteractionFormPromptBlocks(fixture.Operation.Form, want)
			channel, thread, err := Destination(
				fixture.Operation.Destination.ProviderRefKind,
				fixture.Operation.Destination.ProviderRef,
			)
			require.NoError(t, err)
			rendered, err := PromptPayload(MessageTarget{Channel: channel, ThreadTS: thread}, text, blocks)
			require.NoError(t, err)
			require.JSONEq(t, string(fixture.Rendered), string(rendered))

			form := url.Values{"payload": {string(fixture.Callback)}}
			envelope, err := DecodeActionsEnvelope([]byte(form.Encode()))
			require.NoError(t, err)
			actual, err := PromptActionFromActions(envelope)
			require.NoError(t, err)
			require.Equal(t, want, actual)
			resolved := ResolveInteractionForm(fixture.Operation.Form, envelope.State)
			require.Empty(t, resolved.InvalidReason)
			require.Equal(t, fixture.Resolution, resolved.Resolution)

			delete(envelope.State.Values, "omnara_question_0")
			incomplete := ResolveInteractionForm(fixture.Operation.Form, envelope.State)
			require.NotEmpty(t, incomplete.InvalidReason, "partial state must not resolve the form")
		})
	}
}
