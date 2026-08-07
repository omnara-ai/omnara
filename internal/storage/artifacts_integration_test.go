//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
)

type recordingBlobStore struct {
	putKeys             []string
	deleteKeys          []string
	deleteContextErrors []error
	content             map[string][]byte
	afterPut            func()
}

func newRecordingBlobStore() *recordingBlobStore {
	return &recordingBlobStore{content: map[string][]byte{}}
}

func (s *recordingBlobStore) PutBlob(ctx context.Context, key string, content []byte) (blobstore.Metadata, error) {
	_ = ctx
	s.putKeys = append(s.putKeys, key)
	s.content[key] = append([]byte(nil), content...)
	if s.afterPut != nil {
		s.afterPut()
	}
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (s *recordingBlobStore) GetBlob(ctx context.Context, key string) ([]byte, blobstore.Metadata, error) {
	_ = ctx
	content, ok := s.content[key]
	if !ok {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return append(
			[]byte(nil),
			content...), blobstore.Metadata{
			Digest:    blobstore.ContentDigest(content),
			SizeBytes: int64(len(content)),
		}, nil
}

func (s *recordingBlobStore) DeleteBlob(ctx context.Context, key string) error {
	s.deleteKeys = append(s.deleteKeys, key)
	s.deleteContextErrors = append(s.deleteContextErrors, ctx.Err())
	delete(s.content, key)
	return nil
}

func TestCreateArtifactContentRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	record, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   testProjectID,
		AgentID:     agentID,
		ContentType: "image/png",
		Filename:    "diagram.png",
		Content:     []byte("png bytes"),
	})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if !record.Created {
		t.Fatal("expected newly created artifact")
	}
	if record.Filename != "diagram.png" || record.ContentType != "image/png" {
		t.Fatalf("unexpected record metadata: %+v", record)
	}
	if record.SizeBytes == nil || *record.SizeBytes != int64(len("png bytes")) {
		t.Fatalf("unexpected size: %v", record.SizeBytes)
	}
	if !strings.HasPrefix(record.Digest, "sha256:") {
		t.Fatalf("unexpected digest: %q", record.Digest)
	}
	content, loaded, err := store.Artifacts().GetArtifactBlob(ctx, testProjectID, agentID, record.ID)
	if err != nil {
		t.Fatalf("get artifact blob: %v", err)
	}
	if string(content) != "png bytes" {
		t.Fatalf("got content %q", content)
	}
	if loaded.Digest != record.Digest || loaded.Filename != "diagram.png" {
		t.Fatalf("loaded record mismatch: %+v vs %+v", loaded, record)
	}
}

func TestCreateArtifactRequiresBlobStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	_, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   testProjectID,
		AgentID:     agentID,
		ContentType: "image/png",
		Content:     []byte("png bytes"),
	})
	if !errors.Is(err, artifactstore.ErrBlobStoreNotConfigured) {
		t.Fatalf("create artifact without blob store error = %v, want ErrBlobStoreNotConfigured", err)
	}
}

func TestCreateArtifactMaxBytesRejectsBeforeUpload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newRecordingBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	_, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   testProjectID,
		AgentID:     agentID,
		ContentType: "image/png",
		Content:     []byte("png bytes"),
		MaxBytes:    3,
	})
	if err == nil || !strings.Contains(err.Error(), "artifact content exceeds 3 bytes") {
		t.Fatalf("create artifact error = %v, want max bytes error", err)
	}
	if len(blobs.putKeys) != 0 {
		t.Fatalf("uploaded blobs = %v, want none", blobs.putKeys)
	}
}

func TestCreateArtifactCleansUploadedBlobWhenDBInsertFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newRecordingBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))

	_, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   testProjectID,
		AgentID:     testID("missing_artifact_cleanup_agent"),
		ContentType: "image/png",
		Content:     []byte("png bytes"),
	})
	if err == nil {
		t.Fatal("expected create artifact to fail")
	}
	if len(blobs.putKeys) != 1 {
		t.Fatalf("uploaded blobs = %v, want one", blobs.putKeys)
	}
	if len(blobs.deleteKeys) != 1 || blobs.deleteKeys[0] != blobs.putKeys[0] {
		t.Fatalf("deleted blobs = %v, want uploaded key %q", blobs.deleteKeys, blobs.putKeys[0])
	}
	if _, ok := blobs.content[blobs.putKeys[0]]; ok {
		t.Fatalf("uploaded blob %q was not removed", blobs.putKeys[0])
	}
}

func TestCreateArtifactCleanupSurvivesRequestCancellation(t *testing.T) {
	t.Parallel()
	setupCtx := context.Background()
	pool := openIntegrationDB(t, setupCtx)
	seedMigratedDB(t, setupCtx, pool)
	blobs := newRecordingBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, setupCtx, store, now)
	ctx, cancel := context.WithCancel(setupCtx)
	blobs.afterPut = cancel

	_, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   testProjectID,
		AgentID:     agentID,
		ContentType: "image/png",
		Content:     []byte("png bytes"),
	})
	if err == nil {
		t.Fatal("expected create artifact to fail after request cancellation")
	}
	if len(blobs.deleteKeys) != 1 {
		t.Fatalf("deleted blobs = %v, want one", blobs.deleteKeys)
	}
	if len(blobs.deleteContextErrors) != 1 || blobs.deleteContextErrors[0] != nil {
		t.Fatalf("delete context errors = %v, want [nil]", blobs.deleteContextErrors)
	}
}

func TestCreateArtifactIdempotentReplayAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newRecordingBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	input := artifactstore.CreateArtifactInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		ContentType:    "image/png",
		Filename:       "shot.png",
		Content:        []byte("same bytes"),
		IdempotencyKey: "upload:client-1:0",
	}
	first, err := store.Artifacts().CreateArtifact(ctx, input)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	replayed, err := store.Artifacts().CreateArtifact(ctx, input)
	if err != nil {
		t.Fatalf("replay artifact: %v", err)
	}
	if replayed.ID != first.ID {
		t.Fatalf("replay minted a new artifact: %s vs %s", replayed.ID, first.ID)
	}
	if replayed.Created {
		t.Fatal("replay should not report created")
	}
	if len(blobs.putKeys) != 2 || len(blobs.deleteKeys) != 1 ||
		blobs.deleteKeys[0] != blobs.putKeys[1] {
		t.Fatalf("replay blob writes=%v deletes=%v, want second upload deleted", blobs.putKeys, blobs.deleteKeys)
	}
	if _, ok := blobs.content[blobs.putKeys[0]]; !ok {
		t.Fatal("replay deleted the original artifact blob")
	}

	input.Content = []byte("different bytes")
	if _, err := store.Artifacts().CreateArtifact(ctx, input); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrIdempotencyConflict", err)
	}
	if len(blobs.putKeys) != 3 || len(blobs.deleteKeys) != 2 ||
		blobs.deleteKeys[1] != blobs.putKeys[2] {
		t.Fatalf("conflict blob writes=%v deletes=%v, want third upload deleted", blobs.putKeys, blobs.deleteKeys)
	}
}

func TestCreateArtifactRejectsArchivedAgentButReplaysExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newRecordingBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	user := mustCreateProjectOperatorUser(t, ctx, store, "artifact-archived@example.com", "Artifact Archived")
	input := artifactstore.CreateArtifactInput{
		ProjectID:      testProjectID,
		AgentID:        agentID,
		ContentType:    "image/png",
		Filename:       "shot.png",
		Content:        []byte("same bytes"),
		IdempotencyKey: "upload:archived:0",
	}
	first, err := store.Artifacts().CreateArtifact(ctx, input)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if _, _, err := store.Execution().ArchiveAgent(
		ctx,
		testProjectID,
		agentID,
		userPrincipal(user.ID),
	); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	replayed, err := store.Artifacts().CreateArtifact(ctx, input)
	if err != nil {
		t.Fatalf("replay archived agent artifact: %v", err)
	}
	if replayed.Created || replayed.ID != first.ID {
		t.Fatalf("archived replay = %+v, want existing artifact %s", replayed, first.ID)
	}
	input.IdempotencyKey = "upload:archived:1"
	_, err = store.Artifacts().CreateArtifact(ctx, input)
	if !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("new archived agent artifact error = %v, want ErrStateTransitionConflict", err)
	}
	if len(blobs.putKeys) != 3 || len(blobs.deleteKeys) != 2 ||
		blobs.deleteKeys[0] != blobs.putKeys[1] || blobs.deleteKeys[1] != blobs.putKeys[2] {
		t.Fatalf("artifact blob writes=%v deletes=%v, want both post-archive uploads deleted", blobs.putKeys, blobs.deleteKeys)
	}
	var created int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE agent_id = $1 AND idempotency_key = $2`, agentID, input.IdempotencyKey).Scan(&created); err != nil {
		t.Fatalf("count post-archive artifacts: %v", err)
	}
	if created != 0 {
		t.Fatalf("post-archive artifacts = %d, want 0", created)
	}
}

func TestGetArtifactBlobMissingContentFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := integrationblob.MustOpen(t, ctx)
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)

	record, err := store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   testProjectID,
		AgentID:     agentID,
		ContentType: "image/png",
		Content:     []byte("png bytes"),
	})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if err := blobs.DeleteBlob(ctx, artifactObjectKey(record.AgentID, record.ID)); err != nil {
		t.Fatalf("delete blob: %v", err)
	}
	if _, _, err := store.Artifacts().GetArtifactBlob(ctx, testProjectID, agentID, record.ID); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListAgentArtifactsByIDsScopesToAgent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	otherAgentID := mustCreateAgent(t, ctx, store, now)

	mine, err := store.Artifacts().CreateArtifact(
		ctx,
		artifactstore.CreateArtifactInput{
			ProjectID:   testProjectID,
			AgentID:     agentID,
			ContentType: "image/png",
			Content:     []byte("mine"),
		},
	)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	other, err := store.Artifacts().CreateArtifact(
		ctx,
		artifactstore.CreateArtifactInput{
			ProjectID:   testProjectID,
			AgentID:     otherAgentID,
			ContentType: "image/png",
			Content:     []byte("other"),
		},
	)
	if err != nil {
		t.Fatalf("create other artifact: %v", err)
	}

	records, err := store.Artifacts().ListAgentArtifactsByIDs(ctx, testProjectID, agentID, []ID{mine.ID, other.ID})
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(records) != 1 || records[0].ID != mine.ID {
		t.Fatalf("expected only the agent's artifact, got %+v", records)
	}

	if _, _, err := store.Artifacts().GetArtifactBlob(ctx, testProjectID, otherAgentID, mine.ID); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("cross-agent artifact blob load error = %v, want not found", err)
	}
}
