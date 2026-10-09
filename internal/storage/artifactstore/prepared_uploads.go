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
	if agentID == uuid.Nil || s.blobs == nil {
		return fmt.Errorf("planned agent and blob store are required")
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	if int64(len(content)) != expected.SizeBytes || blobstore.ContentDigest(content) != expected.Digest {
		return storeerr.ErrIdempotencyConflict
	}
	// Create before reading: S3 can return AccessDenied for a missing key when
	// the caller lacks ListBucket. The conditional write also prevents concurrent
	// uploaders from overwriting conflicting bytes. Never delete uncertain uploads.
	metadata, err := s.blobs.PutBlobIfAbsent(ctx, artifactObjectKey(agentID, expected.ID), content)
	if errors.Is(err, blobstore.ErrAlreadyExists) {
		present, verifyErr := s.PreparedArtifactUploaded(ctx, agentID, expected)
		if verifyErr != nil {
			return fmt.Errorf("verify existing prepared artifact %s: %w", expected.ID, errors.Join(verifyErr, err))
		}
		if !present {
			return fmt.Errorf("prepared artifact disappeared after conditional upload: %w",
				errors.Join(blobstore.ErrNotFound, err))
		}
		return nil
	}
	if err != nil {
		return err
	}
	if metadata.SizeBytes != expected.SizeBytes || metadata.Digest != expected.Digest {
		return storeerr.ErrIdempotencyConflict
	}
	return nil
}
