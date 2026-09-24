package agentconfig

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
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
		Destination: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123"}},
	}
	handlers := map[string]InteractionHandlerEntry{"alpha": handler, "beta": handler, "gamma": handler}
	integrationID, err := publicid.Encode(publicid.KindProjectIntegration, publicidTestID(120))
	require.NoError(t, err)
	selection := &HandlerSelection{
		Handler: "gamma", IntegrationID: integrationID, Args: []byte(`{}`), Destination: handler.Destination,
	}
	first, err := ListInteractionHandlers(handlers, selection, "", 1)
	require.NoError(t, err)
	require.Equal(t, "alpha", first.Handlers[0].Handler)
	require.Equal(t, handler.Destination, first.Handlers[0].Destination)
	require.Equal(t, selection, first.Selection)
	require.NotEmpty(t, first.NextCursor)
	second, err := ListInteractionHandlers(handlers, selection, first.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, "beta", second.Handlers[0].Handler)
	require.Equal(t, selection, second.Selection)
	last, err := ListInteractionHandlers(handlers, selection, second.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, "gamma", last.Handlers[0].Handler)
	require.Empty(t, last.NextCursor)
	empty, err := ListInteractionHandlers(nil, selection, "", 0)
	require.NoError(t, err)
	require.Empty(t, empty.Handlers)
	require.Equal(t, selection, empty.Selection)
	_, err = ListInteractionHandlers(handlers, nil, "!bad!", 1)
	require.Error(t, err)
	_, err = ListInteractionHandlers(handlers, nil, "", 101)
	require.Error(t, err)
}
