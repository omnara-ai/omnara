package agentconfig

import (
	"testing"

	"encoding/json"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestHandlerAuthorityPinsCurrentIntegration(t *testing.T) {
	opts, integrations := integrationTestOptions(t)
	compiled := compileIntegrationTest(t, "interaction_handlers: {engineering-team: {}}", opts).Compiled
	contract := runtimeIntegrationTest(t, compiled)
	integrationID := compiled.InteractionHandlers["engineering-team"].IntegrationID
	authority, err := ResolveInteractionHandlerAuthority(contract, contract, "engineering-team", integrations)
	require.NoError(t, err)
	require.Equal(t, integrationID, authority.IntegrationID)
	_, err = ResolveInteractionHandlerAuthority(contract, RuntimeContract{}, "engineering-team", integrations)
	require.Error(t, err)
	_, err = ResolveInteractionHandlerAuthority(contract, contract, "engineering-team", nil)
	require.Error(t, err)
	changed := RuntimeContract{
		InteractionHandlers: map[string]IntegrationCapabilityCompiled{
			"engineering-team": {IntegrationID: publicidTestID(122)},
		},
	}
	_, err = ResolveInteractionHandlerAuthority(contract, changed, "engineering-team", integrations)
	require.Error(t, err)
}

func TestHandlerPaginationKeepsCurrentSelectionOutsidePage(t *testing.T) {
	handler := InteractionHandlerEntry{
		Description: "Slack",
		InputSchema: []byte(`{"type":"object"}`),
		Destination: json.RawMessage(`{"channel_id":"C123"}`),
	}
	handlers := map[string]InteractionHandlerEntry{"alpha": handler, "beta": handler, "gamma": handler}
	integrationID, err := publicid.Encode(publicid.KindIntegration, publicidTestID(120))
	require.NoError(t, err)
	selection := &HandlerSelection{
		Handler: "gamma", IntegrationID: integrationID, Args: []byte(`{}`), Destination: handler.Destination,
	}
	first, err := ListInteractionHandlers(handlers, selection, 0, 1)
	require.NoError(t, err)
	require.Equal(t, "alpha", first.Handlers[0].Handler)
	require.Equal(t, handler.Destination, first.Handlers[0].Destination)
	require.Equal(t, selection, first.Selection)
	require.True(t, first.HasMore)
	require.NotNil(t, first.NextOffset)
	require.Equal(t, 1, *first.NextOffset)
	second, err := ListInteractionHandlers(handlers, selection, *first.NextOffset, 1)
	require.NoError(t, err)
	require.Equal(t, "beta", second.Handlers[0].Handler)
	require.Equal(t, selection, second.Selection)
	require.True(t, second.HasMore)
	require.NotNil(t, second.NextOffset)
	require.Equal(t, 2, *second.NextOffset)
	last, err := ListInteractionHandlers(handlers, selection, *second.NextOffset, 1)
	require.NoError(t, err)
	require.Equal(t, "gamma", last.Handlers[0].Handler)
	require.False(t, last.HasMore)
	require.Nil(t, last.NextOffset)
	exact, err := ListInteractionHandlers(handlers, selection, 1, 2)
	require.NoError(t, err)
	require.Len(t, exact.Handlers, 2)
	require.Equal(t, "beta", exact.Handlers[0].Handler)
	require.Equal(t, "gamma", exact.Handlers[1].Handler)
	require.False(t, exact.HasMore)
	require.Nil(t, exact.NextOffset)
	past, err := ListInteractionHandlers(handlers, selection, 3, 1)
	require.NoError(t, err)
	require.Empty(t, past.Handlers)
	require.False(t, past.HasMore)
	require.Equal(t, selection, past.Selection)
	encoded, err := json.Marshal(last)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"has_more":false`)
	require.NotContains(t, string(encoded), "next_offset")
	encoded, err = json.Marshal(first)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"next_offset":1`)
	empty, err := ListInteractionHandlers(nil, selection, 0, 0)
	require.NoError(t, err)
	require.Empty(t, empty.Handlers)
	require.Equal(t, selection, empty.Selection)
	_, err = ListInteractionHandlers(handlers, nil, -1, 1)
	require.ErrorContains(t, err, "offset must be non-negative")
	_, err = ListInteractionHandlers(handlers, nil, 0, 101)
	require.Error(t, err)
}
