package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppResourcesRejectInvalidAndConflictingDefinitions(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(*AgentConfigAppResourceSource)
	}{
		{"unknown definition", "unknown app definition", func(r *AgentConfigAppResourceSource) {
			r.Definition = "unregistered.app"
		}},
		{"removed definition", "unknown app definition", func(r *AgentConfigAppResourceSource) {
			r.Definition = "omnara.external"
		}},
		{"wrong provider", "does not belong", func(r *AgentConfigAppResourceSource) {
			r.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameGitHubRead: {}}
		}},
		{"missing scope", "connection and scope", func(r *AgentConfigAppResourceSource) { r.Scope = nil }},
		{"missing connection", "connection and scope", func(r *AgentConfigAppResourceSource) { r.Connection = "" }},
		{"wrong scope", "does not match provider", func(r *AgentConfigAppResourceSource) {
			r.Scope = &appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42}}
		}},
		{"placeholder", "concrete thread_ts", func(r *AgentConfigAppResourceSource) { r.Scope.Slack.ThreadTS = "${thread}" }},
		{"wrong handler", "not supported", func(r *AgentConfigAppResourceSource) {
			r.InteractionHandler = &appdefinition.InteractionHandler{Definition: appdefinition.DiscordInteractions}
		}},
		{"removed handler", "not supported", func(r *AgentConfigAppResourceSource) {
			r.InteractionHandler = &appdefinition.InteractionHandler{Definition: "omnara.external.interactions"}
		}},
		{"unknown event", "listener event", func(r *AgentConfigAppResourceSource) {
			r.Listener = &appdefinition.Listener{Events: []string{"commit"}}
		}},
		{"incomplete custom definition", "description", func(r *AgentConfigAppResourceSource) {
			r.Tools = map[string]AgentConfigToolSource{"custom": {Type: toolcatalog.ToolTypeCustom}}
		}},
		{"incomplete MCP definition", "url", func(r *AgentConfigAppResourceSource) {
			r.MCP = map[string]AgentConfigMCPSource{"crm": {}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := slackAppTestResource(t)
			resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
			test.change(&resource)
			_, err := compileAppTest(
				t,
				appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource}),
				appTestOptions(),
			)
			require.ErrorContains(t, err, test.want)
		})
	}
	for _, kind := range []string{"custom schema", "tool permission", "MCP endpoint", "MCP permission"} {
		t.Run(kind, func(t *testing.T) {
			first := appPolicyTestResource(t, customAppTestTool())
			first.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://example.com/mcp"}}
			second := appPolicyTestResource(t, customAppTestTool())
			second.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://example.com/mcp"}}
			tool, mcp := second.Tools["ticket"], second.MCP["crm"]
			ask := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			allow := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
			switch kind {
			case "custom schema":
				tool.InputSchema["required"] = []any{}
			case "tool permission":
				tool.Permission = &ask
			case "MCP endpoint":
				mcp.URL = "https://other.example.com/mcp"
			case "MCP permission":
				mcp.Permission = &allow
			}
			second.Tools["ticket"], second.MCP["crm"] = tool, mcp
			_, err := compileAppTest(
				t,
				appTestSource(map[string]AgentConfigAppResourceSource{"first": first, "second": second}),
				appTestOptions(),
			)
			require.ErrorContains(t, err, "conflicting")
		})
	}
}

func TestAppInstanceRejectsUnknownSelectionsAndResolverFailures(t *testing.T) {
	appID := testMachineSourcePublicID(t, publicid.KindProjectApp, "instance")
	defaults := appPolicyTestResource(t, customAppTestTool())
	defaults.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://example.com/mcp"}}
	opts := appTestOptions()
	opts.ResolveAppInstance = func(string) (AppInstanceResolution, error) {
		return AppInstanceResolution{AppInstanceID: appID, Resource: defaults}, nil
	}
	for _, test := range []struct {
		selection AgentConfigAppResourceSource
		want      string
	}{
		{AgentConfigAppResourceSource{Tools: map[string]AgentConfigToolSource{"unknown": {}}}, "not exported"},
		{AgentConfigAppResourceSource{MCP: map[string]AgentConfigMCPSource{"unknown": {}}}, "not exported"},
		{
			AgentConfigAppResourceSource{Tools: map[string]AgentConfigToolSource{"ticket": {Description: "Different"}}},
			"conflicts",
		},
		{
			AgentConfigAppResourceSource{MCP: map[string]AgentConfigMCPSource{"crm": {URL: "https://other.example.com/mcp"}}},
			"conflicts",
		},
	} {
		test.selection.AppInstance = appID
		_, err := compileAppTest(t, appTestSource(map[string]AgentConfigAppResourceSource{"app": test.selection}), opts)
		require.ErrorContains(t, err, test.want)
	}
	_, err := compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"app": {AppInstance: appID}}),
		CompileOptions{},
	)
	require.ErrorContains(t, err, "ResolveAppInstance")
	defaults.AppInstance = appID
	_, err = compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"app": {AppInstance: appID}}),
		opts,
	)
	require.ErrorContains(t, err, "inline resource")
	slack := slackAppTestResource(t)
	_, err = compileAppTest(t, appTestSource(map[string]AgentConfigAppResourceSource{"chat": slack}), CompileOptions{})
	require.ErrorContains(t, err, "ResolveAppConnection")
	_, err = compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"chat": slack}),
		CompileOptions{ResolveAppConnection: func(string, string) (string, error) { return "", nil }},
	)
	require.ErrorContains(t, err, "different connection id")
}

func TestCompiledAppResourcesFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Compiled)
	}{
		{"missing provenance", func(c *Compiled) {
			tool := c.Tools[toolcatalog.ToolNameSlackPostMessage]
			tool.AppOrigin = nil
			c.Tools[toolcatalog.ToolNameSlackPostMessage] = tool
		}},
		{"invented provenance", func(c *Compiled) {
			c.Tools[toolcatalog.ToolNameSlackPostMessage].AppOrigin.ResourceKeys = []string{"unknown"}
		}},
		{"provider marked base", func(c *Compiled) { c.Tools[toolcatalog.ToolNameSlackPostMessage].AppOrigin.Base = true }},
		{"missing tool", func(c *Compiled) { delete(c.Tools, toolcatalog.ToolNameSlackPostMessage) }},
		{"unknown handler", func(c *Compiled) {
			r := c.AppResources["chat"]
			r.InteractionHandler = &appdefinition.InteractionHandler{Definition: "unregistered"}
			c.AppResources["chat"] = r
		}},
		{"removed definition", func(c *Compiled) {
			r := c.AppResources["chat"]
			r.Definition = "omnara.external"
			c.AppResources["chat"] = r
		}},
		{"removed handler", func(c *Compiled) {
			r := c.AppResources["chat"]
			r.InteractionHandler = &appdefinition.InteractionHandler{Definition: "omnara.external.interactions"}
			c.AppResources["chat"] = r
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := slackAppTestResource(t)
			resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
			result, err := compileAppTest(
				t,
				appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource}),
				appTestOptions(),
			)
			require.NoError(t, err)
			test.mutate(&result.Compiled)
			encoded, err := EncodeCompiled(result.Compiled)
			require.NoError(t, err)
			_, err = RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
			require.ErrorContains(t, err, "compiled app resources")
		})
	}
}

func TestAppSchemaRejectsLauncherSecretsAndUnknownFields(t *testing.T) {
	for _, extra := range []string{
		`"launcher":{}`, `"scope":{"slack":{"channel_id":"C123","token":"secret"}}`, `"secret":"secret"`,
		`"listener":{"events":["message"],"unknown":true}`, `"interaction_handler":{"definition":"omnara.slack.interactions","token":"secret"}`,
		`"scope":{"external":{"kind":"ticket","key":"123"}}`,
	} {
		source := `{"instruction":"Help","model":{"provider_config":"test","name":"model"},"app_resources":{"app":{"definition":"omnara.slack",` + extra + `}}}`
		_, err := Compile(SourceFormatJSON, []byte(source), appTestOptions())
		require.ErrorContains(t, err, "unknown field")
	}
	resource := slackAppTestResource(t)
	result, err := compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource}),
		appTestOptions(),
	)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(result.CanonicalJSON, &doc))
	resources, ok := doc["app_resources"].(map[string]any)
	require.True(t, ok)
	chat, ok := resources["chat"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{"secret", "scope"} {
		t.Run("compiled/"+field, func(t *testing.T) {
			if field == "secret" {
				chat["secret"] = "never accepted"
			} else {
				delete(chat, "secret")
				chat["scope"] = map[string]any{"external": map[string]any{"kind": "ticket", "key": "123"}}
			}
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			// Recompute the hash through the existing canonical helper used by fixtures.
			sum := sha256.Sum256(canonicalizeJSON(raw))
			_, err = RuntimeContractFromCompiled(raw, CompilerVersion, hex.EncodeToString(sum[:]))
			require.ErrorContains(t, err, "unknown field")
		})
	}
}

func TestAppTemplateValidatesExportsWithoutRequiringLaunchScope(t *testing.T) {
	resource := slackAppTestResource(t)
	resource.Scope = nil
	resource.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameSlackPostMessage: {}}
	resource.Listener = &appdefinition.Listener{Events: []string{"message"}}
	require.NoError(t, ValidateAppResourceTemplate(resource))
	_, err := compileAppTest(
		t,
		appTestSource(map[string]AgentConfigAppResourceSource{"chat": resource}),
		appTestOptions(),
	)
	require.ErrorContains(t, err, "connection and scope")
	for _, test := range []struct {
		want   string
		change func(*AgentConfigAppResourceSource)
	}{
		{"connection", func(r *AgentConfigAppResourceSource) { r.Connection = "" }},
		{"scope", func(r *AgentConfigAppResourceSource) {
			r.Scope = &appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "${channel}"}}
		}},
		{"description", func(r *AgentConfigAppResourceSource) {
			r.Tools = map[string]AgentConfigToolSource{"ticket": {Type: toolcatalog.ToolTypeCustom}}
		}},
		{"not belong", func(r *AgentConfigAppResourceSource) {
			r.Tools = map[string]AgentConfigToolSource{toolcatalog.ToolNameGitHubRead: {}}
		}},
		{"credentials", func(r *AgentConfigAppResourceSource) {
			r.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://user:password@example.com/mcp"}}
		}},
		{"url", func(r *AgentConfigAppResourceSource) { r.MCP = map[string]AgentConfigMCPSource{"crm": {}} }},
	} {
		changed := resource
		test.change(&changed)
		require.ErrorContains(t, ValidateAppResourceTemplate(changed), test.want)
	}
	resource = appPolicyTestResource(t, customAppTestTool())
	resource.MCP = map[string]AgentConfigMCPSource{"crm": {URL: "https://example.com/mcp"}}
	require.NoError(t, ValidateAppResourceTemplate(resource))
	resource.Definition = "omnara.external"
	require.ErrorContains(t, ValidateAppResourceTemplate(resource), "unknown app definition")
}
