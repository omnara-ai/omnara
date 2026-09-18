package agentconfigcompile

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestDeriveSubagentConfigWithoutSource(t *testing.T) {
	maxDepth := 2
	base := agentconfig.Compiled{
		Instruction: "Help.",
		Model:       agentconfig.ModelCompiled{ConfiguredModelID: uuid.New()},
		Tools: map[string]agentconfig.ToolCompiled{
			"spawn_agent": {Enabled: true},
			"read_agent":  {Enabled: true},
			"skill":       {Enabled: false},
		},
		Subagents: map[string]agentconfig.SubagentCompiled{"worker": {Type: agentconfig.SubagentTypeSelf}},
	}
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	for _, source := range []string{"", "not valid authored config"} {
		body, err := DeriveSubagentConfig(executionstore.AgentConfigRecord{
			Source: source, SourceFormat: "yaml", CompiledDefinition: raw,
		}, agentconfig.SubagentCompiled{InstructionAppend: "Investigate."},
			agentconfig.SubagentDepth{Depth: 1, MaxDepth: &maxDepth}, nil)
		require.NoError(t, err)
		require.Empty(t, body.Source)
		require.Empty(t, body.SourceFormat)
		leaf, err := DeriveSubagentConfig(executionstore.AgentConfigRecord{
			CompiledDefinition: body.CompiledDefinition,
		}, agentconfig.SubagentCompiled{}, agentconfig.SubagentDepth{Depth: 2, MaxDepth: &maxDepth}, nil)
		require.NoError(t, err)
		var compiled agentconfig.Compiled
		require.NoError(t, json.Unmarshal(leaf.CompiledDefinition, &compiled))
		require.Equal(t, "Help.\n\nInvestigate.", compiled.Instruction)
		require.Empty(t, compiled.Subagents)
		require.NotContains(t, compiled.Tools, "spawn_agent")
		require.Equal(t, base.Tools["read_agent"], compiled.Tools["read_agent"])
		require.Equal(t, base.Tools["skill"], compiled.Tools["skill"])
	}
}
