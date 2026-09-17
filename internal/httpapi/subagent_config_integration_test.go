//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

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
		CompilerVersion: base.CompilerVersion, EffectiveDefinitionHash: base.EffectiveDefinitionHash,
	})
	require.NoError(t, err)
	for index, config := range []executionstore.AgentConfigRecord{base, derived} {
		child := spawnHTTPSubagentForTest(t, ctx, store, parent.Agent, config.ID,
			fmt.Sprintf("child-%d", index), fmt.Sprintf("worker-%d", index))
		configID := testPublicID(t, publicid.KindAgentConfig, config.ID)
		response := requestJSONWithHeaders(t, handler, http.MethodGet,
			project.ProjectPath+"/agent-configs/"+configID, "", "", http.StatusOK, authHeaders(project.AdminToken))
		if config.Source == "" {
			require.NotContains(t, response, "source")
			require.NotContains(t, response, "source_format")
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
