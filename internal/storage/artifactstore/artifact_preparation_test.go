package artifactstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type preparationBlobStore struct {
	blobstore.Store // A read would panic: preparation/compensation must never read.
	puts            []string
	deletes         []string
	deleteErr       error
	deleteErrors    map[string]error
}

func (b *preparationBlobStore) PutBlob(_ context.Context, key string, content []byte) (blobstore.Metadata, error) {
	b.puts = append(b.puts, key)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *preparationBlobStore) DeleteBlob(_ context.Context, key string) error {
	b.deletes = append(b.deletes, key)
	if err, ok := b.deleteErrors[key]; ok {
		return err
	}
	return b.deleteErr
}

func TestPrepareArtifactNeedsNoDatabaseAndRetainsOnlyUploadedMetadata(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	blobs := &preparationBlobStore{}
	store := New(nil, blobs)
	agentID, err := uuid.NewV7()
	require.NoError(t, err)
	claimedSize := int64(9999)
	input := CreateArtifactInput{
		ProjectID: uuid.New(), AgentID: agentID, ContentType: "image/png", Filename: "test.png",
		Content: []byte("image"), MaxBytes: 5, Digest: "untrusted caller digest", SizeBytes: &claimedSize,
	}
	prepared, err := store.PrepareArtifact(ctx, input)
	require.NoError(t, err, "no database exists, and exactly MaxBytes must be accepted")
	require.Equal(t, []string{artifactObjectKey(agentID, prepared.id)}, blobs.puts)
	require.Equal(t, uuid.Version(7), prepared.id.Version())
	require.Nil(t, prepared.input.Content, "prepared batches must not retain uploaded content buffers")
	require.Equal(t, blobstore.ContentDigest(input.Content), prepared.input.Digest)
	require.Equal(t, int64(5), *prepared.input.SizeBytes)
	claimedSize = 1
	require.Equal(t, int64(5), *prepared.input.SizeBytes, "caller must not mutate retained metadata")
	input.MaxBytes = 4
	_, err = store.PrepareArtifact(ctx, input)
	require.Error(t, err)
	require.Len(t, blobs.puts, 1, "oversized uploads must fail before blob I/O")
	require.NoError(t, store.FinishPreparedArtifacts(ctx, ArtifactTransactionRolledBack, prepared))
	require.Equal(t, blobs.puts, blobs.deletes)
}

func TestPreparedArtifactCleanupMissingBlobIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	blobs := &preparationBlobStore{deleteErr: blobstore.ErrNotFound}
	store := New(nil, blobs)
	prepared, err := store.PrepareArtifact(ctx, CreateArtifactInput{
		ProjectID: uuid.New(), AgentID: uuid.New(), ContentType: "image/png", Content: []byte("image"),
	})
	require.NoError(t, err)
	// Invalid members must not prevent cleanup of valid uploads in the batch.
	require.ErrorIs(t,
		store.FinishPreparedArtifacts(ctx, ArtifactTransactionRolledBack, prepared, &PreparedArtifact{}),
		storeerr.ErrInvalidRequest,
	)
	require.Equal(t, blobs.puts, blobs.deletes)
	require.NoError(t, store.FinishPreparedArtifacts(ctx, ArtifactTransactionRolledBack, prepared))
	require.NoError(t, store.FinishPreparedArtifacts(ctx, ArtifactTransactionRolledBack, prepared))
	require.Equal(t, blobs.puts, blobs.deletes)
}

