package artifactstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// PreparedArtifact owns one provisional upload. Only PrepareArtifact constructs
// valid instances. It belongs to one caller-owned transaction and must not be
// copied, persisted more than once, or used concurrently. Its agent may be an
// internal preallocated UUID until the caller inserts that agent in the same tx.
// Content bytes are not retained after upload.
type PreparedArtifact struct {
	store     *Store
	id        uuid.UUID
	key       string
	input     CreateArtifactInput
	attempted bool
	inserted  bool
	finalized bool
	outcome   ArtifactTransactionOutcome
	cleaned   bool
}

// ArtifactTransactionOutcome describes the whole outer transaction, including
// any agent, binding, input, and wakeup writes composed with these artifacts.
type ArtifactTransactionOutcome uint8

const (
	// ArtifactTransactionUnknown is the safe default after an ambiguous COMMIT.
	// It permanently retains all uploads associated with that transaction.
	ArtifactTransactionUnknown ArtifactTransactionOutcome = iota
	ArtifactTransactionCommitted
	ArtifactTransactionRolledBack
)

// PrepareArtifact validates and uploads content without opening a database
// transaction or requiring the agent to exist yet. It grants no authority to
// persist. After this succeeds the caller must settle the upload using
// FinishPreparedArtifacts, even if it never begins a transaction.
func (s *Store) PrepareArtifact(ctx context.Context, input CreateArtifactInput) (*PreparedArtifact, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil {
		return nil, errors.New("project id and agent id are required")
	}
	if err := integrationstore.ValidateIntegrationRuntimeLeaseProof(input.runtimeLease); err != nil {
		return nil, err
	}
	if input.runtimeLease != nil && input.integrationInstallID == uuid.Nil {
		return nil, errors.New("runtime artifact integration installation is required")
	}
	if input.ContentType == "" {
		return nil, errors.New("artifact content type is required")
	}
	if err := dbsafe.Text(input.ContentType); err != nil {
		return nil, storeerr.InvalidRequest(fmt.Errorf("artifact content type %w", err))
	}
	if err := dbsafe.Text(input.Filename); err != nil {
		return nil, storeerr.InvalidRequest(fmt.Errorf("artifact filename %w", err))
	}
	if len(input.Content) == 0 {
		return nil, errors.New("artifact content is required")
	}
	if input.MaxBytes > 0 && int64(len(input.Content)) > input.MaxBytes {
		return nil, fmt.Errorf("artifact content exceeds %d bytes", input.MaxBytes)
	}
	if s.blobs == nil {
		return nil, ErrBlobStoreNotConfigured
	}
	if input.runtimeLease != nil {
		proof := *input.runtimeLease
		input.runtimeLease = &proof
	}
	artifactID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate artifact id: %w", err)
	}
	key := artifactObjectKey(input.AgentID, artifactID)
	metadata, err := s.blobs.PutBlob(ctx, key, input.Content)
	if err != nil {
		return nil, fmt.Errorf("upload artifact content: %w", err)
	}
	input.Content = nil
	input.Digest = metadata.Digest
	input.SizeBytes = &metadata.SizeBytes
	return &PreparedArtifact{store: s, id: artifactID, key: key, input: input}, nil
}

// PersistPreparedArtifact performs only database work in the caller's tx. The
// agent must now exist and be active in the prepared project. Existing lifecycle
// and optional runtime fences are locked in this same transaction. Concurrent
// identical uploads return the canonical record; use its ID in the input, not a
// provisional blob ID. This neither commits nor rolls back nor deletes blobs.
func (s *Store) PersistPreparedArtifact(
	ctx context.Context,
	tx pgx.Tx,
	prepared *PreparedArtifact,
) (ArtifactRecord, error) {
	return s.persistPreparedArtifact(ctx, tx, prepared, true)
}

func (s *Store) persistPreparedArtifact(
	ctx context.Context,
	tx pgx.Tx,
	prepared *PreparedArtifact,
	requireActiveAgent bool,
) (ArtifactRecord, error) {
	if prepared == nil || prepared.store != s || tx == nil {
		return ArtifactRecord{}, storeerr.InvalidRequest(errors.New("prepared artifact and owning transaction are required"))
	}
	if prepared.finalized || prepared.attempted {
		return ArtifactRecord{}, storeerr.InvalidRequest(errors.New("prepared artifact has already been used"))
	}
	prepared.attempted = true
	record, err := persistArtifactRecordTx(ctx, tx, prepared.id, prepared.input, requireActiveAgent)
	if err != nil {
		return ArtifactRecord{}, err
	}
	prepared.inserted = record.Created
	return record, nil
}

// FinishPreparedArtifacts must run only after the outer transaction has settled,
// never under its locks. Pass one common outcome for every upload prepared for
// that operation, including unused uploads. A known rollback removes them all;
// a commit removes only uploads unused by canonical artifact records; an unknown
// outcome retains them all. A failed COMMIT is not evidence of rollback.
//
// The first outcome is final. Repeated calls with that outcome are idempotent
// and retry failed cleanup; changing it is rejected. Cleanup has an independent
// timeout so cancellation of the original request cannot prevent compensation.
// Invalid entries and cleanup failures are collected without preventing cleanup
// of other owned uploads. Foreign uploads and conflicting final outcomes remain
// untouched.
// Cleanup errors after a committed operation must not turn it into a retry of
// the operation: report/log them separately, as CreateArtifact does.
func (s *Store) FinishPreparedArtifacts(
	ctx context.Context,
	outcome ArtifactTransactionOutcome,
	prepared ...*PreparedArtifact,
) error {
	if outcome != ArtifactTransactionUnknown && outcome != ArtifactTransactionCommitted &&
		outcome != ArtifactTransactionRolledBack {
		return storeerr.InvalidRequest(errors.New("invalid artifact transaction outcome"))
	}
	var cleanupErr error
	for _, artifact := range prepared {
		if artifact == nil || artifact.store != s {
			cleanupErr = errors.Join(cleanupErr,
				storeerr.InvalidRequest(errors.New("prepared artifact belongs to a different store")))
			continue
		}
		if artifact.finalized && artifact.outcome != outcome {
			cleanupErr = errors.Join(cleanupErr,
				storeerr.InvalidRequest(errors.New("prepared artifact transaction outcome is already final")))
			continue
		}
		artifact.finalized = true
		artifact.outcome = outcome
		if artifact.cleaned || outcome == ArtifactTransactionUnknown ||
			(outcome == ArtifactTransactionCommitted && artifact.inserted) {
			continue
		}
		err := s.deleteProvisionalArtifactBlob(ctx, artifact.key)
		if err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		artifact.cleaned = true
	}
	return cleanupErr
}
