package modelcontext

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
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
				toolcatalog.ToolNameReadChannel:       tc.read, toolcatalog.ToolNameSendChannelMessage: tc.send,
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
