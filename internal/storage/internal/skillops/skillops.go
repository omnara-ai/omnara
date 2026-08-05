package skillops

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

var (
	ErrConflict = errors.New("skill lifecycle conflict")
	ErrNotFound = errors.New("skill not found")
)

type ArchiveRef struct {
	SkillID    uuid.UUID
	RevisionID uuid.UUID
}

// ArchiveKey uses immutable public IDs so concurrent uploads cannot share a
// key, even when they target the same logical skill.
func ArchiveKey(publicSkillID, publicRevisionID string) string {
	return "skills/" + publicSkillID + "/revisions/" + publicRevisionID + "/archive"
}

func Lock(ctx context.Context, q *dbsqlc.Queries, orgID, skillID uuid.UUID) error {
	_, err := q.LockSkill(ctx, dbsqlc.LockSkillParams{OrgID: orgID, ID: skillID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock skill: %w", err)
	}
	return nil
}

// Delete soft deletes a skill and its revisions, removes grants, and returns
// archives that must only be purged after the surrounding transaction commits.
func Delete(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID, skillID uuid.UUID,
) ([]ArchiveRef, error) {
	if err := Lock(ctx, q, orgID, skillID); err != nil {
		return nil, err
	}
	publicSkillID, err := publicid.Encode(publicid.KindSkill, skillID)
	if err != nil {
		return nil, fmt.Errorf("encode skill public id: %w", err)
	}
	referenced, err := q.SkillHasActiveAgentReferences(ctx, dbsqlc.SkillHasActiveAgentReferencesParams{
		OrgID: orgID, SkillPublicID: publicSkillID,
	})
	if err != nil {
		return nil, fmt.Errorf("check skill agent references: %w", err)
	}
	if referenced {
		return nil, fmt.Errorf("skill is referenced by an active agent configuration: %w", ErrConflict)
	}
	revisionIDs, err := q.ListSkillRevisionIDs(ctx, dbsqlc.ListSkillRevisionIDsParams{SkillID: skillID})
	if err != nil {
		return nil, fmt.Errorf("list skill revisions: %w", err)
	}
	rows, err := q.DeleteSkill(ctx, dbsqlc.DeleteSkillParams{OrgID: orgID, ID: skillID})
	if err != nil {
		return nil, fmt.Errorf("delete skill: %w", err)
	}
	if rows == 0 {
		return nil, ErrNotFound
	}
	if err := q.DeleteSkillRevisions(ctx, dbsqlc.DeleteSkillRevisionsParams{SkillID: skillID}); err != nil {
		return nil, fmt.Errorf("delete skill revisions: %w", err)
	}
	if err := q.DeleteSkillGrants(ctx, dbsqlc.DeleteSkillGrantsParams{OrgID: orgID, SkillID: skillID}); err != nil {
		return nil, fmt.Errorf("delete skill grants: %w", err)
	}
	archives := make([]ArchiveRef, 0, len(revisionIDs))
	for _, revisionID := range revisionIDs {
		archives = append(archives, ArchiveRef{SkillID: skillID, RevisionID: revisionID})
	}
	return archives, nil
}

func ListArchiveRefs(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID uuid.UUID,
	ownerProjectID *uuid.UUID,
) ([]ArchiveRef, error) {
	rows, err := q.ListSkillRevisionKeysForOwner(ctx, dbsqlc.ListSkillRevisionKeysForOwnerParams{
		OrgID: orgID, OwnerProjectID: ownerProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("list skill revisions for archive purge: %w", err)
	}
	refs := make([]ArchiveRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, ArchiveRef{SkillID: row.SkillID, RevisionID: row.ID})
	}
	return refs, nil
}

// Purge best-effort deletes archives after a successful database commit. The
// soft-deleted revision rows retain their metadata and instructions.
func Purge(ctx context.Context, blobs blobstore.Store, archives []ArchiveRef) {
	if blobs == nil {
		return
	}
	for _, archive := range archives {
		publicSkillID, err := publicid.Encode(publicid.KindSkill, archive.SkillID)
		if err != nil {
			continue
		}
		publicRevisionID, err := publicid.Encode(publicid.KindSkillRevision, archive.RevisionID)
		if err != nil {
			continue
		}
		_ = blobs.DeleteBlob(ctx, ArchiveKey(publicSkillID, publicRevisionID))
	}
}
