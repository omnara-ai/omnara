//go:build integration

package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func artifactStreamFixture(t *testing.T) (*pgxpool.Pool, artifactstore.ArtifactRecord) {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(newRecordingBlobStore()))
	agentID := mustCreateAgent(t, ctx, store)
	record, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: testProjectID, AgentID: agentID, ContentType: "text/plain",
		Filename: "../../untrusted-name.txt", Content: []byte("prefix then the tail"),
	})
	require.NoError(t, err)
	return pool, record
}

func artifactStreamS3(t *testing.T, handler http.HandlerFunc) *blobstore.S3Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	blobs, err := blobstore.NewS3Store(t.Context(), blobstore.S3Config{
		Bucket: "artifacts-test", Region: "us-east-1", Endpoint: server.URL,
		AccessKeyID: "local-test", SecretAccessKey: "local-test", UsePathStyle: true,
	})
	require.NoError(t, err)
	return blobs
}

func TestOpenArtifactBlobAuthorizesBeforeStreaming(t *testing.T) {
	t.Parallel()
	pool, record := artifactStreamFixture(t)
	store := newIntegrationStore(pool)
	otherAgentID := mustCreateAgent(t, t.Context(), store)
	otherProjectID := seedAdditionalProjectForTest(t, t.Context(), pool, "artifact-stream-other-project")
	var requests atomic.Int32
	releaseTail := make(chan struct{})
	content := "prefix then the tail"
	blobs := artifactStreamS3(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet ||
			r.URL.Path != "/artifacts-test/"+artifactObjectKey(record.AgentID, record.ID) {
			t.Error("artifact filename or caller input changed the authorized storage key")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Header().Set("X-Amz-Meta-Omnara-Digest", record.Digest)
		if _, err := io.WriteString(w, content[:6]); err != nil {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		select {
		case <-releaseTail:
			_, _ = io.WriteString(w, content[6:])
		case <-r.Context().Done():
		}
	})
	artifacts := artifactstore.New(pool, blobs)
	for _, scope := range []struct {
		projectID uuid.UUID
		agentID   uuid.UUID
	}{
		{testProjectID, otherAgentID},
		{otherProjectID, record.AgentID},
		{otherProjectID, otherAgentID},
	} {
		body, _, err := artifacts.OpenArtifactBlob(t.Context(), scope.projectID, scope.agentID, record.ID)
		require.Nil(t, body)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
	require.Zero(t, requests.Load(), "denied scope must not touch object storage")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body, metadata, err := artifacts.OpenArtifactBlob(ctx, record.ProjectID, record.AgentID, record.ID)
	require.NoError(t, err, "opening must not wait for the withheld tail")
	defer func() { require.NoError(t, body.Close()) }()
	require.Equal(t, record.ID, metadata.ID)
	require.Equal(t, record.Filename, metadata.Filename)
	require.Equal(t, record.Digest, metadata.Digest)
	require.Equal(t, record.SizeBytes, metadata.SizeBytes)
	first := make([]byte, 6)
	_, err = io.ReadFull(body, first)
	require.NoError(t, err)
	require.Equal(t, content[:6], string(first))
	close(releaseTail)
	tail, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, content, string(first)+string(tail))
	require.Equal(t, int32(1), requests.Load())
}

func TestOpenArtifactBlobCancellationAndCloseReachStorage(t *testing.T) {
	t.Parallel()
	pool, record := artifactStreamFixture(t)
	for _, action := range []string{"cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			disconnected := make(chan struct{})
			blobs := artifactStreamS3(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "100")
				w.Header().Set("X-Amz-Meta-Omnara-Digest", record.Digest)
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
				<-r.Context().Done()
				close(disconnected)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			body, _, err := artifactstore.New(pool, blobs).OpenArtifactBlob(
				ctx, record.ProjectID, record.AgentID, record.ID,
			)
			require.NoError(t, err)
			defer func() { _ = body.Close() }()
			done := make(chan error, 1)
			go func() {
				_, err := body.Read(make([]byte, 1))
				done <- err
			}()
			if action == "cancel" {
				cancel()
			} else {
				require.NoError(t, body.Close())
			}
			select {
			case err := <-done:
				require.Error(t, err)
				if action == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("artifact read remained blocked")
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("artifact storage request remained live")
			}
		})
	}
}

func TestOpenArtifactBlobStorageFailuresReturnNoReader(t *testing.T) {
	t.Parallel()
	pool, record := artifactStreamFixture(t)
	body, _, err := artifactstore.New(pool, nil).OpenArtifactBlob(
		t.Context(), record.ProjectID, record.AgentID, record.ID,
	)
	require.Nil(t, body)
	require.ErrorIs(t, err, artifactstore.ErrBlobStoreNotConfigured)
	blobs := artifactStreamS3(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>")
	})
	body, _, err = artifactstore.New(pool, blobs).OpenArtifactBlob(
		t.Context(), record.ProjectID, record.AgentID, record.ID,
	)
	require.Nil(t, body)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