func TestPreparedArtifactCleanupContinuesPastInvalidMembersAndFailures(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	blobs := &preparationBlobStore{deleteErrors: make(map[string]error)}
	store := New(nil, blobs)
	foreignBlobs := &preparationBlobStore{}
	foreignStore := New(nil, foreignBlobs)
	prepare := func(owner *Store) *PreparedArtifact {
		t.Helper()
		prepared, err := owner.PrepareArtifact(ctx, CreateArtifactInput{
			ProjectID: uuid.New(), AgentID: uuid.New(), ContentType: "image/png", Content: []byte("image"),
		})
		require.NoError(t, err)
		return prepared
	}
	first, second, retained, foreign := prepare(store), prepare(store), prepare(store), prepare(foreignStore)
	require.NoError(t, store.FinishPreparedArtifacts(ctx, ArtifactTransactionUnknown, retained))
	deleteErr := errors.New("blob service unavailable")
	blobs.deleteErrors[first.key] = deleteErr
	err := store.FinishPreparedArtifacts(ctx, ArtifactTransactionRolledBack,
		first, nil, &PreparedArtifact{}, foreign, retained, second)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.ErrorIs(t, err, deleteErr, "cleanup failures must remain visible alongside invalid batch members")
	require.Equal(t, []string{first.key, second.key}, blobs.deletes, "later valid uploads must still be cleaned")
	require.False(t, first.cleaned)
	require.True(t, second.cleaned)
	require.Empty(t, foreignBlobs.deletes)
	require.False(t, foreign.finalized, "another store cannot settle the owner's upload")
	require.Equal(t, ArtifactTransactionUnknown, retained.outcome)
	require.False(t, retained.cleaned, "an unknown outcome must never be changed to rollback")

	delete(blobs.deleteErrors, first.key)
	require.NoError(t, store.FinishPreparedArtifacts(ctx, ArtifactTransactionRolledBack, first, second))
	require.Equal(t, []string{first.key, second.key, first.key}, blobs.deletes,
		"retry only the failed deletion, preserving successful cleanup")
	require.NoError(t, foreignStore.FinishPreparedArtifacts(ctx, ArtifactTransactionCommitted, foreign))
	require.Equal(t, foreignBlobs.puts, foreignBlobs.deletes, "foreign upload remains under its own store's control")
}

func TestPreparedArtifactInvalidBatchPreservesUnknownAndCommittedUploads(t *testing.T) {
	t.Parallel()
	for _, outcome := range []ArtifactTransactionOutcome{ArtifactTransactionUnknown, ArtifactTransactionCommitted} {
		t.Run(map[ArtifactTransactionOutcome]string{
			ArtifactTransactionUnknown: "unknown", ArtifactTransactionCommitted: "committed",
		}[outcome], func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			blobs := &preparationBlobStore{}
			store := New(nil, blobs)
			var batch []*PreparedArtifact
			for range 2 {
				prepared, err := store.PrepareArtifact(ctx, CreateArtifactInput{
					ProjectID: uuid.New(), AgentID: uuid.New(), ContentType: "image/png", Content: []byte("image"),
				})
				require.NoError(t, err)
				batch = append(batch, prepared)
			}
			batch[0].inserted = true // Model the successful record-insert result; the second upload was unused.
			require.ErrorIs(t, store.FinishPreparedArtifacts(ctx, outcome, nil, batch[0], nil, batch[1]),
				storeerr.ErrInvalidRequest)
			require.NoError(t, store.FinishPreparedArtifacts(ctx, outcome, batch...))
			if outcome == ArtifactTransactionUnknown {
				require.Empty(t, blobs.deletes, "unknown outcomes retain inserted and unused uploads alike")
			} else {
				require.Equal(t, []string{batch[1].key}, blobs.deletes, "committed records retain their blobs")
			}
		})
	}
}

func TestCreateArtifactRuntimeProofMustBeStructurallyValidBeforeUpload(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	blobs := &preparationBlobStore{}
	store := New(nil, blobs)
	input := CreateArtifactInput{
		ProjectID: uuid.New(), AgentID: uuid.New(), ContentType: "image/png", Content: []byte("image"),
	}
	_, err := store.CreateArtifactWithIntegrationRuntimeLease(ctx, input, uuid.New(), nil)
	require.Error(t, err)
	_, err = store.CreateArtifactWithIntegrationRuntimeLease(
		ctx, input, uuid.New(), &integrationstore.IntegrationRuntimeLeaseProof{},
	)
	require.Error(t, err)
	proof := &integrationstore.IntegrationRuntimeLeaseProof{
		IntegrationAppID: uuid.New(), UnitID: uuid.New(), LeaseToken: uuid.New(), LeaseGeneration: 1,
	}
	_, err = store.CreateArtifactWithIntegrationRuntimeLease(ctx, input, uuid.Nil, proof)
	require.Error(t, err)
	require.Empty(t, blobs.puts)
}
