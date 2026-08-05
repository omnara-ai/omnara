package secretstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateSecretGrant(
	ctx context.Context,
	input CreateSecretGrantInput,
) (SecretGrantRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.TargetProjectID) ||
		isNilID(input.Actor.ID) {
		return SecretGrantRecord{}, invalidSecretRequest(
			"org, secret, target project, and actor are required",
		)
	}
	secret, err := s.GetSecret(ctx, input.OrgID, input.SecretID)
	if err != nil {
		return SecretGrantRecord{}, err
	}
	if err := management.RequireTenant(secret.ManagementKind, "secrets"); err != nil {
		return SecretGrantRecord{}, err
	}
	if err := s.authorizeSecretManage(ctx, secret, input.Actor); err != nil {
		return SecretGrantRecord{}, err
	}
	if secret.OwnerKind == SecretOwnerProject && secret.OwnerProjectID == input.TargetProjectID {
		return SecretGrantRecord{}, invalidSecretRequest("secret is already available to its owner project")
	}
	if _, err := s.access.GetProject(ctx, input.OrgID, input.TargetProjectID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretGrantRecord{}, storeerr.ErrNotFound
		}
		return SecretGrantRecord{}, err
	}
	if err := s.authorizeProjectSecretsManage(
		ctx,
		input.OrgID,
		input.TargetProjectID,
		input.Actor,
	); err != nil {
		return SecretGrantRecord{}, err
	}
	grantID, err := newSecretUUID()
	if err != nil {
		return SecretGrantRecord{}, err
	}
	row, err := s.q.InsertSecretGrant(
		ctx,
		dbsqlc.InsertSecretGrantParams{
			ID:              grantID,
			OrgID:           input.OrgID,
			SecretID:        input.SecretID,
			TargetProjectID: input.TargetProjectID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretGrantRecord{}, storeerr.ErrNotFound
		}
		if storeutil.IsUniqueViolation(err) {
			return SecretGrantRecord{}, storeerr.ErrConflict
		}
		return SecretGrantRecord{}, fmt.Errorf("insert secret grant: %w", err)
	}
	grant := secretGrantFromInsert(row)
	return grant, nil
}

func (s *Store) GetSecretGrant(ctx context.Context, orgID, grantID ID) (SecretGrantRecord, error) {
	row, err := s.q.GetSecretGrant(ctx, dbsqlc.GetSecretGrantParams{OrgID: orgID, ID: grantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretGrantRecord{}, storeerr.ErrNotFound
		}
		return SecretGrantRecord{}, fmt.Errorf("get secret grant: %w", err)
	}
	return secretGrantFromGet(row), nil
}

func (s *Store) GetSecretGrantForSourceSecret(
	ctx context.Context,
	orgID, secretID, grantID ID,
) (SecretGrantRecord, error) {
	row, err := s.q.GetSecretGrantForSourceSecret(
		ctx,
		dbsqlc.GetSecretGrantForSourceSecretParams{OrgID: orgID, SecretID: secretID, ID: grantID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretGrantRecord{}, storeerr.ErrNotFound
		}
		return SecretGrantRecord{}, fmt.Errorf("get secret grant for source secret: %w", err)
	}
	return secretGrantFromGet(row), nil
}

func (s *Store) GetSecretGrantForTargetProject(
	ctx context.Context,
	orgID, projectID, grantID ID,
) (SecretGrantRecord, error) {
	row, err := s.q.GetSecretGrantForTargetProject(
		ctx,
		dbsqlc.GetSecretGrantForTargetProjectParams{
			OrgID:           orgID,
			TargetProjectID: projectID,
			ID:              grantID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretGrantRecord{}, storeerr.ErrNotFound
		}
		return SecretGrantRecord{}, fmt.Errorf("get secret grant for target project: %w", err)
	}
	return secretGrantFromGet(row), nil
}

func (s *Store) ListSecretGrants(ctx context.Context, input ListSecretGrantsInput) (ListSecretGrantsResult, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.Actor.ID) {
		return ListSecretGrantsResult{}, invalidSecretRequest("org, secret, and actor are required")
	}
	if input.Limit <= 0 {
		return ListSecretGrantsResult{}, invalidSecretRequest("limit must be positive")
	}
	secret, err := s.GetSecret(ctx, input.OrgID, input.SecretID)
	if err != nil {
		return ListSecretGrantsResult{}, err
	}
	if err := s.authorizeSecretManage(ctx, secret, input.Actor); err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) {
			return ListSecretGrantsResult{}, storeerr.ErrNotFound
		}
		return ListSecretGrantsResult{}, err
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListSecretGrantsBySecretParams{
		OrgID:     input.OrgID,
		SecretID:  input.SecretID,
		RowLimit:  int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc,
		NamePattern: input.List.NamePattern, TargetProjectID: sqlcIDFromNil(input.TargetProjectID),
	}
	if !listing.SortAllowed(input.List.SortField, "name", "created_at") {
		return ListSecretGrantsResult{}, invalidSecretRequest("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListSecretGrantsBySecret(ctx, params)
	if err != nil {
		return ListSecretGrantsResult{}, fmt.Errorf("list secret grants: %w", err)
	}
	result := ListSecretGrantsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Grants = make([]SecretGrantListRecord, 0, len(rows))
	for _, row := range rows {
		result.Grants = append(result.Grants, SecretGrantListRecord{
			Grant: SecretGrantRecord{
				ID: row.ID, OrgID: row.OrgID, SecretID: row.SecretID,
				TargetProjectID: row.TargetProjectID, CreatedAt: row.CreatedAt,
			},
			TargetProject: ProjectRecord{
				ID: row.TargetProjectID, OrgID: row.OrgID, Name: row.TargetProjectName,
				CreatedAt: row.TargetProjectCreatedAt, UpdatedAt: row.TargetProjectUpdatedAt,
			},
		})
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) DeleteSecretGrant(
	ctx context.Context,
	input DeleteSecretGrantInput,
) (SecretGrantRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.SecretID) || isNilID(input.GrantID) ||
		isNilID(input.Actor.ID) {
		return SecretGrantRecord{}, invalidSecretRequest("org, secret, grant, and actor are required")
	}
	grant, err := s.GetSecretGrantForSourceSecret(ctx, input.OrgID, input.SecretID, input.GrantID)
	if err != nil {
		return SecretGrantRecord{}, err
	}
	secret, err := s.GetSecret(ctx, input.OrgID, grant.SecretID)
	if err != nil {
		return SecretGrantRecord{}, err
	}
	if err := s.authorizeSecretGrantDelete(
		ctx,
		secret,
		grant.TargetProjectID,
		input.Actor,
	); err != nil {
		return SecretGrantRecord{}, err
	}
	row, err := s.q.DeleteSecretGrant(
		ctx,
		dbsqlc.DeleteSecretGrantParams{OrgID: input.OrgID, ID: input.GrantID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretGrantRecord{}, storeerr.ErrNotFound
		}
		return SecretGrantRecord{}, fmt.Errorf("delete secret grant: %w", err)
	}
	return secretGrantFromDelete(row), nil
}
