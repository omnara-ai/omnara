//go:build integration

package integration

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestScheduledLaunchPreservesToolsAndSavesReplyContext(t *testing.T) {
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			f := newScheduledProviderJourney(t, provider)
			base, found, err := f.store.Execution().GetAgentConfig(t.Context(), f.ids.ProjectID, f.profile.CurrentConfigID)
			require.NoError(t, err)
			require.True(t, found)
			appID, err := publicid.Encode(publicid.KindProjectApp, f.appID)
			require.NoError(t, err)
			source := base.Source + `
tools:
  app__chat__read: {}
  app__chat__post_message: {}
`
			compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{
				ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
					return agentconfig.ResolvedModelSelection{ConfiguredModelID: base.ConfiguredModelID.String()}, nil
				},
				ResolveAppName: func(string) (agentconfig.AppResolution, error) {
					return agentconfig.AppResolution{AppID: appID, Definition: "omnara." + provider}, nil
				},
			})
			require.NoError(t, err)
			config, err := f.store.Execution().CreateAgentConfig(t.Context(), executionstore.CreateAgentConfigInput{
				ProjectID: f.ids.ProjectID, Source: source, ConfiguredModelID: base.ConfiguredModelID,
				CompiledDefinition: compiled.CanonicalJSON, CompilerVersion: compiled.CompilerVersion,
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
			for _, name := range []string{"app__chat__read", "app__chat__post_message"} {
				require.Equal(t, compiled.Compiled.Tools[name], actual.Tools[name], "explicit tool policy stays intact")
			}
			target, found, err := f.store.Integrations().GetAgentAppToolContext(
				t.Context(), f.ids.ProjectID, launched.Agent.ID, f.appID,
			)
			require.NoError(t, err)
			require.True(t, found)
			kind, ref, err := f.provider.root.Conversation()
			require.NoError(t, err)
			require.Equal(t, kind, target.ProviderRefKind)
			require.Equal(t, ref, target.ProviderRef)
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
			require.Contains(t, blocks[1].Text, `"app":"chat"`)
			require.Contains(t, blocks[1].Text, `"reply_address":`+string(expected))
		})
	}
}
