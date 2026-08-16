package identitystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreatedOrgAPIKey struct {
	Record OrgAPIKeyRecord
	Token  string
}

func orgAPIKeyRoleAllowed(role string) bool {
	return role == authz.OrgRoleAdmin || role == authz.OrgRoleMember
}

// CreateOrgAPIKeyWithPlaintext returns the plaintext token once without storing it.
func (s *Store) CreateOrgAPIKeyWithPlaintext(
	ctx context.Context,
	input CreateOrgAPIKeyInput,
) (CreatedOrgAPIKey, error) {
	tokenID, token, err := prepareOrgAPIKeyInput(input)
	if err != nil {
		return CreatedOrgAPIKey{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreatedOrgAPIKey{}, fmt.Errorf("begin create org api key: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CreatedOrgAPIKey{}, storeerr.ErrNotFound
		}
		return CreatedOrgAPIKey{}, fmt.Errorf("lock org for api key: %w", err)
	}
	if err := ensureUserPrincipalStillActive(ctx, qtx, input.ActorPrincipal); err != nil {
		return CreatedOrgAPIKey{}, err
	}
	row, err := qtx.CreateOrgAPIKey(
		ctx,
		dbsqlc.CreateOrgAPIKeyParams{
			OrgID:           input.OrgID,
			Name:            input.Name,
			TokenID:         tokenID,
			TokenHash:       HashBearerToken(token),
			CreatedByUserID: input.CreatedByUserID,
		},
	)
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return CreatedOrgAPIKey{}, storeerr.ErrConflict
		}
		return CreatedOrgAPIKey{}, fmt.Errorf("create org api key: %w", err)
	}
	limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
	if err != nil {
		return CreatedOrgAPIKey{}, err
	}
	keyCount, err := qtx.CountActiveOrgAPIKeysForOrg(
		ctx,
		dbsqlc.CountActiveOrgAPIKeysForOrgParams{OrgID: input.OrgID},
	)
	if err != nil {
		return CreatedOrgAPIKey{}, fmt.Errorf("count active org api keys: %w", err)
	}
	if keyCount > limits.MaxActiveOrgApiKeysPerOrg {
		return CreatedOrgAPIKey{}, resourceLimitExceeded(
			"active org api keys",
			limits.MaxActiveOrgApiKeysPerOrg,
		)
	}
	membership, err := qtx.AddOrgAPIKeyOrgMembership(
		ctx,
		dbsqlc.AddOrgAPIKeyOrgMembershipParams{
			OrgID:       input.OrgID,
			OrgApiKeyID: row.ID,
			Role:        input.OrgRole,
		},
	)
	if err != nil {
		return CreatedOrgAPIKey{}, fmt.Errorf("create org api key membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedOrgAPIKey{}, fmt.Errorf("commit create org api key: %w", err)
	}
	record := orgAPIKeyRecordFromSQLC(row)
	record.OrgRole = membership.Role
	return CreatedOrgAPIKey{Record: record, Token: token}, nil
}

func prepareOrgAPIKeyInput(input CreateOrgAPIKeyInput) (string, string, error) {
	if isNilID(input.OrgID) {
		return "", "", errors.New("org api key org id is required")
	}
	if input.Name == "" {
		return "", "", errors.New("org api key name is required")
	}
	if err := resourcename.Validate("org api key name", input.Name); err != nil {
		return "", "", storeerr.InvalidRequest(err)
	}
	if !orgAPIKeyRoleAllowed(input.OrgRole) {
		return "", "", fmt.Errorf("org api key role must be %q or %q", authz.OrgRoleAdmin, authz.OrgRoleMember)
	}
	if isNilID(input.CreatedByUserID) {
		return "", "", errors.New("org api key creator user id is required")
	}
	if input.ActorPrincipal.Type != "" && input.ActorPrincipal.ID != input.CreatedByUserID {
		return "", "", storeerr.ErrUnauthorized
	}
	tokenID, err := randomTokenPart(10)
	if err != nil {
		return "", "", fmt.Errorf("generate org api key token id: %w", err)
	}
	token, err := bearertoken.Generate(bearertoken.KindOrganization)
	if err != nil {
		return "", "", err
	}
	return tokenID, token, nil
}

