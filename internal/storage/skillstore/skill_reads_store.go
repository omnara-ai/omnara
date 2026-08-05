package skillstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) GetSkillByPublicID(ctx context.Context, orgID uuid.UUID, publicSkillID string) (SkillRecord, error) {
	id, err := publicid.Decode(publicid.KindSkill, publicSkillID)
	if err != nil {
		return SkillRecord{}, fmt.Errorf("decode skill id: %w", err)
	}
	row, err := s.q.GetSkillForOrg(ctx, dbsqlc.GetSkillForOrgParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return SkillRecord{}, fmt.Errorf("get skill: %w", err)
	}
	return skillRecordFromSQLC(dbsqlc.GetSkillByIDRow(row)), nil
}

func (s *Store) getSkill(ctx context.Context, orgID, skillID uuid.UUID) (SkillRecord, error) {
	row, err := s.q.GetSkillForOrg(ctx, dbsqlc.GetSkillForOrgParams{OrgID: orgID, ID: skillID})
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return SkillRecord{}, fmt.Errorf("get skill: %w", err)
	}
	return skillRecordFromSQLC(dbsqlc.GetSkillByIDRow(row)), nil
}

func (s *Store) GetVisibleSkill(
	ctx context.Context,
	orgID uuid.UUID,
	publicSkillID string,
	actor identitystore.PrincipalRecord,
) (SkillRecord, error) {
	record, err := s.GetSkillByPublicID(ctx, orgID, publicSkillID)
	if err != nil {
		return SkillRecord{}, err
	}
	if err := s.authorizeSkillRead(ctx, record, actor); err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) {
			return SkillRecord{}, storeerr.ErrNotFound
		}
		return SkillRecord{}, err
	}
	return record, nil
}

// GetSkillForDispatch resolves the latest revision of a skill for an agent
// turn. Skills must either belong to the project or still have an active grant
// to it. Rechecking availability here makes a revoked grant effective for
// already-compiled agent configs.
func (s *Store) GetSkillForDispatch(
	ctx context.Context,
	projectID uuid.UUID,
	publicSkillID string,
) (SkillRecord, error) {
	if isNilUUID(projectID) {
		return SkillRecord{}, errors.New("project id is required")
	}
	id, err := publicid.Decode(publicid.KindSkill, publicSkillID)
	if err != nil {
		return SkillRecord{}, fmt.Errorf("decode skill id: %w", err)
	}
	row, err := s.q.GetSkillForDispatch(ctx, dbsqlc.GetSkillForDispatchParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return SkillRecord{}, fmt.Errorf("get skill for dispatch: %w", err)
	}
	return skillRecordFromSQLC(dbsqlc.GetSkillByIDRow(row)), nil
}

func (s *Store) LoadSkillArchive(
	ctx context.Context,
	publicSkillID, publicRevisionID string,
) ([]byte, blobstore.Metadata, error) {
	if s.blobs == nil {
		return nil, blobstore.Metadata{}, errors.New("skill storage requires a blob store")
	}
	if _, err := publicid.Decode(publicid.KindSkillRevision, publicRevisionID); err != nil {
		return nil, blobstore.Metadata{}, fmt.Errorf("decode skill revision id: %w", err)
	}
	return s.blobs.GetBlob(ctx, skillops.ArchiveKey(publicSkillID, publicRevisionID))
}

type SkillListFilters struct {
	OwnerKind      string
	OwnerProjectID uuid.UUID
}

type ListSkillsInput struct {
	OrgID   uuid.UUID
	Actor   identitystore.PrincipalRecord
	Filters SkillListFilters
	Limit   int
	List    listing.Options
}

type ListSkillsResult struct {
	Skills  []SkillRecord
	HasMore bool
	Next    listing.Cursor
}

type ProjectAvailableSkillFilters struct {
	OwnerKinds          []string
	AvailabilitySources []string
}

type ListProjectAvailableSkillsInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Filters   ProjectAvailableSkillFilters
	List      listing.Options
	Limit     int
}

type ListProjectSkillAccessesResult struct {
	Accesses []ProjectSkillAccessRecord
	HasMore  bool
	Next     listing.Cursor
}

