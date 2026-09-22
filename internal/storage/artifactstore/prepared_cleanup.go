package artifactstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) DeleteUnreferencedPreparedArtifact(
	ctx context.Context,
	projectID, agentID, artifactID uuid.UUID,
) error {
	// The caller must establish terminal receipt failure before deletion; it fences
	// admission so reference checks and blob deletion need no shared transaction.
	// A stale uploader can still recreate unused bytes afterward.
	if projectID == uuid.Nil || agentID == uuid.Nil || artifactID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project, planned agent and artifact IDs are required"))
	}
	if s.blobs == nil {
		return ErrBlobStoreNotConfigured
	}
	agent, err := s.q.GetAgent(ctx, dbsqlc.GetAgentParams{ID: agentID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check prepared artifact agent: %w", err)
	}
	if err == nil && agent.ProjectID != projectID {
		return storeerr.ErrUnauthorized
	}
	if _, err := s.GetArtifact(ctx, projectID, agentID, artifactID); err == nil {
		return nil
	} else if !errors.Is(err, storeerr.ErrNotFound) {
		return err
	}
	if err := s.blobs.DeleteBlob(ctx, artifactObjectKey(agentID, artifactID)); err != nil &&
		!errors.Is(err, blobstore.ErrNotFound) {
		return fmt.Errorf("delete unused prepared artifact %s: %w", artifactID, err)
	}
	return nil
}
