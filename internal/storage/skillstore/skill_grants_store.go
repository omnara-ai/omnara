package skillstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateSkillGrant(
	ctx context.Context,
	input CreateSkillGrantInput,
) (SkillGrantRecord, error) {
	if isNilUUID(input.OrgID) || isNilUUID(input.SkillID) || isNilUUID(input.TargetProjectID) ||
		isNilUUID(input.Actor.ID) {
		return SkillGrantRecord{}, invalidSkillRequest("org, skill, target project, and actor are required")
	}
	skill, err := s.getSkill(ctx, input.OrgID, input.SkillID)
	if err != nil {
		return SkillGrantRecord{}, err
	}
	if err := s.authorizeSkillManage(ctx, skill, input.Actor); err != nil {
		return SkillGrantRecord{}, err
	}
	if skill.OwnerKind == SkillOwnerProject && skill.OwnerProjectID == input.TargetProjectID {
		return SkillGrantRecord{}, invalidSkillRequest("skill is already available to its owner project")
	}
	if _, err := s.access.GetProject(ctx, input.OrgID, input.TargetProjectID); err != nil {
		if storeerr.IsNotFound(err) {
			return SkillGrantRecord{}, storeerr.ErrNotFound
		}
		return SkillGrantRecord{}, err
	}
	if err := s.authorizeProjectSkillManage(ctx, input.OrgID, input.TargetProjectID, input.Actor); err != nil {
		return SkillGrantRecord{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return SkillGrantRecord{}, fmt.Errorf("generate skill grant id: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SkillGrantRecord{}, fmt.Errorf("begin create skill grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	projectIDs := []uuid.UUID{input.TargetProjectID}
	if skill.OwnerKind == SkillOwnerProject {
		projectIDs = append(projectIDs, skill.OwnerProjectID)
	}
	if err := lifecyclelock.EnterActiveProjects(ctx, tx, input.OrgID, projectIDs); err != nil {
		return SkillGrantRecord{}, err
	}
	if err := lockSkillTx(ctx, qtx, input.OrgID, input.SkillID); err != nil {
		return SkillGrantRecord{}, err
	}
	row, err := qtx.InsertSkillGrant(ctx, dbsqlc.InsertSkillGrantParams{
		ID: id, OrgID: input.OrgID, SkillID: input.SkillID,
		TargetProjectID: input.TargetProjectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SkillGrantRecord{}, storeerr.ErrNotFound
		}
		if storeutil.IsUniqueViolation(err) {
			return SkillGrantRecord{}, storeerr.ErrConflict
		}
		return SkillGrantRecord{}, fmt.Errorf("insert skill grant: %w", err)
	}
	record := skillGrantRecordFromSQLC(row)
	if err := tx.Commit(ctx); err != nil {
		return SkillGrantRecord{}, fmt.Errorf("commit create skill grant: %w", err)
	}
	return record, nil
}

func (s *Store) ListSkillGrants(ctx context.Context, input ListSkillGrantsInput) (ListSkillGrantsResult, error) {
	if isNilUUID(input.OrgID) || isNilUUID(input.SkillID) || isNilUUID(input.Actor.ID) {
		return ListSkillGrantsResult{}, invalidSkillRequest("org, skill, and actor are required")
	}
	if input.Limit <= 0 {
		return ListSkillGrantsResult{}, invalidSkillRequest("limit must be positive")
	}
	skill, err := s.getSkill(ctx, input.OrgID, input.SkillID)
	if err != nil {
		return ListSkillGrantsResult{}, err
	}
	if err := s.authorizeSkillManage(ctx, skill, input.Actor); err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) {
			return ListSkillGrantsResult{}, storeerr.ErrNotFound
		}
		return ListSkillGrantsResult{}, err
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListSkillGrantsBySkillParams{
		OrgID: input.OrgID, SkillID: input.SkillID, RowLimit: int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc,
		NamePattern: input.List.NamePattern, TargetProjectID: sqlcUUIDFromNil(input.TargetProjectID),
	}
	if !listing.SortAllowed(input.List.SortField, "name", "created_at") {
		return ListSkillGrantsResult{}, invalidSkillRequest("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListSkillGrantsBySkill(ctx, params)
	if err != nil {
		return ListSkillGrantsResult{}, fmt.Errorf("list skill grants: %w", err)
	}
	result := ListSkillGrantsResult{Grants: make([]SkillGrantListRecord, 0, min(len(rows), input.Limit))}
	if len(rows) > input.Limit {
		result.HasMore, rows = true, rows[:input.Limit]
	}
	for _, row := range rows {
		result.Grants = append(result.Grants, SkillGrantListRecord{
			Grant: SkillGrantRecord{ID: row.ID, OrgID: row.OrgID, SkillID: row.SkillID,
				TargetProjectID: row.TargetProjectID, CreatedAt: row.CreatedAt},
			TargetProject: ProjectRecord{ID: row.TargetProjectID, OrgID: row.OrgID,
				Name: row.TargetProjectName, CreatedAt: row.TargetProjectCreatedAt, UpdatedAt: row.TargetProjectUpdatedAt},
		})
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) DeleteSkillGrant(ctx context.Context, input DeleteSkillGrantInput) (SkillGrantRecord, error) {
	if isNilUUID(input.OrgID) || isNilUUID(input.SkillID) || isNilUUID(input.GrantID) || isNilUUID(input.Actor.ID) {
		return SkillGrantRecord{}, invalidSkillRequest("org, skill, grant, and actor are required")
	}
	grant, err := s.q.GetSkillGrantForSourceSkill(ctx, dbsqlc.GetSkillGrantForSourceSkillParams{
		OrgID: input.OrgID, SkillID: input.SkillID, ID: input.GrantID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return SkillGrantRecord{}, fmt.Errorf("get skill grant: %w", err)
	}
	skill, err := s.getSkill(ctx, input.OrgID, input.SkillID)
	if err != nil {
		return SkillGrantRecord{}, err
	}
	if err := s.authorizeSkillGrantDelete(ctx, skill, grant.TargetProjectID, input.Actor); err != nil {
		return SkillGrantRecord{}, err
	}
	deleted, err := s.q.DeleteSkillGrant(ctx, dbsqlc.DeleteSkillGrantParams{
		OrgID: input.OrgID, ID: input.GrantID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return SkillGrantRecord{}, fmt.Errorf("delete skill grant: %w", err)
	}
	return skillGrantRecordFromSQLC(deleted), nil
}

func skillGrantRecordFromSQLC(row dbsqlc.SkillGrant) SkillGrantRecord {
	return SkillGrantRecord{
		ID: row.ID, OrgID: row.OrgID, SkillID: row.SkillID,
		TargetProjectID: row.TargetProjectID,
		CreatedAt:       row.CreatedAt,
	}
}
