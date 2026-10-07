package artifactstore

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type preparedUploadBlobs struct {
	mu      sync.Mutex
	content map[string][]byte
}

func (s *preparedUploadBlobs) PutBlob(_ context.Context, key string, content []byte) (blobstore.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content[key] = append([]byte(nil), content...)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}
func (s *preparedUploadBlobs) GetBlob(_ context.Context, key string) ([]byte, blobstore.Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
func (s *preparedUploadBlobs) DeleteBlob(context.Context, string) error {
	panic("uncertain upload must never delete")
}

func TestPreparedUploadPinnedBytesConcurrentReplayAndMismatch(t *testing.T) {
	blobs := &preparedUploadBlobs{content: map[string][]byte{}}
	store := New(nil, blobs)
	agentID := uuid.Must(uuid.NewV7())
	content := []byte("frozen bytes")
	expected := PreparedArtifact{
		ID:          uuid.Must(uuid.NewV7()),
		ContentType: "text/plain",
		Filename:    "review.txt",
		Digest:      blobstore.ContentDigest(content),
		SizeBytes:   int64(len(content)),
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := store.UploadPreparedArtifact(t.Context(), agentID, expected, content); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	present, err := store.PreparedArtifactUploaded(t.Context(), agentID, expected)
	require.NoError(t, err)
	require.True(t, present)
	changed := expected
	changed.Digest = blobstore.ContentDigest([]byte("changed"))
	changed.SizeBytes = 7
	require.ErrorIs(
		t,
		store.UploadPreparedArtifact(t.Context(), agentID, changed, []byte("changed")),
		storeerr.ErrIdempotencyConflict,
	)
	stored, _, err := blobs.GetBlob(t.Context(), artifactObjectKey(agentID, expected.ID))
	require.NoError(t, err)
	require.Equal(t, content, stored)
	expected.ID = uuid.Must(uuid.NewV7())
	require.ErrorIs(
		t,
		store.UploadPreparedArtifact(t.Context(), agentID, expected, []byte("changed")),
		storeerr.ErrIdempotencyConflict,
	)
	present, err = store.PreparedArtifactUploaded(t.Context(), agentID, expected)
	require.NoError(t, err)
	require.False(t, present)
}
