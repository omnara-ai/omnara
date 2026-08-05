package skillstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func decodeSkillPublicID(raw string) (uuid.UUID, bool) {
	id, err := publicid.Decode(publicid.KindSkill, raw)
	return id, err == nil
}

type DeleteSkillInput struct {
	OrgID   uuid.UUID
	SkillID uuid.UUID
	Actor   identitystore.PrincipalRecord
}

func (s *Store) DeleteSkill(ctx context.Context, input DeleteSkillInput) error {
	if isNilUUID(input.OrgID) || isNilUUID(input.SkillID) || isNilUUID(input.Actor.ID) {
		return invalidSkillRequest("org, skill, and actor are required")
	}
	row, err := s.q.GetSkillForOrg(ctx, dbsqlc.GetSkillForOrgParams{
		OrgID: input.OrgID,
		ID:    input.SkillID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get skill for delete: %w", err)
	}
	record := skillRecordFromSQLC(dbsqlc.GetSkillByIDRow(row))
	if err := s.authorizeSkillManage(ctx, record, input.Actor); err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) {
			return storeerr.ErrNotFound
		}
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete skill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	archives, err := skillops.Delete(ctx, dbsqlc.New(tx), input.OrgID, input.SkillID)
	if err != nil {
		return mapSkillOpsError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete skill: %w", err)
	}
	skillops.Purge(ctx, s.blobs, archives)
	return nil
}

func lockSkillTx(ctx context.Context, qtx *dbsqlc.Queries, orgID, skillID uuid.UUID) error {
	return mapSkillOpsError(skillops.Lock(ctx, qtx, orgID, skillID))
}

func mapSkillOpsError(err error) error {
	switch {
	case errors.Is(err, skillops.ErrNotFound):
		return fmt.Errorf("%w: %w", err, storeerr.ErrNotFound)
	case errors.Is(err, skillops.ErrConflict):
		return fmt.Errorf("%w: %w", err, storeerr.ErrConflict)
	default:
		return err
	}
}
