package agentconfig

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestHandlerAuthorityPinsCurrentApp(t *testing.T) {
	opts, apps := appTestOptions(t)
	compiled := compileAppTest(t, "interaction_handlers: {engineering-team: {}}", opts).Compiled
	contract := runtimeAppTest(t, compiled)
	appID := compiled.InteractionHandlers["engineering-team"].AppID
	authority, err := ResolveInteractionHandlerAuthority(contract, contract, "engineering-team", apps)
	require.NoError(t, err)
	require.Equal(t, appID, authority.AppID)
	_, err = ResolveInteractionHandlerAuthority(contract, RuntimeContract{}, "engineering-team", apps)
	require.Error(t, err)
	_, err = ResolveInteractionHandlerAuthority(contract, contract, "engineering-team", nil)
	require.Error(t, err)
	changed := RuntimeContract{
		InteractionHandlers: map[string]AppCapabilityCompiled{
			"engineering-team": {AppID: publicidTestID(122)},
		},
	}
	_, err = ResolveInteractionHandlerAuthority(contract, changed, "engineering-team", apps)
	require.Error(t, err)
}

func TestHandlerPaginationKeepsCurrentSelectionOutsidePage(t *testing.T) {
	handler := PreparedAppInteractionHandler{
		AppID: publicidTestID(120),
		PreparedInteractionHandler: appdefinition.PreparedInteractionHandler{
			Description: "Slack",
			InputSchema: []byte(`{"type":"object"}`),
		},
	}
	handlers := map[string]PreparedAppInteractionHandler{"alpha": handler, "beta": handler, "gamma": handler}
	appID, err := publicid.Encode(publicid.KindProjectApp, handler.AppID)
	require.NoError(t, err)
	selection := &HandlerSelection{Handler: "gamma", AppID: appID, Args: []byte(`{"channel_id":"C123"}`)}
	first, err := ListInteractionHandlers(handlers, selection, "", 1)
	require.NoError(t, err)
	require.Equal(t, "alpha", first.Handlers[0].Handler)
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
