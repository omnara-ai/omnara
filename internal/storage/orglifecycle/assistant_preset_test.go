package orglifecycle

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAssistantPresetPreservesOperatorPolicy(t *testing.T) {
	model := modelstore.ConfiguredModelSummaryRecord{ID: uuid.New(), Name: "model", ProviderConfigName: "provider"}
	allowlist, err := ssrf.ParseAllowlist([]string{"10.23.0.4:8080"})
	require.NoError(t, err)
	for _, test := range []struct {
		name       string
		url        string
		allowLocal bool
		allowlist  *ssrf.Allowlist
		wantError  string
	}{
		{name: "loopback blocked", url: "http://localhost:8080/mcp", wantError: "url scheme must be https"},
		{name: "development loopback", url: "http://localhost:8080/mcp", allowLocal: true},
		{name: "private blocked", url: "http://10.23.0.4:8080/mcp", wantError: "url scheme must be https"},
		{name: "private allowed", url: "http://10.23.0.4:8080/mcp", allowlist: allowlist},
		{
			name: "wrong port blocked", url: "http://10.23.0.4:8081/mcp",
			allowlist: allowlist, wantError: "url scheme must be https",
		},
		{
			name: "wrong address blocked", url: "http://10.23.0.5:8080/mcp",
			allowlist: allowlist, wantError: "url scheme must be https",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := compileAssistantPreset(CreateAssistantAgentInput{
				MCPURL: test.url,
				AgentConfigOptions: agentconfig.CompileOptions{
					AllowInsecureLocalMCPHTTP: test.allowLocal,
					EndpointAllowlist:         test.allowlist,
					ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
						return agentconfig.ResolvedModelSelection{}, errors.New("must use the assistant's selected model")
					},
					ValidateSecretID: func(uuid.UUID, secrets.Kind) error {
						return errors.New("must use the assistant's dedicated secret")
					},
				},
			}, model, uuid.New())
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				return
			}
			require.NoError(t, err)
			require.Contains(t, compiled.Source, test.url)
		})
	}
}

func TestAssistantPresetUsesOnlyReferencedCredentialAndModel(t *testing.T) {
	modelID, secretID := uuid.New(), uuid.New()
	compiled, err := compileAssistantPreset(CreateAssistantAgentInput{
		MCPURL: "https://api.example.com/mcp",
	}, modelstore.ConfiguredModelSummaryRecord{ID: modelID, Name: "model", ProviderConfigName: "provider"}, secretID)
	require.NoError(t, err)
	contract, err := agentconfig.RuntimeContractFromCompiled(compiled.CanonicalJSON, compiled.Hash)
	require.NoError(t, err)
	require.Equal(t, modelID, contract.Model.ConfiguredModelID)
	require.Empty(t, contract.MachineSources)
	webFetch := slices.IndexFunc(contract.Tools, func(tool agentconfig.RuntimeTool) bool {
		return tool.Name == "web_fetch"
	})
	require.NotEqual(t, -1, webFetch)
	require.Equal(t, toolpermission.ModeAlwaysAllow, contract.Tools[webFetch].Permission.Mode)
	require.Len(t, contract.MCPServers, 1)
	server := contract.MCPServers[0]
	require.Equal(t, secretID, server.Auth.SecretID)
	for _, name := range []string{"agents_list", "agents_launch", "secrets_delete", "a_tool_added_later"} {
		tool, ok := server.ResolveTool(name)
		require.True(t, ok)
		require.Equal(t, toolpermission.ModeAlwaysAllow, tool.Permission.Mode, name)
	}
	require.True(t, server.DefersAnyTool())
	for _, name := range []string{AssistantOpenPageTool, AssistantPointAtTool} {
		index := slices.IndexFunc(contract.Tools, func(tool agentconfig.RuntimeTool) bool { return tool.Name == name })
		require.NotEqual(t, -1, index, name)
		pageTool := contract.Tools[index]
		require.Equal(t, toolpermission.ModeAlwaysAllow, pageTool.Permission.Mode, name)
		require.NotNil(t, pageTool.TimeoutSeconds, name)
		require.Positive(t, *pageTool.TimeoutSeconds, name)
	}
	require.Contains(t, compiled.Source, "cannot click")
	require.Contains(t, compiled.Source, "untrusted data")
}
