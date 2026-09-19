package agentconfig

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppToolAuthorityPreservesOriginalMeaningAndLiveRevocation(t *testing.T) {
	resource := slackAppTestResource(t)
	resource.Scope.Slack.ThreadTS = ""
	resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
	source := appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource})
	compile := func(source AgentConfigSource) RuntimeContract {
		result, err := compileAppTest(t, source, appTestOptions())
		require.NoError(t, err)
		contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
		require.NoError(t, err)
		return contract
	}
	original := compile(source)
	authority, err := ResolveAppToolAuthority(original, original, toolcatalog.ToolNameSlackPostMessage, "")
	require.NoError(t, err)
	require.True(
		t,
		authority.AllowsScope(
			appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"}},
		),
	)
	require.False(t, authority.AllowsScope(appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C999"}}))
	// Adding another resource cannot redirect an old call's omitted selector.
	source.AppResources["second"] = resource
	current := compile(source)
	authority, err = ResolveAppToolAuthority(original, current, toolcatalog.ToolNameSlackPostMessage, "")
	require.NoError(t, err)
	require.Equal(t, "chat", authority.ResourceKey)
	_, err = ResolveAppToolAuthority(current, current, toolcatalog.ToolNameSlackPostMessage, "")
	require.ErrorContains(t, err, "resource is required")
	delete(source.AppResources, "second")
	// Narrowing scope allows only destinations common to the two snapshots.
	resource.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "111.222"}}
	source.AppResources["chat"] = resource
	authority, err = ResolveAppToolAuthority(original, compile(source), toolcatalog.ToolNameSlackPostMessage, "chat")
	require.NoError(t, err)
	require.False(t, authority.AllowsScope(appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}}))
	require.True(t, authority.AllowsScope(*resource.Scope))
	// A same-name resource pointing at another account never takes over a call.
	resource.Connection = slackAppTestResource(t).Connection + "bad"
	next := original.AppResources["chat"]
	next.ConnectionID = resource.Connection
	changed := original
	changed.AppResources = map[string]AppResourceCompiled{"chat": next}
	_, err = ResolveAppToolAuthority(original, changed, toolcatalog.ToolNameSlackPostMessage, "chat")
	require.ErrorContains(t, err, "changed provider identity")
	// Policy changes require a fresh call and the ordinary permission gate.
	resource.Connection = slackAppTestResource(t).Connection
	source.AppResources["chat"] = resource
	policy := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	source.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {Permission: &policy}}
	_, err = ResolveAppToolAuthority(original, compile(source), toolcatalog.ToolNameSlackPostMessage, "chat")
	require.ErrorContains(t, err, "permission changed")
	delete(source.AppResources, "chat")
	_, err = ResolveAppToolAuthority(original, compile(source), toolcatalog.ToolNameSlackPostMessage, "chat")
	require.ErrorContains(t, err, "unavailable")
}
