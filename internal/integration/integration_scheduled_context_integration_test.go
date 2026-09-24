//go:build integration

package integration

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledLaunchPreservesToolsAndSavesReplyContext(t *testing.T) {
	for _, provider := range []string{integrationdefinition.ProviderSlack, integrationdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			f := newScheduledProviderJourney(t, provider)
			base, found, err := f.store.Execution().GetAgentConfig(t.Context(), f.ids.ProjectID, f.profile.CurrentConfigID)
			require.NoError(t, err)
			require.True(t, found)
			integration, err := f.store.Integrations().GetProjectIntegration(t.Context(), f.ids.ProjectID, f.integrationID)
			require.NoError(t, err)
			source := base.Source + `
tools:
  int__chat__read: {}
  int__chat__post_message: {}
`
			compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
				ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
					return agentconfig.ResolvedModelSelection{ConfiguredModelID: base.ConfiguredModelID}, nil
				},
				ResolveIntegrationName: func(string) (agentconfig.IntegrationResolution, error) {
					return agentconfig.IntegrationResolution{
						IntegrationID:   f.integrationID,
						IntegrationType: integration.IntegrationType,
					}, nil
				},
			})
			require.NoError(t, err)
			config, err := f.store.Execution().CreateAgentConfig(t.Context(), executionstore.CreateAgentConfigInput{
				ProjectID: f.ids.ProjectID, Source: source, ConfiguredModelID: base.ConfiguredModelID,
				CompiledDefinition:      compiled.CanonicalJSON,
				EffectiveDefinitionHash: compiled.Hash,
			})
			require.NoError(t, err)
			_, err = f.store.Execution().RetargetAgentProfile(t.Context(), executionstore.RetargetAgentProfileInput{
				ProjectID: f.ids.ProjectID, ProfileID: f.profile.ID,
				ExpectedCurrentConfigID: base.ID, ConfigID: config.ID,
			})
			require.NoError(t, err)
			receipt := f.fire()
			results, err := f.consumer.Consume(t.Context(), receipt.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			launched := results[0].Launch
			actualConfig, found, err := f.store.Execution().GetAgentConfig(
				t.Context(), f.ids.ProjectID, launched.Agent.CurrentConfigID,
			)
			require.NoError(t, err)
			require.True(t, found)
			var actual agentconfig.Compiled
			require.NoError(t, json.Unmarshal(actualConfig.CompiledDefinition, &actual))
			for _, name := range []string{"int__chat__read", "int__chat__post_message"} {
				require.Equal(t, compiled.Compiled.Tools[name], actual.Tools[name], "explicit tool policy stays intact")
			}
			target, found, err := f.store.Integrations().GetAgentIntegrationConversation(
				t.Context(), f.ids.ProjectID, launched.Agent.ID, f.integrationID,
			)
			require.NoError(t, err)
			require.True(t, found)
			kind, ref, err := f.provider.root.Conversation()
			require.NoError(t, err)
			require.Equal(t, kind, target.Kind)
			require.Equal(t, ref, target.Ref)
			var blocks []struct {
				Text     string            `json:"text"`
				Metadata map[string]string `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(launched.InputContentBlocks, &blocks))
			require.Len(t, blocks, 2)
			require.Equal(t, "Review today and post your findings.", blocks[0].Text)
			require.Equal(t, "true", blocks[1].Metadata["omnara_hidden"])
			expected, err := json.Marshal(f.provider.root)
			require.NoError(t, err)
			require.Contains(t, blocks[1].Text, `"integration":"chat"`)
			require.Contains(t, blocks[1].Text, `"source_conversation":`+string(expected))
		})
	}
}
