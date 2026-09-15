package artifactstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// OpenArtifactBlob authorizes the artifact in the supplied project and agent
// before opening its private storage key. It does not buffer content or impose an
// egress size limit. The returned metadata comes from the authorized artifact.
// Callers own the reader and must close it. Cancellation interrupts opening and
// reading; Close unblocks Read even when called concurrently, as required by
// channelconnector.OperationArtifact.Open. Channel/grant authority remains the
// operation caller's responsibility.
func (s *Store) OpenArtifactBlob(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (io.ReadCloser, ArtifactRecord, error) {
	record, err := s.GetArtifact(ctx, projectID, agentID, id)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}
	if s.blobs == nil {
		return nil, ArtifactRecord{}, ErrBlobStoreNotConfigured
	}
	body, _, err := s.blobs.OpenBlob(ctx, artifactObjectKey(record.AgentID, record.ID))
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, ArtifactRecord{}, fmt.Errorf("open artifact %s content: %w", record.ID, storeerr.ErrNotFound)
		}
		return nil, ArtifactRecord{}, fmt.Errorf("open artifact %s content: %w", record.ID, err)
	}
	return body, record, nil
}
