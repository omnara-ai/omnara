package identitystore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	MaxOwnedOrgsPerUser      = 1000
	MaxOrgMembershipsPerUser = 1000
	defaultProjectName       = "Default"
	DefaultProjectKey        = "default"
	resourceProjects         = "projects"
)

func checkOrgCreationCapacity(
	ctx context.Context,
	q *dbsqlc.Queries,
	userID ID,
) error {
	ownedCount, err := q.CountOwnedOrgMembershipsForUser(
		ctx,
		dbsqlc.CountOwnedOrgMembershipsForUserParams{UserID: userID},
	)
	if err != nil {
		return fmt.Errorf("count owned orgs: %w", err)
	}
	membershipCount, err := q.CountOrgMembershipsForUser(
		ctx,
		dbsqlc.CountOrgMembershipsForUserParams{UserID: userID},
	)
	if err != nil {
		return fmt.Errorf("count user org memberships: %w", err)
	}
	if ownedCount >= MaxOwnedOrgsPerUser || membershipCount >= MaxOrgMembershipsPerUser {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) ProvisionOrganizationTx(
	ctx context.Context,
	tx pgx.Tx,
	input ProvisionOrganizationInput,
) (CreateOrgForUserRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.UserID) {
		return CreateOrgForUserRecord{}, errors.New("org id and user id are required")
	}
	if input.Name == "" {
		return CreateOrgForUserRecord{}, errors.New("organization name is required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("organization name", input.Name)
	if err != nil {
		return CreateOrgForUserRecord{}, storeerr.InvalidRequest(err)
	}
	input.Name = normalizedName
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: input.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CreateOrgForUserRecord{}, storeerr.ErrNotFound
		}
		return CreateOrgForUserRecord{}, fmt.Errorf("lock organization creator: %w", err)
	}
	scopedKey := scopedOrgIdempotencyKey(input.UserID, input.IdempotencyKey)
	orgRow, err := qtx.CreateOrg(ctx, dbsqlc.CreateOrgParams{
		ID:             input.OrgID,
		Name:           input.Name,
		IdempotencyKey: storeutil.TextFromEmpty(scopedKey),
	})
	created := true
	var org OrgRecord
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		if scopedKey == "" {
			return CreateOrgForUserRecord{}, storeerr.ErrIdempotencyConflict
		}
		idempotentOrgRow, loadErr := qtx.GetOrgByIdempotencyKey(
			ctx,
			dbsqlc.GetOrgByIdempotencyKeyParams{IdempotencyKey: scopedKey},
		)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return CreateOrgForUserRecord{}, storeerr.ErrIdempotencyConflict
		}
		if loadErr != nil {
			return CreateOrgForUserRecord{}, fmt.Errorf("load idempotent org: %w", loadErr)
		}
		org = orgRecordFromGetByIdempotencySQLC(idempotentOrgRow)
		err = nil
	}
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return CreateOrgForUserRecord{}, storeerr.ErrIdempotencyConflict
		}
		return CreateOrgForUserRecord{}, fmt.Errorf("create org: %w", err)
	}
	if created {
		org = orgRecordFromSQLC(orgRow)
	}
	org.IdempotencyKey = input.IdempotencyKey
	org.Created = created
	if org.Name != input.Name {
		return CreateOrgForUserRecord{}, storeerr.ErrIdempotencyConflict
	}

	var membership OrgMembershipRecord
	if created {
		if err := checkOrgCreationCapacity(ctx, qtx, input.UserID); err != nil {
			return CreateOrgForUserRecord{}, err
		}
		membershipRow, err := qtx.AddUserOrgMembership(ctx, dbsqlc.AddUserOrgMembershipParams{
			OrgID:  org.ID,
			UserID: input.UserID,
			Role:   "owner",
		})
		if err != nil {
			return CreateOrgForUserRecord{}, fmt.Errorf("create owner membership: %w", err)
		}
		membership = userOrgMembershipRecord(
			membershipRow.ID,
			membershipRow.OrgID,
			input.UserID,
			membershipRow.Role,
			membershipRow.CreatedAt,
		)
	} else {
		membershipRow, err := qtx.LockUserOrgMembership(
			ctx,
			dbsqlc.LockUserOrgMembershipParams{OrgID: org.ID, UserID: input.UserID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return CreateOrgForUserRecord{}, storeerr.ErrUnauthorized
		}
		if err != nil {
			return CreateOrgForUserRecord{}, fmt.Errorf("load idempotent owner membership: %w", err)
		}
		membership = userOrgMembershipRecord(
			membershipRow.ID,
			membershipRow.OrgID,
			input.UserID,
			membershipRow.Role,
			membershipRow.CreatedAt,
		)
	}
	projectRow, err := qtx.CreateProject(ctx, dbsqlc.CreateProjectParams{
		OrgID:          org.ID,
		Name:           defaultProjectName,
		IdempotencyKey: storeutil.TextFromEmpty(DefaultProjectKey),
	})
	var project ProjectRecord
	if errors.Is(err, pgx.ErrNoRows) {
		idempotentProjectRow, loadErr := qtx.GetProjectByIdempotencyKey(
			ctx,
			dbsqlc.GetProjectByIdempotencyKeyParams{
				OrgID:          org.ID,
				IdempotencyKey: DefaultProjectKey,
			},
		)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			return CreateOrgForUserRecord{}, storeerr.ErrIdempotencyConflict
		}
		if loadErr != nil {
			return CreateOrgForUserRecord{}, fmt.Errorf("load idempotent default project: %w", loadErr)
		}
		project = projectRecordFromGetByIdempotencySQLC(idempotentProjectRow)
		err = nil
	}
	if err != nil {
		return CreateOrgForUserRecord{}, fmt.Errorf("create default project: %w", err)
	}
	if created && !isNilID(membership.ID) {
		defaultProjectID := projectRow.ID
		if defaultProjectID == NilID {
			defaultProjectID = project.ID
		}
		if _, err := qtx.AddProjectMembership(ctx, dbsqlc.AddProjectMembershipParams{
			OrgID:           org.ID,
			ProjectID:       defaultProjectID,
			OrgMembershipID: membership.ID,
			Role:            "admin",
		}); err != nil {
			return CreateOrgForUserRecord{}, fmt.Errorf("create default project admin membership: %w", err)
		}
	}
	if project.ID == NilID {
		project = projectRecordFromSQLC(projectRow)
	}
	project.Created = created
	return CreateOrgForUserRecord{
		Org:        org,
		Membership: membership,
		Project:    project,
		Created:    created,
	}, nil
}

