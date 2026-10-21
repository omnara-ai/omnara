//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/stretchr/testify/require"
)

type inputArtifactBlobs struct {
	blobstore.Store
	mu   sync.Mutex
	keys []string
}

func (b *inputArtifactBlobs) PutBlob(ctx context.Context, key string, content []byte) (blobstore.Metadata, error) {
	b.mu.Lock()
	b.keys = append(b.keys, key)
	b.mu.Unlock()
	return b.Store.PutBlob(ctx, key, content)
}

func TestAgentInputPreparedArtifacts(t *testing.T) {
	for _, scenario := range []string{"full and retry", "race", "validation", "deferred rejection", "replay"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			blobs := &inputArtifactBlobs{Store: integrationblob.MustOpen(t, ctx)}
			wrapper := integrationKeyWrapper()
			store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper), storage.WithBlobStore(blobs))
			handler := newIntegrationHTTPHandler(mustNewServer(t, store, WithSecretKeyWrapper(wrapper)).Handler(), pool, store)
			project := bootstrapPublicHTTPProject(t, handler, "prepared-artifacts")
			launch := launchPublicHTTPAgent(t, handler, project, "prepared-artifacts", project.AdminToken, http.StatusCreated)
			public := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
			agent, err := publicid.Decode(publicid.KindAgent, public)
			require.NoError(t, err)
			var projectID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT project_id FROM agents WHERE id=$1`, agent).Scan(&projectID))
			path := project.ProjectPath + "/agents/" + public + "/inputs"
			image := `{"type":"media","media_type":"image/png","filename":"image.png","data":"` +
				base64.StdEncoding.EncodeToString(testPNGBytes) + `"}`
			body := `{"content_blocks":[` + image + `,` + image + `]}`
			send := func(body, key string, status int) map[string]any {
				t.Helper()
				return requestJSONWithHeaders(t, handler, http.MethodPost, path, body, key, status, authHeaders(project.AdminToken))
			}
			check := func(count int) {
				t.Helper()
				var rows, referenced int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE agent_id=$1`, agent).Scan(&rows))
				require.Equal(t, count, rows)
				require.NoError(t, pool.QueryRow(ctx,
					`SELECT count(*) FROM content_blocks WHERE agent_id=$1 AND artifact_id IS NOT NULL`, agent).Scan(&referenced))
				require.Equal(t, count, referenced)
				files, err := store.Artifacts().ListFiles(ctx, agent, nil, "", 0, 100)
				require.NoError(t, err)
				require.Len(t, files, count)
				live := 0
				for _, key := range blobs.keys {
					_, _, err := blobs.GetBlob(ctx, key)
					if err == nil {
						live++
					} else {
						require.ErrorIs(t, err, blobstore.ErrNotFound)
					}
				}
				require.Equal(t, count, live)
			}
			if scenario == "validation" {
				send(`{"content_blocks":[`+image+`,{"type":"text","text":"invalid metadata","metadata":{"":"invalid"}}]}`,
					"invalid", http.StatusBadRequest)
				require.NotEmpty(t, blobs.keys)
				check(0)
				return
			}
			if scenario == "deferred rejection" {
				_, err := pool.Exec(ctx, `
CREATE FUNCTION reject_input_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'reject input' USING ERRCODE='23514'; END $$;
CREATE CONSTRAINT TRIGGER reject_input_commit AFTER INSERT ON agent_inputs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_input_commit()`)
				require.NoError(t, err)
				send(body, "deferred", http.StatusInternalServerError)
				require.NotEmpty(t, blobs.keys)
				check(0)
				return
			}
			var first uuid.UUID
			for range 49 {
				input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
					ProjectID: projectID, AgentID: agent, ContentBlocks: []byte(`[{"type":"text","text":"pending"}]`),
				})
				require.NoError(t, err)
				first = input.ID
			}
			if scenario == "race" {
				start := make(chan struct{})
				codes := make(chan int, 2)
				var wg sync.WaitGroup
				for i := range 2 {
					wg.Go(func() {
						<-start
						req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Authorization", "Bearer "+project.AdminToken)
						req.Header.Set("Idempotency-Key", fmt.Sprintf("race-%d", i))
						response := httptest.NewRecorder()
						handler.ServeHTTP(response, req)
						codes <- response.Code
					})
				}
				close(start)
				wg.Wait()
				close(codes)
				var results []int
				for code := range codes {
					results = append(results, code)
				}
				require.ElementsMatch(t, []int{http.StatusCreated, http.StatusConflict}, results)
				check(2)
				return
			}
			var original map[string]any
			if scenario == "replay" {
				original = send(body, "replay", http.StatusCreated)
			} else {
				send(`{"content_blocks":[{"type":"text","text":"last"}]}`, "last", http.StatusCreated)
			}
			for i := range 2 {
				rejected := send(body, fmt.Sprintf("rejected-%d", i), http.StatusConflict)
				require.Equal(t, "agent_backlog_full", rejected["code"])
			}
			if scenario == "replay" {
				replay := send(body, "replay", http.StatusOK)
				require.Equal(t, original, replay)
				changed := strings.ReplaceAll(body, base64.StdEncoding.EncodeToString(testPNGBytes),
					base64.StdEncoding.EncodeToString([]byte("other")))
				retried := send(changed, "replay", http.StatusOK)
				require.Equal(t, original, retried)
				check(2)
				return
			}
			check(0)
			require.NoError(t, store.Execution().CancelQueuedBacklogInput(ctx, executionstore.CancelQueuedBacklogInputInput{
				ProjectID: projectID, AgentID: agent, InputID: first,
			}))
			send(body, "rejected-0", http.StatusCreated)
			check(2)
		})
	}
}

func TestInputArtifactCleanupUnknownOutcome(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	blobs := &inputArtifactBlobs{Store: integrationblob.MustOpen(t, ctx)}
	wrapper := integrationKeyWrapper()
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper), storage.WithBlobStore(blobs))
	server := mustNewServer(t, store, WithSecretKeyWrapper(wrapper))
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "unknown-artifact")
	launch := launchPublicHTTPAgent(t, handler, project, "unknown-artifact", project.AdminToken, http.StatusCreated)
	public := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launch["agent"])["id"])
	agent, err := publicid.Decode(publicid.KindAgent, public)
	require.NoError(t, err)
	var projectID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT project_id FROM agents WHERE id=$1`, agent).Scan(&projectID))
	blocks := []byte(`[{"type":"media","media_type":"image/png","data":"` +
		base64.StdEncoding.EncodeToString(testPNGBytes) + `"}]`)
	_, prepared, err := server.prepareInputMedia(ctx, agent, blocks)
	require.NoError(t, err)
	server.cleanupInputMedia(ctx, projectID, agent, prepared,
		errors.Join(storeerr.ErrCommitOutcomeUnknown, io.ErrUnexpectedEOF))
	require.Len(t, blobs.keys, 1)
	_, _, err = blobs.GetBlob(ctx, blobs.keys[0])
	require.NoError(t, err)
	server.cleanupInputMedia(ctx, projectID, agent, prepared, storeerr.ErrInvalidRequest)
	_, _, err = blobs.GetBlob(ctx, blobs.keys[0])
	require.ErrorIs(t, err, blobstore.ErrNotFound)
}
