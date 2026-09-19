package artifactstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// PreparedArtifactUploaded checks bytes at a frozen inbox identity. The planned
// agent need not exist yet. This is a provider-ingress helper, not a public
// artifact API; its caller must obtain identity and digest from the durable plan.
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

// UploadPreparedArtifact performs no metadata writes and must run outside a DB
// transaction. Every writer verifies the same frozen digest before uploading;
// even a worker whose lease expires during upload can only write identical bytes.
// Never delete on an uncertain upload/commit: admission may already reference it.
func (s *Store) UploadPreparedArtifact(
	ctx context.Context,
	agentID uuid.UUID,
	expected PreparedArtifact,
	content []byte,
) error {
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
