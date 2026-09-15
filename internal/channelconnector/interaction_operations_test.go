package channelconnector

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestInteractionPresentationPreservesCanonicalForm(t *testing.T) {
	t.Parallel()
	encode := func(kind publicid.Kind) string {
		id, err := publicid.Encode(kind, uuid.New())
		require.NoError(t, err)
		return id
	}
	payload := InteractionPayload{
		InteractionID: encode(publicid.KindAgentInteraction), AgentID: encode(publicid.KindAgent),
		ChannelID: encode(publicid.KindIntegrationTarget), Kind: "question",
		Form: interactionform.Form{
			Title: "  Question  ", Questions: []interactionform.Question{{
				Prompt: " Choose ", Options: []interactionform.Option{{Label: " Option "}},
			}},
		},
	}
	before, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, payload.Validate())
	after, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Equal(t, before, after, "validation must not normalize shared canonical form slices")
	payload.ChannelID = payload.AgentID
	require.Error(t, payload.Validate())
}

func TestInteractionDeliveryCannotSmuggleAnApproval(t *testing.T) {
	t.Parallel()
	_, err := DecodeInteractionResult(json.RawMessage(`{"message_id":"posted"}`))
	require.NoError(t, err)
	for _, raw := range []string{
		`{"answers":[]}`, `{"approved":true}`, `null`, `{"metadata":{"token":1,"token":2}}`,
	} {
		_, err := DecodeInteractionResult(json.RawMessage(raw))
		require.Error(t, err)
	}
}
