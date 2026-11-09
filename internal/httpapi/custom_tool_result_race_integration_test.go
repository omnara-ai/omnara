//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/stretchr/testify/require"
)

type gatedToolResultBlobs struct {
	blobstore.Store
	entered, release chan struct{}
}

func (b *gatedToolResultBlobs) PutBlob(ctx context.Context, key string, content []byte) (blobstore.Metadata, error) {
	close(b.entered)
	select {
	case <-b.release:
	case <-ctx.Done():
		return blobstore.Metadata{}, ctx.Err()
	}
	return b.Store.PutBlob(ctx, key, content)
}

func TestCustomToolResultCompletionAfterReadyPreflight(t *testing.T) {
	for _, winner := range []string{"result", "cancellation"} {
		t.Run(winner, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			blobs := &gatedToolResultBlobs{
				Store: integrationblob.MustOpen(t, ctx), entered: make(chan struct{}), release: make(chan struct{}),
			}
			store := storage.NewStore(pool, storage.WithSecretKeyWrapper(integrationKeyWrapper()), storage.WithBlobStore(blobs))
			handler := newIntegrationHTTPHandler(mustNewServer(t, store).Handler(), pool, store)
			project := bootstrapPublicHTTPProject(t, handler, "custom-result-race")
			launch := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID,
				project.AdminUserUUID, "custom-result-race")
			callID := createHTTPCustomToolCallForAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID,
				project.AdminUserUUID, launch.Agent)
			path := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID) +
				"/tool-calls/" + testPublicID(t, publicid.KindToolCall, callID) + "/result"
			body := `{"outcome":"succeeded","content_blocks":[{"type":"media","media_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(testPNGBytes) + `"}]}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+project.AdminToken)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); handler.ServeHTTP(response, request) }()
			select {
			case <-blobs.entered:
			case <-ctx.Done():
				t.Fatal("result did not pass preflight and reach upload")
			}
			if winner == "result" {
				requestJSONWithHeaders(t, handler, http.MethodPost, path,
					`{"outcome":"succeeded","content_blocks":[{"type":"text","text":"winning result"}]}`, "", http.StatusCreated, authHeaders(project.AdminToken))
			} else {
				_, err := store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
					ProjectID: project.ProjectUUID, AgentID: launch.Agent.ID,
				})
				require.NoError(t, err)
			}
			before, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, launch.Agent.ID, callID)
			require.NoError(t, err)
			close(blobs.release)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("result request did not finish")
			}
			require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			var failure struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &failure))
			require.Equal(t, "tool_call_already_completed", failure.Code)
			after, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, launch.Agent.ID, callID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			var results, events int
			require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*), count(event.id)
FROM tool_call_results result
LEFT JOIN agent_events event ON event.agent_id=result.agent_id AND event.tool_call_result_id=result.id
WHERE result.agent_id=$1 AND result.tool_call_id=$2`, launch.Agent.ID, callID).Scan(&results, &events))
			require.Equal(t, 1, results)
			require.Equal(t, 1, events)
			if winner == "result" {
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, after.Outcome)
				require.Contains(t, string(after.ResultContentParts), "winning result")
			} else {
				require.Equal(t, executionstore.ToolResultOutcomeCanceled, after.Outcome)
			}
		})
	}
}
