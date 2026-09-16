//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

func TestLegacyDaemonArtifactEndpoints(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(
		pool, []storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
	)
	project := bootstrapPublicHTTPProject(t, handler, "legacy-artifact-endpoints")
	store := integrationStoreForHandler(t, handler)
	for _, tool := range []string{"upload_file", "download_file"} {
		t.Run(tool, func(t *testing.T) {
			var artifactID string
			fixture := createDaemonProcessFixtureWithToolInputBuilder(
				t, ctx, pool, store, project, time.Now(), "legacy-"+tool, tool, nil,
				func(agentID uuid.UUID) json.RawMessage {
					if tool == "upload_file" {
						return json.RawMessage(`{"path":"/artifacts","source":"report.txt"}`)
					}
					artifact, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
						ProjectID: project.ProjectUUID, AgentID: agentID, ContentType: "text/plain",
						Filename: "report.txt", Content: []byte("content"),
					})
					if err != nil {
						t.Fatal(err)
					}
					artifactID = testPublicID(t, publicid.KindArtifact, artifact.ID)
					input := map[string]string{"path": "/artifacts/" + artifactID, "destination": "report.txt"}
					raw, err := json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
					return raw
				},
			)
			if _, found, err := acceptDaemonProcessOfferForTest(
				ctx, store, fixture.authority(), fixture.ProcessUUID,
			); err != nil || !found {
				t.Fatalf("accept legacy process: found=%t err=%v", found, err)
			}
			root := "/api/v1/daemon/tool-calls/" + testPublicID(t, publicid.KindToolCall, fixture.ToolCallUUID)
			if tool == "upload_file" {
				legacy := requestLegacyArtifactEndpoint(
					t, handler, fixture.Token, http.MethodPost, root+"/artifact?filename=report.txt", http.StatusCreated,
				)
				var response map[string]any
				if err := json.Unmarshal(legacy.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				current := requestDaemonFileArtifactUpload(t, handler, fixture, "report.txt", []byte("content"), http.StatusCreated)
				if len(response) != 1 || response["artifact_id"] != current["artifact_id"] {
					t.Fatalf("legacy upload differs: %v, current %v", response, current)
				}
				return
			}
			legacy := requestLegacyArtifactEndpoint(
				t, handler, fixture.Token, http.MethodGet, root+"/artifacts/"+artifactID+"/content", http.StatusOK,
			)
			current := requestDaemonFileArtifactDownload(t, handler, fixture.Token, fixture.ToolCallUUID, http.StatusOK)
			if !bytes.Equal(legacy.Body.Bytes(), current.Body.Bytes()) {
				t.Fatal("legacy download body differs")
			}
			for _, header := range []string{"Content-Type", "Content-Disposition", "ETag", "Cache-Control", "Content-Length"} {
				if legacy.Header().Get(header) != current.Header().Get(header) {
					t.Fatalf("legacy download %s differs", header)
				}
			}
			other, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID: project.ProjectUUID, AgentID: fixture.AgentUUID, ContentType: "text/plain",
				Content: []byte("other"),
			})
			if err != nil {
				t.Fatal(err)
			}
			otherPath := root + "/artifacts/" + testPublicID(t, publicid.KindArtifact, other.ID) + "/content"
			requestLegacyArtifactEndpoint(t, handler, fixture.Token, http.MethodGet, otherPath, http.StatusNotFound)
		})
	}
}

func TestLegacyDaemonArtifactEndpointsRejectMemoryPaths(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServerWithStoreOptions(
		pool, []storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
	)
	project := bootstrapPublicHTTPProject(t, handler, "legacy-artifact-reject-memory")
	store := integrationStoreForHandler(t, handler)
	for _, tool := range []string{"upload_file", "download_file"} {
		var artifactID string
		fixture := createDaemonProcessFixtureWithToolInputBuilder(
			t, ctx, pool, store, project, time.Now(), "legacy-memory-"+tool, tool, nil,
			func(agentID uuid.UUID) json.RawMessage {
				if tool == "upload_file" {
					return json.RawMessage(`{"path":"/memory/team/note.md","source":"note.md"}`)
				}
				artifact, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
					ProjectID: project.ProjectUUID, AgentID: agentID, ContentType: "text/plain", Content: []byte("private"),
				})
				if err != nil {
					t.Fatal(err)
				}
				artifactID = testPublicID(t, publicid.KindArtifact, artifact.ID)
				return json.RawMessage(`{"path":"/memory/team/note.md","destination":"note.md"}`)
			},
		)
		if _, found, err := acceptDaemonProcessOfferForTest(
			ctx, store, fixture.authority(), fixture.ProcessUUID,
		); err != nil || !found {
			t.Fatalf("accept memory process: found=%t err=%v", found, err)
		}
		root := "/api/v1/daemon/tool-calls/" + testPublicID(t, publicid.KindToolCall, fixture.ToolCallUUID)
		if tool == "upload_file" {
			requestLegacyArtifactEndpoint(t, handler, fixture.Token, http.MethodPost,
				root+"/artifact?filename=note.md", http.StatusNotFound)
		} else {
			requestLegacyArtifactEndpoint(t, handler, fixture.Token, http.MethodGet,
				root+"/artifacts/"+artifactID+"/content", http.StatusNotFound)
		}
	}
}

func requestLegacyArtifactEndpoint(
	t *testing.T,
	handler http.Handler,
	token, method, path string,
	wantStatus int,
) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewReader([]byte("content"))
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != wantStatus {
		t.Fatalf("legacy endpoint %s %s: status=%d body=%s", method, path, recorder.Code, recorder.Body.String())
	}
	return recorder
}
