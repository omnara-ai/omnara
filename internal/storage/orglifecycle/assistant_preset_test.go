package orglifecycle

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

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
