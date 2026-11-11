package orglifecycle

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAssistantPresetPreservesOperatorPolicy(t *testing.T) {
	model := modelstore.ConfiguredModelSummaryRecord{ID: uuid.New(), Name: "model", ProviderConfigName: "provider"}
	for _, allowLocal := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "allowed"}[allowLocal], func(t *testing.T) {
			compiled, err := compileAssistantPreset(CreateAssistantAgentInput{
				MCPURL: "http://localhost:8080/mcp",
				AgentConfigOptions: agentconfig.CompileOptions{
					AllowInsecureLocalMCPHTTP: allowLocal,
					ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
						return agentconfig.ResolvedModelSelection{}, errors.New("must use the assistant's selected model")
					},
					ValidateSecretID: func(uuid.UUID, secrets.Kind) error {
						return errors.New("must use the assistant's dedicated secret")
					},
				},
			}, model, uuid.New())
			if !allowLocal {
				require.ErrorContains(t, err, "targets loopback")
				return
			}
			require.NoError(t, err)
			require.Contains(t, compiled.Source, "http://localhost:8080/mcp")
		})
	}
}

func TestAssistantPresetUsesOnlyReferencedCredentialAndModel(t *testing.T) {
	modelID, secretID := uuid.New(), uuid.New()
	compiled, err := compileAssistantPreset(CreateAssistantAgentInput{
		MCPURL: "https://api.example.com/mcp", ReadOnlyTools: []string{"agents_list"},
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
	require.Equal(t, toolpermission.ModeAlwaysAsk, contract.Tools[webFetch].Permission.Mode)
	require.Len(t, contract.MCPServers, 1)
	server := contract.MCPServers[0]
	require.Equal(t, secretID, server.Auth.SecretID)
	read, ok := server.ResolveTool("agents_list")
	require.True(t, ok)
	require.Equal(t, toolpermission.ModeAlwaysAllow, read.Permission.Mode)
	write, ok := server.ResolveTool("agents_launch")
	require.True(t, ok)
	require.Equal(t, toolpermission.ModeAlwaysAsk, write.Permission.Mode)
	require.True(t, server.DefersAnyTool())
	require.Contains(t, compiled.Source, "cannot click")
	require.Contains(t, compiled.Source, "untrusted data")
}