func (s *Store) ListSkills(ctx context.Context, input ListSkillsInput) (ListSkillsResult, error) {
	if isNilUUID(input.OrgID) || isNilUUID(input.Actor.ID) {
		return ListSkillsResult{}, invalidSkillRequest("org and actor are required")
	}
	if input.Limit <= 0 {
		return ListSkillsResult{}, invalidSkillRequest("limit must be positive")
	}
	if err := s.requireOrgMember(ctx, input.OrgID, input.Actor); err != nil {
		return ListSkillsResult{}, err
	}
	if input.Filters.OwnerKind != "" && input.Filters.OwnerKind != SkillOwnerOrg &&
		input.Filters.OwnerKind != SkillOwnerProject && input.Filters.OwnerKind != SkillOwnerUser {
		return ListSkillsResult{}, invalidSkillRequest("unsupported owner kind")
	}
	if input.Filters.OwnerKind == SkillOwnerProject {
		if isNilUUID(input.Filters.OwnerProjectID) {
			return ListSkillsResult{}, invalidSkillRequest("owner project is required for project owner filter")
		}
		if err := s.authorizeSkillRead(ctx, SkillRecord{
			OrgID: input.OrgID, OwnerKind: SkillOwnerProject,
			OwnerProjectID: input.Filters.OwnerProjectID,
		}, input.Actor); err != nil {
			return ListSkillsResult{}, err
		}
	} else if !isNilUUID(input.Filters.OwnerProjectID) {
		return ListSkillsResult{}, invalidSkillRequest("owner project requires project owner filter")
	} else if input.Filters.OwnerKind == SkillOwnerOrg {
		if err := s.authorizeSkillRead(ctx, SkillRecord{
			OrgID: input.OrgID, OwnerKind: SkillOwnerOrg,
		}, input.Actor); err != nil {
			return ListSkillsResult{}, err
		}
	}
	actorUserID, actorOrgAPIKeyID := identitystore.AccountPrincipalIDs(input.Actor)
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListVisibleOwnedSkillsParams{
		OrgID:          input.OrgID,
		OwnerKind:      input.Filters.OwnerKind,
		OwnerProjectID: sqlcUUIDFromNil(input.Filters.OwnerProjectID),
		UserID:         actorUserID,
		OrgApiKeyID:    actorOrgAPIKeyID,
		RowLimit:       int64(input.Limit) + 1,
		SortField:      input.List.SortField,
		SortDesc:       input.List.SortDesc,
		NamePattern:    input.List.NamePattern,
	}
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at", "owner_kind") {
		return ListSkillsResult{}, invalidSkillRequest("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListVisibleOwnedSkills(ctx, params)
	if err != nil {
		return ListSkillsResult{}, fmt.Errorf("list visible owned skills: %w", err)
	}
	result := ListSkillsResult{Skills: make([]SkillRecord, 0, min(len(rows), input.Limit))}
	if len(rows) > input.Limit {
		result.HasMore, rows = true, rows[:input.Limit]
	}
	for _, row := range rows {
		result.Skills = append(result.Skills, skillRecordFromSQLC(dbsqlc.GetSkillByIDRow{
			ID: row.ID, OrgID: row.OrgID, OwnerKind: row.OwnerKind,
			OwnerProjectID: row.OwnerProjectID, OwnerUserID: row.OwnerUserID,
			Name: row.Name, CreatedAt: row.CreatedAt,
			RevisionID: row.RevisionID, Revision: row.Revision, Description: row.Description,
			SkillMd: row.SkillMd, ArchiveDigest: row.ArchiveDigest, UpdatedAt: row.UpdatedAt,
		}))
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) ListProjectAvailableSkills(
	ctx context.Context,
	input ListProjectAvailableSkillsInput,
) (ListProjectSkillAccessesResult, error) {
	if isNilUUID(input.OrgID) || isNilUUID(input.ProjectID) {
		return ListProjectSkillAccessesResult{}, invalidSkillRequest("org and project are required")
	}
	if input.Limit <= 0 {
		return ListProjectSkillAccessesResult{}, invalidSkillRequest("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListProjectAvailableSkillsParams{
		OrgID: input.OrgID, ProjectID: input.ProjectID, RowLimit: int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc,
		NamePattern: input.List.NamePattern, OwnerKinds: input.Filters.OwnerKinds,
		AvailabilitySources: input.Filters.AvailabilitySources,
	}
	if !listing.SortAllowed(
		input.List.SortField,
		"name",
		"created_at",
		"updated_at",
		"owner_kind",
		"availability_source",
	) {
		return ListProjectSkillAccessesResult{}, invalidSkillRequest("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListProjectAvailableSkills(ctx, params)
	if err != nil {
		return ListProjectSkillAccessesResult{}, fmt.Errorf("list project available skills: %w", err)
	}
	result := ListProjectSkillAccessesResult{
		Accesses: make([]ProjectSkillAccessRecord, 0, min(len(rows), input.Limit)),
	}
	if len(rows) > input.Limit {
		result.HasMore, rows = true, rows[:input.Limit]
	}
	for _, row := range rows {
		skill := skillRecordFromSQLC(dbsqlc.GetSkillByIDRow{
			ID: row.ID, OrgID: row.OrgID, OwnerKind: row.OwnerKind,
			OwnerProjectID: row.OwnerProjectID, OwnerUserID: row.OwnerUserID,
			Name: row.Name, CreatedAt: row.CreatedAt,
			RevisionID: row.RevisionID, Revision: row.Revision, Description: row.Description,
			SkillMd: row.SkillMd, ArchiveDigest: row.ArchiveDigest, UpdatedAt: row.UpdatedAt,
		})
		access := ProjectSkillAccessRecord{Skill: skill, ProjectID: input.ProjectID, Availability: row.AvailabilitySource}
		if row.GrantID != nil {
			access.Availability = SkillAvailabilityGrant
			access.GrantID = *row.GrantID
		}
		result.Accesses = append(result.Accesses, access)
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

type GetSkillsByIDsInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	IDs       []string
}

type CreateSkillGrantInput struct {
	OrgID           uuid.UUID
	SkillID         uuid.UUID
	TargetProjectID uuid.UUID
	Actor           identitystore.PrincipalRecord
}

type ListSkillGrantsInput struct {
	OrgID           uuid.UUID
	SkillID         uuid.UUID
	Actor           identitystore.PrincipalRecord
	Limit           int
	TargetProjectID uuid.UUID
	List            listing.Options
}

type ListSkillGrantsResult struct {
	Grants  []SkillGrantListRecord
	HasMore bool
	Next    listing.Cursor
}

type DeleteSkillGrantInput struct {
	OrgID   uuid.UUID
	SkillID uuid.UUID
	GrantID uuid.UUID
	Actor   identitystore.PrincipalRecord
}

func (s *Store) GetSkillsByIDsForCompile(
	ctx context.Context,
	input GetSkillsByIDsInput,
) ([]SkillRecord, []string, error) {
	if isNilUUID(input.OrgID) {
		return nil, nil, errors.New("org id is required")
	}
	if len(input.IDs) == 0 {
		return nil, nil, nil
	}
	uniq := make([]uuid.UUID, 0, len(input.IDs))
	seen := map[uuid.UUID]string{}
	inputOrder := make([]uuid.UUID, 0, len(input.IDs))
	for _, raw := range input.IDs {
		id, ok := decodeSkillPublicID(raw)
		if !ok {
			return nil, []string{raw}, nil
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = raw
		uniq = append(uniq, id)
		inputOrder = append(inputOrder, id)
	}
	rows, err := s.q.ListSkillsByIDs(ctx, dbsqlc.ListSkillsByIDsParams{OrgID: input.OrgID, Ids: uniq})
	if err != nil {
		return nil, nil, fmt.Errorf("list skills by ids: %w", err)
	}
	byID := make(map[uuid.UUID]SkillRecord, len(rows))
	for _, row := range rows {
		rec := skillRecordFromSQLC(dbsqlc.GetSkillByIDRow(row))
		visible, err := s.skillVisibleForCompile(ctx, rec, input)
		if err != nil {
			return nil, nil, err
		}
		if !visible {
			continue
		}
		byID[rec.ID] = rec
	}
	out := make([]SkillRecord, 0, len(byID))
	var missing []string
	for _, id := range inputOrder {
		if rec, ok := byID[id]; ok {
			out = append(out, rec)
		} else {
			missing = append(missing, seen[id])
		}
	}
	return out, missing, nil
}

func skillRecordFromSQLC(row dbsqlc.GetSkillByIDRow) SkillRecord {
	return SkillRecord{
		ID:             row.ID,
		OrgID:          row.OrgID,
		OwnerKind:      row.OwnerKind,
		OwnerProjectID: uuidFromSQLCPtr(row.OwnerProjectID),
		OwnerUserID:    uuidFromSQLCPtr(row.OwnerUserID),
		Name:           row.Name,
		RevisionID:     row.RevisionID,
		Revision:       row.Revision,
		Description:    row.Description,
		SkillMd:        row.SkillMd,
		ArchiveDigest:  row.ArchiveDigest,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}
