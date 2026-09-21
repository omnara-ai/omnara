//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestSubagentConfigReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "readonly-subagent")
	store := newIntegrationStore(pool)
	user := createHTTPInteractionUser(t, ctx, pool, store, project.OrgUUID, project.ProjectUUID, "readonly-subagent")
	parent := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, user.ID, "readonly-subagent")
	base := parent.AgentConfig
	derived, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:         project.ProjectUUID,
		ConfiguredModelID: base.ConfiguredModelID, CompiledDefinition: base.CompiledDefinition,
		EffectiveDefinitionHash: base.EffectiveDefinitionHash,
	})
	require.NoError(t, err)
	for index, config := range []executionstore.AgentConfigRecord{base, derived} {
		child := spawnHTTPSubagentForTest(t, ctx, store, parent.Agent, config.ID,
			fmt.Sprintf("child-%d", index), fmt.Sprintf("worker-%d", index))
		configID := testPublicID(t, publicid.KindAgentConfig, config.ID)
		response := requestJSONWithHeaders(t, handler, http.MethodGet,
			project.ProjectPath+"/agent-configs/"+configID, "", "", http.StatusOK, authHeaders(project.AdminToken))
		compiled, err := json.Marshal(response["compiled_definition"])
		require.NoError(t, err)
		requirePublicCompiledDefinition(t, config, compiled)
		if config.Source == "" {
			require.NotContains(t, response, "source")
			require.NotContains(t, response, "source_format")
		} else {
			require.Equal(t, config.Source, response["source"])
		}
		before := child.CurrentConfigID
		requestJSONWithHeaders(t, handler, http.MethodPost,
			project.ProjectPath+"/agents/"+testPublicID(t, publicid.KindAgent, child.ID)+"/config",
			`{"source_format":"yaml","source":`+quotedJSONString(base.Source)+`}`,
			"", http.StatusBadRequest, authHeaders(project.AdminToken))
		updated, err := store.Execution().GetAgentInProject(ctx, project.ProjectUUID, child.ID)
		require.NoError(t, err)
		require.Equal(t, before, updated.CurrentConfigID)
	}
}

func TestDerivedAgentConfigResponse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "compiled-subagent")
	store := newIntegrationStore(pool)
	user := createHTTPInteractionUser(t, ctx, pool, store, project.OrgUUID, project.ProjectUUID, "compiled-subagent")
	parent := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, user.ID, "compiled-parent")
	for _, kind := range []string{agentconfig.SubagentTypeSelf, agentconfig.SubagentTypeProfile} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			source := fmt.Sprintf(`instruction: %s base
model: {provider_config: openai-prod, name: http-test}
subagents: {helper: {type: self}}
tools: {skill: {enabled: false}}
`, kind)
			created := requestJSONWithHeaders(t, handler, http.MethodPost, project.ProjectPath+"/agent-configs",
				`{"source_format":"yaml","source":`+quotedJSONString(source)+`}`, "", http.StatusCreated,
				authHeaders(project.AdminToken))
			require.Equal(t, source, created["source"])
			createdID, ok := created["id"].(string)
			require.True(t, ok)
			configID, err := publicid.Decode(publicid.KindAgentConfig, createdID)
			require.NoError(t, err)
			base, found, err := store.Execution().GetAgentConfig(ctx, project.ProjectUUID, configID)
			require.NoError(t, err)
			require.True(t, found)
			createdCompiled, err := json.Marshal(created["compiled_definition"])
			require.NoError(t, err)
			requirePublicCompiledDefinition(t, base, createdCompiled)
			parentAgent := parent.Agent
			if kind == agentconfig.SubagentTypeProfile {
				profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
					ProjectID: project.ProjectUUID, Name: "child-base", CurrentConfigID: base.ID,
				})
				require.NoError(t, err)
				profileResponse := requestJSONWithHeaders(t, handler, http.MethodGet,
					project.ProjectPath+"/agent-profiles/"+testPublicID(t, publicid.KindAgentProfile, profile.ID),
					"", "", http.StatusOK, authHeaders(project.AdminToken))
				profileJSON, err := json.Marshal(profileResponse["current_config"])
				require.NoError(t, err)
				var current struct {
					Compiled json.RawMessage `json:"compiled_definition"`
				}
				require.NoError(t, json.Unmarshal(profileJSON, &current))
				requirePublicCompiledDefinition(t, base, current.Compiled)
				storedProfile, err := store.Execution().GetAgentProfile(ctx, project.ProjectUUID, profile.ID)
				require.NoError(t, err)
				base = storedProfile.CurrentConfig
			} else {
				parentAgent, err = store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
					ProjectID: project.ProjectUUID, CurrentConfigID: base.ID,
				})
				require.NoError(t, err)
			}
			maxTokens := 128
			body, err := agentconfigcompile.DeriveSubagentConfig(base, agentconfig.SubagentCompiled{
				Type: kind, InstructionAppend: "Child instructions.",
				Model: &agentconfig.SubagentModelCompiled{DefaultMaxOutputTokens: &maxTokens},
			}, agentconfig.SubagentDepth{Depth: 1}, nil)
			require.NoError(t, err)
			derived, err := store.Execution().CreateAgentConfig(ctx, body.CreateInput(project.ProjectUUID))
			require.NoError(t, err)
			child := spawnHTTPSubagentForTest(t, ctx, store, parentAgent, derived.ID, "child-"+kind, "helper")
			response := requestJSONWithHeaders(t, handler, http.MethodGet,
				project.ProjectPath+"/agent-configs/"+testPublicID(t, publicid.KindAgentConfig, child.CurrentConfigID),
				"", "", http.StatusOK, authHeaders(project.AdminToken))
			require.NotContains(t, response, "source")
			require.NotContains(t, response, "source_format")
			raw, err := json.Marshal(response["compiled_definition"])
			require.NoError(t, err)
			requirePublicCompiledDefinition(t, derived, raw)
			var compiled agentconfig.Compiled
			require.NoError(t, json.Unmarshal(derived.CompiledDefinition, &compiled))
			require.Equal(t, kind+" base\n\nChild instructions.", compiled.Instruction)
			require.Equal(t, &maxTokens, compiled.Model.DefaultMaxOutputTokens)
			require.Empty(t, compiled.Subagents)
			require.NotContains(t, compiled.Tools, "spawn_agent")
			require.True(t, compiled.Tools["read_agent"].Enabled)
			require.True(t, compiled.Tools["read_file"].Enabled)
			require.Contains(t, compiled.Tools, "skill")
			require.False(t, compiled.Tools["skill"].Enabled)
		})
	}
}

func requirePublicCompiledDefinition(t *testing.T, config executionstore.AgentConfigRecord, raw []byte) {
	t.Helper()
	var stored, response map[string]any
	require.NoError(t, json.Unmarshal(config.CompiledDefinition, &stored))
	require.NoError(t, json.Unmarshal(raw, &response))
	storedModel, ok := stored["model"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, config.ConfiguredModelID.String(), storedModel["configured_model_id"])
	storedModel["configured_model_id"] = testPublicID(t, publicid.KindConfiguredModel, config.ConfiguredModelID)
	require.Equal(t, stored, response)
}
