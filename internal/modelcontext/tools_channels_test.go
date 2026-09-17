package modelcontext

import (
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestImplicitChannelToolkitTracksIndependentReadAndSendGrants(t *testing.T) {
	t.Parallel()
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		read, send bool
	}{
		{"receive only", false, false},
		{"read only", true, false},
		{"send only", false, true},
		{"read and send", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eligibility := integrationstore.AgentChannelToolEligibility{List: true, Read: tc.read, Send: tc.send}
			contract, err := WithImplicitChannelTools(agentconfig.RuntimeContract{},
				eligibility)
			require.NoError(t, err)
			// Reconstructing the context must neither duplicate tools nor restore the legacy send path.
			contract, err = WithImplicitChannelTools(contract, eligibility)
			require.NoError(t, err)
			want := map[string]bool{
				toolcatalog.ToolNameListChannels: true, toolcatalog.ToolNameGetChannel: true,
				toolcatalog.ToolNameSetCurrentChannel: true,
				toolcatalog.ToolNameReadFile:          true, toolcatalog.ToolNameSearchFiles: true,
				toolcatalog.ToolNameReadChannel: tc.read, toolcatalog.ToolNameSendChannelMessage: tc.send,
			}
			seen := make(map[string]bool)
			for _, tool := range contract.Tools {
				require.True(t, want[tool.Name], "unexpected tool %s", tool.Name)
				require.False(t, seen[tool.Name], "duplicate tool %s", tool.Name)
				seen[tool.Name] = true
				entry, ok := catalog.Lookup(tool.Name)
				require.True(t, ok)
				require.JSONEq(t, string(entry.InputSchema), string(tool.InputSchema))
			}
			for name, enabled := range want {
				require.Equal(t, enabled, seen[name], name)
			}
		})
	}
}

func TestChannelEligibilityPreservesSubagentsAndFileToolsFromPinnedConfig(t *testing.T) {
	t.Parallel()
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(`
instruction: Use configured tools and independent channel grants.
model:
  provider_config: test
  name: test
tools:
  upload_file: {}
  download_file: {}
  read_file:
    enabled: false
  search_files:
    permission:
      mode: always_ask
  spawn_agent:
    permission:
      mode: always_ask
  stop_agent:
    enabled: false
subagents:
  fork:
    type: self
`), agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: testIDN(100).String()}, nil
		},
	})
	require.NoError(t, err)
	for name := range compiled.Compiled.Tools {
		require.False(t, toolcatalog.IsBindingManagedTool(name), "channel eligibility is not compiled into the config")
	}
	for _, eligibility := range []integrationstore.AgentChannelToolEligibility{
		{List: true, Read: true, Send: true}, {}, {List: true},
	} {
		// Reconstruct the same pinned config independently for each live grant
		// state. Revocation must not retain channel tools from the previous turn.
		contract, err := agentconfig.RuntimeContractFromCompiled(
			compiled.CanonicalJSON, compiled.CompilerVersion, compiled.Hash)
		require.NoError(t, err)
		contract, err = WithImplicitChannelTools(contract, eligibility)
		require.NoError(t, err)
		specs, err := RuntimeContractToolSpecs(t.Context(), nil, testProjectID, testAgentID, contract, time.Time{})
		require.NoError(t, err)
		require.Equal(t, eligibility.List, HasTool(specs, toolcatalog.ToolNameListChannels))
		require.Equal(t, eligibility.Send, HasTool(specs, toolcatalog.ToolNameSendChannelMessage))
		require.Equal(t, eligibility.Read, HasTool(specs, toolcatalog.ToolNameReadChannel))
		require.True(t, HasTool(specs, toolcatalog.ToolNameUploadFile))
		require.True(t, HasTool(specs, toolcatalog.ToolNameDownloadFile))
		require.False(t, HasTool(specs, toolcatalog.ToolNameReadFile))
		require.True(t, HasTool(specs, toolcatalog.ToolNameSearchFiles))
		for _, name := range toolcatalog.SubagentToolNames() {
			require.Equal(t, name != toolcatalog.ToolNameStopAgent, HasTool(specs, name), name)
		}
		for _, spec := range specs {
			if spec.Name == toolcatalog.ToolNameSearchFiles {
				require.Equal(t, toolpermission.ModeAlwaysAsk, spec.Permission.Mode)
			}
			if spec.Name == toolcatalog.ToolNameSpawnAgent {
				require.Equal(t, toolpermission.ModeAlwaysAsk, spec.Permission.Mode)
				require.Contains(t, string(spec.InputSchema), `"enum":["fork"]`)
			}
		}
	}
}