func (s *Store) AuthenticateOrgAPIKey(
	ctx context.Context,
	token string,
) (PrincipalRecord, error) {
	if err := bearertoken.Validate(token, bearertoken.KindOrganization); err != nil {
		return PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	row, err := s.q.AuthenticateOrgAPIKey(
		ctx,
		dbsqlc.AuthenticateOrgAPIKeyParams{
			TokenHash:            HashBearerToken(token),
			TouchIntervalSeconds: int64(bearerTokenTouchInterval / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("authenticate org api key: %w", err)
	}
	return NewOrgAPIKeyPrincipal(row.OrgID, row.OrgApiKeyID), nil
}

func (s *Store) GetOrgAPIKey(ctx context.Context, orgID, keyID ID) (OrgAPIKeyRecord, error) {
	if isNilID(orgID) || isNilID(keyID) {
		return OrgAPIKeyRecord{}, errors.New("org id and key id are required")
	}
	row, err := s.q.GetOrgAPIKey(ctx, dbsqlc.GetOrgAPIKeyParams{OrgID: orgID, ID: keyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgAPIKeyRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OrgAPIKeyRecord{}, fmt.Errorf("get org api key: %w", err)
	}
	return orgAPIKeyRecordFromGetRow(row), nil
}

type ListOrgAPIKeysInput struct {
	OrgID ID
	Limit int
	After listing.KeysetCursor
}

type ListOrgAPIKeysResult struct {
	Keys    []OrgAPIKeyRecord
	HasMore bool
}

// ListOrgAPIKeysForOrg returns one keyset page of an org's API keys, newest
// first, including revoked keys.
func (s *Store) ListOrgAPIKeysForOrg(
	ctx context.Context,
	input ListOrgAPIKeysInput,
) (ListOrgAPIKeysResult, error) {
	if isNilID(input.OrgID) {
		return ListOrgAPIKeysResult{}, errors.New("org id is required")
	}
	if input.Limit <= 0 {
		return ListOrgAPIKeysResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListOrgAPIKeysForOrgParams{
		OrgID:    input.OrgID,
		RowLimit: int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListOrgAPIKeysForOrg(ctx, params)
	if err != nil {
		return ListOrgAPIKeysResult{}, fmt.Errorf("list org api keys: %w", err)
	}
	result := ListOrgAPIKeysResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Keys = make([]OrgAPIKeyRecord, 0, len(rows))
	for _, row := range rows {
		record := orgAPIKeyRecordFromSQLC(dbsqlc.OrgApiKey{
			ID:              row.ID,
			OrgID:           row.OrgID,
			Name:            row.Name,
			TokenID:         row.TokenID,
			TokenHash:       row.TokenHash,
			CreatedByUserID: row.CreatedByUserID,
			CreatedAt:       row.CreatedAt,
			UpdatedAt:       row.UpdatedAt,
			LastUsedAt:      row.LastUsedAt,
			RevokedAt:       row.RevokedAt,
		})
		record.OrgRole = row.OrgRole
		result.Keys = append(result.Keys, record)
	}
	return result, nil
}

type UpdateOrgAPIKeyInput struct {
	OrgID          ID
	KeyID          ID
	ActorPrincipal PrincipalRecord
	Name           string
	OrgRole        string
}

// UpdateOrgAPIKey renames a key and/or changes its org role. Empty fields are
// left unchanged. Revoked keys cannot be updated.
func (s *Store) UpdateOrgAPIKey(
	ctx context.Context,
	input UpdateOrgAPIKeyInput,
) (OrgAPIKeyRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.KeyID) {
		return OrgAPIKeyRecord{}, errors.New("org id and key id are required")
	}
	if input.Name == "" && input.OrgRole == "" {
		return OrgAPIKeyRecord{}, errors.New("org api key update requires a name or role")
	}
	if input.OrgRole != "" && !orgAPIKeyRoleAllowed(input.OrgRole) {
		return OrgAPIKeyRecord{}, fmt.Errorf("org api key role must be %q or %q", authz.OrgRoleAdmin, authz.OrgRoleMember)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrgAPIKeyRecord{}, fmt.Errorf("begin update org api key: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	current, err := lockActiveOrgAPIKeyTx(ctx, qtx, input.OrgID, input.KeyID, input.ActorPrincipal)
	if err != nil {
		return OrgAPIKeyRecord{}, err
	}
	if input.Name != "" && input.Name != current.Name {
		if err := resourcename.Validate("org api key name", input.Name); err != nil {
			return OrgAPIKeyRecord{}, storeerr.InvalidRequest(err)
		}
	}
	var row dbsqlc.OrgApiKey
	if input.Name != "" {
		row, err = qtx.RenameOrgAPIKey(
			ctx,
			dbsqlc.RenameOrgAPIKeyParams{
				OrgID: input.OrgID,
				ID:    input.KeyID,
				Name:  input.Name,
			},
		)
	} else {
		row, err = qtx.TouchOrgAPIKeyUpdatedAt(
			ctx,
			dbsqlc.TouchOrgAPIKeyUpdatedAtParams{
				OrgID: input.OrgID,
				ID:    input.KeyID,
			},
		)
	}
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return OrgAPIKeyRecord{}, storeerr.ErrConflict
		}
		return OrgAPIKeyRecord{}, fmt.Errorf("update org api key: %w", err)
	}
	record := orgAPIKeyRecordFromSQLC(row)
	record.OrgRole = current.OrgRole
	if input.OrgRole != "" {
		membership, err := qtx.UpdateOrgMembershipRoleForOrgAPIKey(
			ctx,
			dbsqlc.UpdateOrgMembershipRoleForOrgAPIKeyParams{
				OrgID:       input.OrgID,
				OrgApiKeyID: input.KeyID,
				Role:        input.OrgRole,
			},
		)
		if err != nil {
			return OrgAPIKeyRecord{}, fmt.Errorf("update org api key role: %w", err)
		}
		record.OrgRole = membership.Role
	}
	if err := tx.Commit(ctx); err != nil {
		return OrgAPIKeyRecord{}, fmt.Errorf("commit update org api key: %w", err)
	}
	return record, nil
}

// RevokeOrgAPIKey terminally revokes a key and deletes its memberships. It is
// idempotent: an already-revoked key keeps its original revoked_at.
func (s *Store) RevokeOrgAPIKey(
	ctx context.Context,
	orgID, keyID ID,
	actor PrincipalRecord,
) (OrgAPIKeyRecord, error) {
	if isNilID(orgID) || isNilID(keyID) {
		return OrgAPIKeyRecord{}, errors.New("org id and key id are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrgAPIKeyRecord{}, fmt.Errorf("begin revoke org api key: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockOrgAPIKeyForUpdate(
		ctx,
		dbsqlc.LockOrgAPIKeyForUpdateParams{OrgID: orgID, ID: keyID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OrgAPIKeyRecord{}, storeerr.ErrNotFound
		}
		return OrgAPIKeyRecord{}, fmt.Errorf("lock org api key: %w", err)
	}
	if err := ensureUserPrincipalStillActive(ctx, qtx, actor); err != nil {
		return OrgAPIKeyRecord{}, err
	}
	row, err := qtx.RevokeOrgAPIKey(
		ctx,
		dbsqlc.RevokeOrgAPIKeyParams{OrgID: orgID, ID: keyID},
	)
	if err != nil {
		return OrgAPIKeyRecord{}, fmt.Errorf("revoke org api key: %w", err)
	}
	if _, err := qtx.RemoveOrgAPIKeyOrgMembership(
		ctx,
		dbsqlc.RemoveOrgAPIKeyOrgMembershipParams{OrgID: orgID, OrgApiKeyID: keyID},
	); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OrgAPIKeyRecord{}, fmt.Errorf("delete org api key org membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrgAPIKeyRecord{}, fmt.Errorf("commit revoke org api key: %w", err)
	}
	return orgAPIKeyRecordFromSQLC(row), nil
}

type OrgAPIKeyProjectRoleInput struct {
	OrgID          ID
	KeyID          ID
	ProjectID      ID
	ActorPrincipal PrincipalRecord
	Role           string
}

// SetOrgAPIKeyProjectRole grants or replaces a key's role on a project through
// the same project_memberships machinery used for users.
func (s *Store) SetOrgAPIKeyProjectRole(
	ctx context.Context,
	input OrgAPIKeyProjectRoleInput,
) (ProjectMembershipRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.KeyID) || isNilID(input.ProjectID) {
		return ProjectMembershipRecord{}, errors.New("org id, key id, and project id are required")
	}
	if !isValidProjectRole(input.Role) {
		return ProjectMembershipRecord{}, errors.New("role must be admin, developer, operator, or viewer")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf("begin set org api key project role: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := lockActiveOrgAPIKeyTx(ctx, qtx, input.OrgID, input.KeyID, input.ActorPrincipal); err != nil {
		return ProjectMembershipRecord{}, err
	}
	membership, err := qtx.GetOrgMembershipForOrgAPIKey(
		ctx,
		dbsqlc.GetOrgMembershipForOrgAPIKeyParams{OrgID: input.OrgID, OrgApiKeyID: input.KeyID},
	)
	if err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf("load org api key membership: %w", err)
	}
	row, err := qtx.AddProjectMembership(
		ctx,
		dbsqlc.AddProjectMembershipParams{
			OrgID:           input.OrgID,
			ProjectID:       input.ProjectID,
			OrgMembershipID: membership.ID,
			Role:            input.Role,
		},
	)
	if err != nil {
		if storeutil.IsForeignKeyViolation(err) {
			return ProjectMembershipRecord{}, storeerr.ErrNotFound
		}
		return ProjectMembershipRecord{}, fmt.Errorf("add org api key project membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf("commit set org api key project role: %w", err)
	}
	return projectMembershipRecordFromSQLC(row), nil
}

func (s *Store) ListProjectMembershipGrantsForOrgAPIKey(
	ctx context.Context,
	orgID, keyID ID,
) ([]ProjectMembershipGrantRecord, error) {
	if isNilID(orgID) {
		return nil, errors.New("org id is required")
	}
	if isNilID(keyID) {
		return nil, errors.New("key id is required")
	}
	rows, err := s.q.ListProjectMembershipsForOrgAPIKey(
		ctx,
		dbsqlc.ListProjectMembershipsForOrgAPIKeyParams{OrgID: orgID, OrgApiKeyID: keyID},
	)
	if err != nil {
		return nil, fmt.Errorf("list project memberships for org api key: %w", err)
	}
	records := make([]ProjectMembershipGrantRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, ProjectMembershipGrantRecord{
			ProjectID:   row.ProjectID,
			ProjectName: row.ProjectName,
			Role:        row.Role,
			CreatedAt:   row.CreatedAt,
		})
	}
	return records, nil
}

// RemoveOrgAPIKeyProjectRole removes a key's role on a project. Removing a
// role the key does not hold yields ErrNotFound.
func (s *Store) RemoveOrgAPIKeyProjectRole(
	ctx context.Context,
	input OrgAPIKeyProjectRoleInput,
) error {
	if isNilID(input.OrgID) || isNilID(input.KeyID) || isNilID(input.ProjectID) {
		return errors.New("org id, key id, and project id are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin remove org api key project role: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := lockActiveOrgAPIKeyTx(ctx, qtx, input.OrgID, input.KeyID, input.ActorPrincipal); err != nil {
		return err
	}
	membership, err := qtx.GetOrgMembershipForOrgAPIKey(
		ctx,
		dbsqlc.GetOrgMembershipForOrgAPIKeyParams{OrgID: input.OrgID, OrgApiKeyID: input.KeyID},
	)
	if err != nil {
		return fmt.Errorf("load org api key membership: %w", err)
	}
	if _, err := qtx.RemoveProjectMembership(
		ctx,
		dbsqlc.RemoveProjectMembershipParams{
			OrgID:           input.OrgID,
			ProjectID:       input.ProjectID,
			OrgMembershipID: membership.ID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("remove org api key project membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remove org api key project role: %w", err)
	}
	return nil
}

func lockActiveOrgAPIKeyTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, keyID ID,
	actor PrincipalRecord,
) (dbsqlc.GetOrgAPIKeyRow, error) {
	if _, err := qtx.LockOrgAPIKeyForUpdate(
		ctx,
		dbsqlc.LockOrgAPIKeyForUpdateParams{OrgID: orgID, ID: keyID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dbsqlc.GetOrgAPIKeyRow{}, storeerr.ErrNotFound
		}
		return dbsqlc.GetOrgAPIKeyRow{}, fmt.Errorf("lock org api key: %w", err)
	}
	if err := ensureUserPrincipalStillActive(ctx, qtx, actor); err != nil {
		return dbsqlc.GetOrgAPIKeyRow{}, err
	}
	current, err := qtx.GetOrgAPIKey(ctx, dbsqlc.GetOrgAPIKeyParams{OrgID: orgID, ID: keyID})
	if err != nil {
		return dbsqlc.GetOrgAPIKeyRow{}, fmt.Errorf("load org api key: %w", err)
	}
	if current.RevokedAt != nil {
		return dbsqlc.GetOrgAPIKeyRow{}, storeerr.ErrConflict
	}
	return current, nil
}

func orgAPIKeyRecordFromSQLC(row dbsqlc.OrgApiKey) OrgAPIKeyRecord {
	return OrgAPIKeyRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		Name:            row.Name,
		TokenID:         row.TokenID,
		TokenHash:       row.TokenHash,
		CreatedByUserID: row.CreatedByUserID,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		LastUsedAt:      row.LastUsedAt,
		RevokedAt:       row.RevokedAt,
	}
}

func orgAPIKeyRecordFromGetRow(row dbsqlc.GetOrgAPIKeyRow) OrgAPIKeyRecord {
	return OrgAPIKeyRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		Name:            row.Name,
		TokenID:         row.TokenID,
		TokenHash:       row.TokenHash,
		OrgRole:         row.OrgRole,
		CreatedByUserID: row.CreatedByUserID,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		LastUsedAt:      row.LastUsedAt,
		RevokedAt:       row.RevokedAt,
	}
}
