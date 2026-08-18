package skillstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateSkillRevision(ctx context.Context, input CreateSkillInput) (SkillRecord, error) {
	if err := s.validateCreateSkillInput(ctx, input); err != nil {
		return SkillRecord{}, err
	}
	if s.blobs == nil {
		return SkillRecord{}, errors.New("skill storage requires a blob store")
	}
	skillID, identityExists, err := s.findSkillIdentity(ctx, input)
	if err != nil {
		return SkillRecord{}, err
	}
	if !identityExists {
		skillID, err = uuid.NewV7()
		if err != nil {
			return SkillRecord{}, fmt.Errorf("generate skill id: %w", err)
		}
	}

	for {
		publicID, err := publicid.Encode(publicid.KindSkill, skillID)
		if err != nil {
			return SkillRecord{}, fmt.Errorf("encode skill public id: %w", err)
		}

		revisionID, err := uuid.NewV7()
		if err != nil {
			return SkillRecord{}, fmt.Errorf("generate skill revision id: %w", err)
		}
		publicRevisionID, err := publicid.Encode(publicid.KindSkillRevision, revisionID)
		if err != nil {
			return SkillRecord{}, fmt.Errorf("encode skill revision public id: %w", err)
		}
		blobKey := skillops.ArchiveKey(publicID, publicRevisionID)
		blobMeta, err := s.blobs.PutBlob(ctx, blobKey, input.ArchiveBytes)
		if err != nil {
			return SkillRecord{}, fmt.Errorf("upload skill archive: %w", err)
		}
		cleanupBlob := func() {
			_ = s.blobs.DeleteBlob(context.WithoutCancel(ctx), blobKey)
		}
		if blobMeta.Digest == "" {
			cleanupBlob()
			return SkillRecord{}, errors.New("blob store did not return an archive digest")
		}

		insertResult, err := s.insertSkillRevision(
			ctx,
			skillID,
			revisionID,
			!identityExists,
			input,
			blobMeta.Digest,
		)
		if err != nil {
			if !insertResult.databaseMayReferenceArchive {
				cleanupBlob()
			}
			return SkillRecord{}, err
		}
		if !isNilUUID(insertResult.retrySkillID) {
			cleanupBlob()
			skillID = insertResult.retrySkillID
			identityExists = true
			continue
		}
		return insertResult.record, nil
	}
}

func (s *Store) insertSkillRevision(
	ctx context.Context,
	skillID, revisionID uuid.UUID,
	createIdentity bool,
	input CreateSkillInput,
	archiveDigest string,
) (skillRevisionInsertResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return skillRevisionInsertResult{}, fmt.Errorf("begin create skill revision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if isNilUUID(input.OwnerProjectID) {
		if err := lifecyclelock.EnterActiveOrganization(ctx, tx, input.OrgID); err != nil {
			return skillRevisionInsertResult{}, err
		}
	} else if err := lifecyclelock.EnterActiveProject(
		ctx,
		tx,
		input.OrgID,
		input.OwnerProjectID,
	); err != nil {
		return skillRevisionInsertResult{}, err
	}
	if createIdentity {
		if err := resourceguard.Lock(
			ctx,
			qtx,
			"skills",
			resourceguard.OwnerScope(
				input.OrgID,
				input.OwnerKind,
				input.OwnerProjectID,
				input.OwnerUserID,
			),
		); err != nil {
			return skillRevisionInsertResult{}, err
		}
		existing, err := qtx.GetSkillIDByName(ctx, skillIdentityLookupParams(input))
		if err == nil {
			if existing != skillID {
				return skillRevisionInsertResult{retrySkillID: existing}, nil
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return skillRevisionInsertResult{}, fmt.Errorf("look up skill by name: %w", err)
		} else {
			limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
			if err != nil {
				return skillRevisionInsertResult{}, err
			}
			skillCount, err := qtx.CountActiveSkillsForOwner(
				ctx,
				dbsqlc.CountActiveSkillsForOwnerParams{
					OrgID:          input.OrgID,
					OwnerKind:      input.OwnerKind,
					OwnerProjectID: sqlcUUIDFromNil(input.OwnerProjectID),
					OwnerUserID:    sqlcUUIDFromNil(input.OwnerUserID),
				},
			)
			if err != nil {
				return skillRevisionInsertResult{}, fmt.Errorf("count active skills: %w", err)
			}
			if skillCount >= limits.MaxActiveSkillsPerOwner {
				return skillRevisionInsertResult{}, fmt.Errorf(
					"active skills limit of %d reached: %w",
					limits.MaxActiveSkillsPerOwner,
					storeerr.ErrConflict,
				)
			}
			if _, err := qtx.InsertSkill(ctx, dbsqlc.InsertSkillParams{
				ID:             skillID,
				OrgID:          input.OrgID,
				OwnerKind:      input.OwnerKind,
				OwnerProjectID: sqlcUUIDFromNil(input.OwnerProjectID),
				OwnerUserID:    sqlcUUIDFromNil(input.OwnerUserID),
				Name:           input.Name,
			}); err != nil {
				return skillRevisionInsertResult{}, fmt.Errorf("insert skill: %w", err)
			}
		}
	}
	if err := lockSkillTx(ctx, qtx, input.OrgID, skillID); err != nil {
		return skillRevisionInsertResult{}, err
	}
	revision, err := qtx.NextSkillRevision(ctx, dbsqlc.NextSkillRevisionParams{SkillID: skillID})
	if err != nil {
		return skillRevisionInsertResult{}, fmt.Errorf("compute next skill revision: %w", err)
	}
	if _, err := qtx.InsertSkillRevision(ctx, dbsqlc.InsertSkillRevisionParams{
		ID:            revisionID,
		SkillID:       skillID,
		Revision:      revision,
		Description:   input.Description,
		SkillMd:       input.SkillMd,
		ArchiveDigest: archiveDigest,
	}); err != nil {
		return skillRevisionInsertResult{}, fmt.Errorf("insert skill revision: %w", err)
	}
	row, err := qtx.GetSkillByID(ctx, dbsqlc.GetSkillByIDParams{ID: skillID})
	if err != nil {
		return skillRevisionInsertResult{}, fmt.Errorf("get skill after revision insert: %w", err)
	}
	result := skillRevisionInsertResult{record: skillRecordFromSQLC(row)}
	if err := tx.Commit(ctx); err != nil {
		result.databaseMayReferenceArchive = !errors.Is(err, pgx.ErrTxCommitRollback)
		return result, fmt.Errorf("commit create skill revision: %w", err)
	}
	result.databaseMayReferenceArchive = true
	return result, nil
}

func skillIdentityLookupParams(input CreateSkillInput) dbsqlc.GetSkillIDByNameParams {
	return dbsqlc.GetSkillIDByNameParams{
		OrgID:          input.OrgID,
		OwnerKind:      input.OwnerKind,
		Name:           input.Name,
		OwnerProjectID: sqlcUUIDFromNil(input.OwnerProjectID),
		OwnerUserID:    sqlcUUIDFromNil(input.OwnerUserID),
	}
}

func (s *Store) findSkillIdentity(ctx context.Context, input CreateSkillInput) (uuid.UUID, bool, error) {
	existing, err := s.q.GetSkillIDByName(ctx, skillIdentityLookupParams(input))
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("look up skill by name: %w", err)
	}
	return uuid.Nil, false, nil
}
