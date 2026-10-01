package artifactstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) PreparedArtifactUploaded(
	ctx context.Context,
	agentID uuid.UUID,
	expected PreparedArtifact,
) (bool, error) {
	if agentID == uuid.Nil || s.blobs == nil {
		return false, fmt.Errorf("planned agent and blob store are required")
	}
	if err := expected.Validate(); err != nil {
		return false, err
	}
	content, _, err := s.blobs.GetBlob(ctx, artifactObjectKey(agentID, expected.ID))
	if errors.Is(err, blobstore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if int64(len(content)) != expected.SizeBytes || blobstore.ContentDigest(content) != expected.Digest {
		return false, storeerr.ErrIdempotencyConflict
	}
	return true, nil
}

func (s *Store) UploadPreparedArtifact(
	ctx context.Context,
	agentID uuid.UUID,
	expected PreparedArtifact,
	content []byte,
) error {
	// All writers use the frozen digest, so an expired uploader can only write
	// identical bytes. Uncertain uploads must not delete possibly admitted data.
	if present, err := s.PreparedArtifactUploaded(ctx, agentID, expected); err != nil || present {
		return err
	}
	if int64(len(content)) != expected.SizeBytes || blobstore.ContentDigest(content) != expected.Digest {
		return storeerr.ErrIdempotencyConflict
	}
	metadata, err := s.blobs.PutBlob(ctx, artifactObjectKey(agentID, expected.ID), content)
	if err != nil {
		return err
	}
	if metadata.SizeBytes != expected.SizeBytes || metadata.Digest != expected.Digest {
		return storeerr.ErrIdempotencyConflict
	}
	return nil
}