func (s *Store) CreateProjectForPrincipal(
	ctx context.Context,
	input CreateProjectForPrincipalInput,
) (ProjectRecord, error) {
	if isNilID(input.OrgID) {
		return ProjectRecord{}, errors.New("org id is required")
	}
	if isNilID(input.Creator.ID) {
		return ProjectRecord{}, errors.New("project creator is required")
	}
	if input.Name == "" {
		return ProjectRecord{}, errors.New("project name is required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("project name", input.Name)
	if err != nil {
		return ProjectRecord{}, storeerr.InvalidRequest(err)
	}
	input.Name = normalizedName
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("begin create project: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	creatorMembership, err := getOrgMembershipForPrincipalTx(ctx, qtx, input.OrgID, input.Creator)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectRecord{}, storeerr.ErrUnauthorized
	} else if err != nil {
		return ProjectRecord{}, fmt.Errorf("load org membership for project creator: %w", err)
	}

	row, err := qtx.CreateProject(
		ctx,
		dbsqlc.CreateProjectParams{
			OrgID:          input.OrgID,
			Name:           input.Name,
			IdempotencyKey: storeutil.TextFromEmpty(input.IdempotencyKey),
		},
	)
	created := false
	var record ProjectRecord
	if err == nil {
		record = projectRecordFromSQLC(row)
		record.Created = true
		created = true
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return ProjectRecord{}, storeerr.ErrIdempotencyConflict
		}
		return ProjectRecord{}, fmt.Errorf("create project: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if input.IdempotencyKey == "" {
			return ProjectRecord{}, storeerr.ErrIdempotencyConflict
		}
		idempotentRow, err := qtx.GetProjectByIdempotencyKey(
			ctx,
			dbsqlc.GetProjectByIdempotencyKeyParams{
				OrgID:          input.OrgID,
				IdempotencyKey: input.IdempotencyKey,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			// The key is held by a soft-deleted project; the key stays consumed.
			return ProjectRecord{}, storeerr.ErrIdempotencyConflict
		}
		if err != nil {
			return ProjectRecord{}, fmt.Errorf("load idempotent project: %w", err)
		}
		record = projectRecordFromGetByIdempotencySQLC(idempotentRow)
		if record.Name != input.Name {
			return ProjectRecord{}, storeerr.ErrIdempotencyConflict
		}
	}
	if created {
		if err := resourceguard.Lock(ctx, qtx, resourceProjects, input.OrgID.String()); err != nil {
			return ProjectRecord{}, err
		}
		limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
		if err != nil {
			return ProjectRecord{}, err
		}
		projectCount, err := qtx.CountActiveProjectsForOrg(
			ctx,
			dbsqlc.CountActiveProjectsForOrgParams{OrgID: input.OrgID},
		)
		if err != nil {
			return ProjectRecord{}, fmt.Errorf("count active projects: %w", err)
		}
		if projectCount > limits.MaxActiveProjectsPerOrg {
			return ProjectRecord{}, resourceLimitExceeded(
				"active projects",
				limits.MaxActiveProjectsPerOrg,
			)
		}
		if _, err := qtx.AddProjectMembership(
			ctx,
			dbsqlc.AddProjectMembershipParams{
				OrgID:           input.OrgID,
				ProjectID:       record.ID,
				OrgMembershipID: creatorMembership.ID,
				Role:            "admin",
			},
		); err != nil {
			return ProjectRecord{}, fmt.Errorf("create project creator membership: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectRecord{}, fmt.Errorf("commit create project: %w", err)
	}
	return record, nil
}

type ListVisibleProjectsForPrincipalInput struct {
	OrgID     ID
	Principal PrincipalRecord
	Limit     int
	After     listing.KeysetCursor
}

type ListVisibleProjectsForPrincipalResult struct {
	Projects []VisibleProjectRecord
	HasMore  bool
}

func (s *Store) ListVisibleProjectsForPrincipal(
	ctx context.Context,
	input ListVisibleProjectsForPrincipalInput,
) (ListVisibleProjectsForPrincipalResult, error) {
	userID, orgAPIKeyID := AccountPrincipalIDs(input.Principal)
	if isNilID(input.OrgID) || (userID == nil && orgAPIKeyID == nil) {
		return ListVisibleProjectsForPrincipalResult{}, errors.New("org id and principal are required")
	}
	if input.Limit <= 0 {
		return ListVisibleProjectsForPrincipalResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListVisibleProjectRolesForPrincipalParams{
		OrgID:       input.OrgID,
		UserID:      userID,
		OrgApiKeyID: orgAPIKeyID,
		RowLimit:    int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListVisibleProjectRolesForPrincipal(ctx, params)
	if err != nil {
		return ListVisibleProjectsForPrincipalResult{}, fmt.Errorf("list visible projects for principal: %w", err)
	}
	out := make([]VisibleProjectRecord, 0, len(rows))
	index := make(map[ID]int)
	for _, row := range rows {
		i, ok := index[row.ID]
		if !ok {
			index[row.ID] = len(out)
			out = append(out, VisibleProjectRecord{Project: projectRecordFromVisibleSQLC(row)})
			i = len(out) - 1
		}
		out[i].Roles = append(out[i].Roles, row.Role)
	}
	result := ListVisibleProjectsForPrincipalResult{}
	if len(out) > input.Limit {
		result.HasMore = true
		out = out[:input.Limit]
	}
	result.Projects = out
	return result, nil
}

func (s *Store) GetOrg(ctx context.Context, orgID ID) (OrgRecord, error) {
	if isNilID(orgID) {
		return OrgRecord{}, errors.New("org id is required")
	}
	row, err := s.q.GetOrg(ctx, dbsqlc.GetOrgParams{ID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OrgRecord{}, fmt.Errorf("get org: %w", err)
	}
	return orgRecordFromGetSQLC(row), nil
}

func (s *Store) GetOrgCreationReplay(
	ctx context.Context,
	input GetOrgCreationReplayInput,
) (CreateOrgForUserRecord, bool, error) {
	if isNilID(input.UserID) {
		return CreateOrgForUserRecord{}, false, errors.New("user id is required")
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return CreateOrgForUserRecord{}, false, errors.New("idempotency key is required")
	}
	if input.Name == "" {
		return CreateOrgForUserRecord{}, false, errors.New("organization name is required")
	}
	scopedIdempotencyKey := scopedOrgIdempotencyKey(input.UserID, input.IdempotencyKey)
	orgRow, err := s.q.GetOrgByIdempotencyKey(ctx, dbsqlc.GetOrgByIdempotencyKeyParams{
		IdempotencyKey: scopedIdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateOrgForUserRecord{}, false, nil
	}
	if err != nil {
		return CreateOrgForUserRecord{}, false, fmt.Errorf("load organization creation replay: %w", err)
	}
	org := orgRecordFromGetByIdempotencySQLC(orgRow)
	if org.Name != input.Name {
		return CreateOrgForUserRecord{}, true, storeerr.ErrIdempotencyConflict
	}
	row, err := s.q.GetOrgCreationReplayForUser(ctx, dbsqlc.GetOrgCreationReplayForUserParams{
		UserID:         input.UserID,
		IdempotencyKey: scopedIdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateOrgForUserRecord{}, true, storeerr.ErrUnauthorized
	}
	if err != nil {
		return CreateOrgForUserRecord{}, false, fmt.Errorf("get organization creation replay: %w", err)
	}
	org.IdempotencyKey = input.IdempotencyKey
	org.Created = false
	return CreateOrgForUserRecord{
		Org: org,
		Membership: OrgMembershipRecord{
			OrgID:     row.OrgID,
			UserID:    input.UserID,
			Role:      row.MembershipRole,
			CreatedAt: row.MembershipCreatedAt,
		},
		Project: ProjectRecord{
			ID:             row.ProjectID,
			OrgID:          row.OrgID,
			Name:           row.ProjectName,
			IdempotencyKey: row.ProjectIdempotencyKey,
			CreatedAt:      row.ProjectCreatedAt,
			UpdatedAt:      row.ProjectUpdatedAt,
		},
		Created: false,
	}, true, nil
}

func scopedOrgIdempotencyKey(userID ID, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "user:" + userID.String() + ":" + key
}

func (s *Store) GetProject(ctx context.Context, orgID, projectID ID) (ProjectRecord, error) {
	if isNilID(orgID) {
		return ProjectRecord{}, errors.New("org id is required")
	}
	if isNilID(projectID) {
		return ProjectRecord{}, errors.New("project id is required")
	}
	row, err := s.q.GetProject(ctx, dbsqlc.GetProjectParams{OrgID: orgID, ID: projectID})
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("load project: %w", err)
	}
	return projectRecordFromGetSQLC(row), nil
}
