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

// DeleteUnreferencedPreparedArtifact is a best-effort cleanup for a frozen key
// whose owning inbox receipt has committed discard. The caller must establish
// that terminal state first: a failed admission or expired lease is insufficient.
// Discard fences future admission at these server-generated IDs, so the reference
// check and blob deletion need no transaction spanning external I/O. A stale
// uploader can recreate unused bytes afterward; this is not garbage collection.
func (s *Store) DeleteUnreferencedPreparedArtifact(
	ctx context.Context,
	projectID, agentID, artifactID uuid.UUID,
) error {
	if projectID == uuid.Nil || agentID == uuid.Nil || artifactID == uuid.Nil {
		return storeerr.InvalidRequest(errors.New("project, planned agent and artifact IDs are required"))
	}
	if s.blobs == nil {
		return ErrBlobStoreNotConfigured
	}
	// A planned launch may have no agent row. An existing agent must belong to
	// this project; otherwise a scoped artifact miss would not prove absence.
	agent, err := s.q.GetAgent(ctx, dbsqlc.GetAgentParams{ID: agentID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check prepared artifact agent: %w", err)
	}
	if err == nil && agent.ProjectID != projectID {
		return storeerr.ErrUnauthorized
	}
	if _, err := s.GetArtifact(ctx, projectID, agentID, artifactID); err == nil {
		return nil // Durable references win, even for an archived/deleted scope.
	} else if !errors.Is(err, storeerr.ErrNotFound) {
		return err
	}
	if err := s.blobs.DeleteBlob(ctx, artifactObjectKey(agentID, artifactID)); err != nil &&
		!errors.Is(err, blobstore.ErrNotFound) {
		return fmt.Errorf("delete unused prepared artifact %s: %w", artifactID, err)
	}
	return nil
}
