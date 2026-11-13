package agentconfigcompile_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestDeriveSubagentConfigWithoutSource(t *testing.T) {
	maxDepth := 2
	base := agentconfig.Compiled{
		Instruction: "Help.",
		Model:       agentconfig.ModelCompiled{ConfiguredModelID: uuid.New()},
		Tools: map[string]agentconfig.ToolCompiled{
			"spawn_agent":   {Enabled: true},
			"read_agent":    {Enabled: true},
			"storage_write": {Enabled: true},
			"skill":         {Enabled: false},
		},
		MemoryStores: []agentconfig.MemoryStoreCompiled{
			{ID: uuid.New(), Access: "read"},
			{ID: uuid.New(), Access: "read_write"},
		},
		Subagents: map[string]agentconfig.SubagentCompiled{"worker": {Type: agentconfig.SubagentTypeSelf}},
	}
	encoded, err := agentconfig.EncodeCompiled(base)
	require.NoError(t, err)
	for _, source := range []string{"", "not valid authored config"} {
		body, err := agentconfigcompile.DeriveSubagentConfig(executionstore.AgentConfigRecord{
			Source: source, SourceFormat: "yaml", CompiledDefinition: encoded.CanonicalJSON,
			EffectiveDefinitionHash: encoded.Hash,
		}, agentconfig.SubagentCompiled{Type: agentconfig.SubagentTypeSelf, InstructionAppend: "Investigate."},
			agentconfig.SubagentDepth{Depth: 1, MaxDepth: &maxDepth})
		require.NoError(t, err)
		require.Empty(t, body.Source)
		require.Empty(t, body.SourceFormat)
		leaf, err := agentconfigcompile.DeriveSubagentConfig(executionstore.AgentConfigRecord{
			CompiledDefinition:      body.CompiledDefinition,
			EffectiveDefinitionHash: body.DefinitionHash,
		}, agentconfig.SubagentCompiled{Type: agentconfig.SubagentTypeSelf},
			agentconfig.SubagentDepth{Depth: 2, MaxDepth: &maxDepth})
		require.NoError(t, err)
		var compiled agentconfig.Compiled
		require.NoError(t, json.Unmarshal(leaf.CompiledDefinition, &compiled))
		require.Equal(t, "Help.\n\nInvestigate.", compiled.Instruction)
		require.Equal(t, base.MemoryStores, compiled.MemoryStores)
		require.Empty(t, compiled.Subagents)
		require.NotContains(t, compiled.Tools, "spawn_agent")
		require.Equal(t, base.Tools["read_agent"], compiled.Tools["read_agent"])
		require.Equal(t, base.Tools["storage_write"], compiled.Tools["storage_write"])
		require.Equal(t, base.Tools["skill"], compiled.Tools["skill"])
	}
}

func TestDerivationsPreserveVerifiedRuntimeValues(t *testing.T) {
	encoded, err := agentconfig.EncodeCompiled(agentconfig.Compiled{
		Instruction: "Help.", Model: agentconfig.ModelCompiled{ConfiguredModelID: uuid.New()},
		Tools: map[string]agentconfig.ToolCompiled{"lookup": {
			Type: "custom", Enabled: true, Description: "Look up an item.",
			Permission:  toolpermission.Selection{Mode: toolpermission.ModeAlwaysAllow, Parameters: json.RawMessage(`{}`)},
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"const":9007199254740993}}}`),
		}},
	})
	require.NoError(t, err)
	var rounded any
	require.NoError(t, json.Unmarshal(encoded.CanonicalJSON, &rounded))
	legacy, err := json.Marshal(rounded)
	require.NoError(t, err)
	derivations := map[string]func(executionstore.AgentConfigRecord) (executionstore.CreateAgentConfigInput, error){
		"subagent": func(base executionstore.AgentConfigRecord) (executionstore.CreateAgentConfigInput, error) {
			body, err := agentconfigcompile.DeriveSubagentConfig(base,
				agentconfig.SubagentCompiled{Type: agentconfig.SubagentTypeSelf, InstructionAppend: "Investigate."},
				agentconfig.SubagentDepth{})
			return body.CreateInput(base.ProjectID), err
		},
		"integration": func(base executionstore.AgentConfigRecord) (executionstore.CreateAgentConfigInput, error) {
			body, err := agentconfigcompile.DeriveIntegrationConfig(t.Context(), nil, base.OrgID, base.ProjectID,
				agentconfig.CompileOptions{}, base, agentconfig.IntegrationCapabilitiesSource{})
			return body.CreateInput(base.ProjectID), err
		},
		"integration profile": func(base executionstore.AgentConfigRecord) (executionstore.CreateAgentConfigInput, error) {
			return integration.DeriveIntegrationProfileConfig(base,
				agentconfig.IntegrationCapabilitiesSource{}, agentconfig.CompileOptions{})
		},
	}
	for name, derive := range derivations {
		for kind, hash := range map[string]string{
			"exact": encoded.Hash, "legacy": agentconfig.DefinitionHash(legacy), "invalid": "mismatched", "missing": "",
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				base := executionstore.AgentConfigRecord{ID: uuid.New(), ProjectID: uuid.New(),
					CompiledDefinition: encoded.CanonicalJSON, EffectiveDefinitionHash: hash}
				derived, err := derive(base)
				if kind == "invalid" || kind == "missing" {
					require.ErrorContains(t, err, "hash mismatch")
					return
				}
				require.NoError(t, err)
				parent, err := agentconfig.RuntimeContractFromCompiled(base.CompiledDefinition, hash)
				require.NoError(t, err)
				child, err := agentconfig.RuntimeContractFromCompiled(derived.CompiledDefinition, derived.EffectiveDefinitionHash)
				require.NoError(t, err)
				require.Equal(t, parent.Tools, child.Tools)
			})
		}
	}
}
