//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

// A result that arrives after the deadline is refused whether or not the expiry job has
// reached the call yet, and the call keeps its timeout result either way.
func TestCustomToolResultAfterTimeout(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
	for name, sweptFirst := range map[string]bool{"swept-first": true, "result-first": false} {
		project := bootstrapPublicHTTPProject(t, handler, "custom-timeout-"+name)
		launch := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID,
			project.AdminUserUUID, "custom-timeout")
		toolCall, runtime := recordHTTPToolCallForAgent(
			t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, launch.Agent,
			"lookup_customer", toolcatalog.ToolTypeCustom, json.RawMessage(`{"email":"ada@example.com"}`), new(45),
		)
		ready, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: project.ProjectUUID, AgentID: launch.Agent.ID, ID: toolCall.ID, RuntimeLockID: runtime.ID,
		})
		require.NoError(t, err, name)
		require.Equal(t, executionstore.ToolCallStateReady, ready.State, name)
		_, err = pool.Exec(ctx,
			`UPDATE tool_calls SET expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, toolCall.ID)
		require.NoError(t, err, name)
		if sweptFirst {
			expired, _, err := store.Execution().ExpireCustomToolCalls(ctx, 10)
			require.NoError(t, err, name)
			require.Positive(t, expired, name)
		}

		path := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID) +
			"/tool-calls/" + testPublicID(t, publicid.KindToolCall, toolCall.ID)
		late := requestJSONWithHeaders(t, handler, http.MethodPost, path+"/result",
			`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"Too late."}]}`, "", http.StatusConflict,
			authHeaders(project.AdminToken))
		require.Equal(t, "tool_call_already_completed", late["code"], name)
		fetched := requestJSONWithHeaders(t, handler, http.MethodGet, path, "", "", http.StatusOK,
			authHeaders(project.AdminToken))
		require.Equal(t, "completed", fetched["state"], name)
		record, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, launch.Agent.ID, toolCall.ID)
		require.NoError(t, err, name)
		require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome, name)
		require.JSONEq(t, `[{"type":"text","text":"No result was submitted within 45 seconds."}]`,
			string(record.ResultContentParts), name)
	}
}
