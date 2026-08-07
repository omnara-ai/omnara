//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/tooloutput"
)

type offloadBlobStore struct {
	putKeys    []string
	deleteKeys []string
	content    map[string][]byte
}

func newOffloadBlobStore() *offloadBlobStore {
	return &offloadBlobStore{content: make(map[string][]byte)}
}

func (s *offloadBlobStore) PutBlob(
	_ context.Context,
	key string,
	content []byte,
) (blobstore.Metadata, error) {
	s.putKeys = append(s.putKeys, key)
	s.content[key] = append([]byte(nil), content...)
	return blobstore.Metadata{
		Digest:    blobstore.ContentDigest(content),
		SizeBytes: int64(len(content)),
	}, nil
}

func (s *offloadBlobStore) GetBlob(
	_ context.Context,
	key string,
) ([]byte, blobstore.Metadata, error) {
	content, ok := s.content[key]
	if !ok {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return append([]byte(nil), content...), blobstore.Metadata{
		Digest:    blobstore.ContentDigest(content),
		SizeBytes: int64(len(content)),
	}, nil
}

func (s *offloadBlobStore) DeleteBlob(_ context.Context, key string) error {
	s.deleteKeys = append(s.deleteKeys, key)
	delete(s.content, key)
	return nil
}

func newToolResultOffloadFixture(
	t *testing.T,
	ctx context.Context,
	testName string,
) (processDaemonFixture, *offloadBlobStore) {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newOffloadBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	user := mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"offload-"+testName+"@example.com",
		"Offload Tester",
	)
	return newProcessDaemonFixtureInStore(t, ctx, store, user.ID, testName, now), blobs
}

func countArtifactsForAgent(
	t *testing.T,
	ctx context.Context,
	fixture processDaemonFixture,
) int {
	t.Helper()
	var count int
	if err := fixture.Store.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM artifacts WHERE agent_id = $1`,
		fixture.AgentID,
	).Scan(&count); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	return count
}

func TestToolResultOffloadRoundTripsAndReplaysWithoutUpload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, blobs := newToolResultOffloadFixture(t, ctx, "round_trip")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "round_trip", "web_fetch")
	oversized := strings.Repeat("fetched content line\n", 10_000)
	parts, err := json.Marshal([]map[string]any{{"type": "text", "text": oversized}})
	if err != nil {
		t.Fatal(err)
	}
	input := executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      fixture.Lock.ID,
		ResultContentParts: parts,
	}
	completed, err := fixture.Store.Execution().CompleteToolCall(ctx, input)
	if err != nil {
		t.Fatalf("complete tool call: %v", err)
	}
	if len(completed.ResultContentParts) > tooloutput.MaxInlineToolResultBytes {
		t.Fatalf("bounded result = %d bytes", len(completed.ResultContentParts))
	}
	var storedParts []map[string]any
	if err := json.Unmarshal(completed.ResultContentParts, &storedParts); err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	if len(storedParts) != 2 || storedParts[1]["type"] != "artifact_ref" {
		t.Fatalf("stored result parts = %s", completed.ResultContentParts)
	}
	artifactIDText, ok := storedParts[1]["artifact_id"].(string)
	if !ok {
		t.Fatalf("artifact id = %#v", storedParts[1]["artifact_id"])
	}
	artifactID, err := ParseID(artifactIDText)
	if err != nil {
		t.Fatalf("parse artifact id: %v", err)
	}
	content, _, err := fixture.Store.Artifacts().GetArtifactBlob(
		ctx,
		testProjectID,
		fixture.AgentID,
		artifactID,
	)
	if err != nil || string(content) != oversized {
		t.Fatalf("artifact round trip bytes=%d err=%v", len(content), err)
	}
	replayed, err := fixture.Store.Execution().CompleteToolCall(ctx, input)
	if err != nil {
		t.Fatalf("replay tool completion: %v", err)
	}
	if !sameJSON(replayed.ResultContentParts, completed.ResultContentParts) {
		t.Fatalf("replay result changed:\nfirst: %s\nreplay: %s", completed.ResultContentParts, replayed.ResultContentParts)
	}
	if len(blobs.putKeys) != 1 || countArtifactsForAgent(t, ctx, fixture) != 1 {
		t.Fatalf("uploads=%d artifacts=%d, want one of each", len(blobs.putKeys), countArtifactsForAgent(t, ctx, fixture))
	}
}

func TestToolResultOffloadRollsBackArtifactAndBlobWithCompletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture, blobs := newToolResultOffloadFixture(t, ctx, "rollback")
	toolCallID := createToolCallForProcessTest(t, ctx, fixture, "rollback", "web_fetch")
	parts, err := json.Marshal([]map[string]any{{
		"type": "text",
		"text": strings.Repeat("rollback content\n", 10_000),
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID:          testProjectID,
		AgentID:            fixture.AgentID,
		ID:                 toolCallID,
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		RuntimeLockID:      testID("wrong-runtime-lock"),
		ResultContentParts: parts,
	})
	if err == nil {
		t.Fatal("completion with the wrong runtime lock unexpectedly succeeded")
	}
	if len(blobs.putKeys) != 1 || len(blobs.deleteKeys) != 1 ||
		blobs.putKeys[0] != blobs.deleteKeys[0] {
		t.Fatalf("uploaded=%v deleted=%v", blobs.putKeys, blobs.deleteKeys)
	}
	if len(blobs.content) != 0 || countArtifactsForAgent(t, ctx, fixture) != 0 {
		t.Fatalf("rollback left blobs=%d artifacts=%d", len(blobs.content), countArtifactsForAgent(t, ctx, fixture))
	}
}
