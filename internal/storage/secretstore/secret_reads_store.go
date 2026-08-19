package secretstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) GetSecret(ctx context.Context, orgID, secretID ID) (SecretRecord, error) {
	row, err := s.q.GetSecret(ctx, dbsqlc.GetSecretParams{OrgID: orgID, ID: secretID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, storeerr.ErrNotFound
		}
		return SecretRecord{}, fmt.Errorf("get secret: %w", err)
	}
	return secretFromGet(row), nil
}

func (s *Store) GetVisibleSecret(
	ctx context.Context,
	orgID, secretID ID,
	actor PrincipalRecord,
) (SecretRecord, error) {
	record, err := s.GetSecret(ctx, orgID, secretID)
	if err != nil {
		return SecretRecord{}, err
	}
	if err := s.authorizeSecretRead(ctx, record, actor); err != nil {
		if errors.Is(err, storeerr.ErrUnauthorized) {
			return SecretRecord{}, storeerr.ErrNotFound
		}
		return SecretRecord{}, err
	}
	return record, nil
}

func (s *Store) GetProjectOwnedSecretPayload(
	ctx context.Context,
	orgID, projectID, secretID ID,
) (secrets.Payload, error) {
	if s.secretKeyWrapper == nil {
		return nil, errors.New("secret key wrapper is required")
	}
	if isNilID(orgID) || isNilID(projectID) || isNilID(secretID) {
		return nil, invalidSecretRequest("org, project, and secret are required")
	}
	secret, err := s.GetSecret(ctx, orgID, secretID)
	if err != nil {
		return nil, err
	}
	if secret.OwnerKind != SecretOwnerProject || secret.OwnerProjectID != projectID {
		return nil, storeerr.ErrNotFound
	}
	row, err := s.q.GetSecretVersion(
		ctx,
		dbsqlc.GetSecretVersionParams{
			OrgID:    orgID,
			SecretID: secret.ID,
			ID:       secret.CurrentVersionID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storeerr.ErrNotFound
		}
		return nil, fmt.Errorf("get current secret version: %w", err)
	}
	version := SecretVersionRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		SecretID:          row.SecretID,
		VersionNumber:     row.VersionNumber,
		PayloadKeys:       append([]string(nil), row.PayloadKeys...),
		EncryptionScheme:  row.EncryptionScheme,
		KeyID:             row.KeyID,
		DEKWrappedBy:      row.DekWrappedBy,
		EncryptedDEK:      append([]byte(nil), row.EncryptedDek...),
		EncryptedDEKNonce: append([]byte(nil), row.EncryptedDekNonce...),
		Nonce:             append([]byte(nil), row.Nonce...),
		Ciphertext:        append([]byte(nil), row.Ciphertext...),
		CreatedAt:         row.CreatedAt,
	}
	payload, err := secrets.DecryptPayload(
		ctx,
		s.secretKeyWrapper,
		encryptedPayloadFromSecretVersion(version),
		secrets.AssociatedData{
			OrgID:         orgID.String(),
			SecretID:      secret.ID.String(),
			VersionID:     version.ID.String(),
			VersionNumber: version.VersionNumber,
			Kind:          secret.Kind,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret payload: %w", err)
	}
	return payload, nil
}

func (s *Store) GetSecretByOwnerName(
	ctx context.Context,
	orgID ID,
	ownerKind string,
	ownerProjectID, ownerUserID ID,
	name string,
) (SecretRecord, error) {
	if name == "" {
		return SecretRecord{}, invalidSecretRequest("secret name is required")
	}
	row, err := s.q.GetSecretByOwnerName(ctx, dbsqlc.GetSecretByOwnerNameParams{
		OrgID:          orgID,
		OwnerKind:      ownerKind,
		OwnerProjectID: sqlcIDFromNil(ownerProjectID),
		OwnerUserID:    sqlcIDFromNil(ownerUserID),
		Name:           name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretRecord{}, storeerr.ErrNotFound
		}
		return SecretRecord{}, fmt.Errorf("get secret by owner name: %w", err)
	}
	return secretFromGetByOwnerName(row), nil
}

func (s *Store) ListSecrets(ctx context.Context, input ListSecretsInput) (ListSecretsResult, error) {
	if isNilID(input.OrgID) || isNilID(input.Actor.ID) {
		return ListSecretsResult{}, invalidSecretRequest("org and actor are required")
	}
	if input.Limit <= 0 {
		return ListSecretsResult{}, invalidSecretRequest("limit must be positive")
	}
	if err := s.requireOrgMember(ctx, input.OrgID, input.Actor); err != nil {
		return ListSecretsResult{}, err
	}
	if input.Filters.OwnerKind != "" && input.Filters.OwnerKind != SecretOwnerOrg &&
		input.Filters.OwnerKind != SecretOwnerProject && input.Filters.OwnerKind != SecretOwnerUser {
		return ListSecretsResult{}, invalidSecretRequest("unsupported owner kind")
	}
	if input.Filters.OwnerKind == SecretOwnerProject {
		if isNilID(input.Filters.OwnerProjectID) {
			return ListSecretsResult{}, invalidSecretRequest("owner project is required for project owner filter")
		}
		if err := s.authorizeProjectSecretsList(ctx, input.OrgID, input.Filters.OwnerProjectID, input.Actor); err != nil {
			return ListSecretsResult{}, err
		}
	} else if !isNilID(input.Filters.OwnerProjectID) {
		return ListSecretsResult{}, invalidSecretRequest("owner project requires project owner filter")
	} else if input.Filters.OwnerKind == SecretOwnerOrg {
		if err := s.authorizeOrgSecretsList(ctx, input.OrgID, input.Actor); err != nil {
			return ListSecretsResult{}, err
		}
	}
	metadataFilter, err := secretMetadataFilterJSON(input.Filters.Metadata)
	if err != nil {
		return ListSecretsResult{}, invalidSecretRequest("%v", err)
	}
	actorUserID, actorOrgAPIKeyID := identitystore.AccountPrincipalIDs(input.Actor)
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListVisibleOwnedSecretsParams{
		OrgID: input.OrgID, UserID: actorUserID, OrgApiKeyID: actorOrgAPIKeyID,
		OwnerKind:      input.Filters.OwnerKind,
		OwnerProjectID: sqlcIDFromNil(input.Filters.OwnerProjectID),
		McpOauthFlowID: sqlcIDFromNil(input.Filters.MCPOAuthFlowID), MetadataFilter: metadataFilter,
		RowLimit:  int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc,
		NamePattern: input.List.NamePattern, Kinds: input.Filters.Kinds,
	}
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at", "owner_kind", "kind") {
		return ListSecretsResult{}, invalidSecretRequest("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListVisibleOwnedSecrets(ctx, params)
	if err != nil {
		return ListSecretsResult{}, fmt.Errorf("list visible owned secrets: %w", err)
	}
	result := ListSecretsResult{Secrets: make([]SecretRecord, 0, min(len(rows), input.Limit))}
	if len(rows) > input.Limit {
		result.HasMore, rows = true, rows[:input.Limit]
	}
	for _, row := range rows {
		result.Secrets = append(result.Secrets, secretFromListVisible(row))
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) ListProjectAvailableSecretsForPrincipal(
	ctx context.Context,
	input ListProjectAvailableSecretsForPrincipalInput,
) (ListProjectSecretAccessesResult, error) {
	if isNilID(input.Actor.ID) {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("actor is required")
	}
	if err := s.authorizeProjectSecretsList(
		ctx,
		input.OrgID,
		input.ProjectID,
		input.Actor,
	); err != nil {
		return ListProjectSecretAccessesResult{}, err
	}
	return s.ListProjectAvailableSecrets(ctx, input.ListProjectAvailableSecretsInput)
}

func (s *Store) ListProjectAvailableSecrets(
	ctx context.Context,
	input ListProjectAvailableSecretsInput,
) (ListProjectSecretAccessesResult, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("org and project are required")
	}
	if input.Limit <= 0 {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("limit must be positive")
	}
	if input.Filters.OwnerKind != "" && input.Filters.OwnerKind != SecretOwnerOrg &&
		input.Filters.OwnerKind != SecretOwnerProject && input.Filters.OwnerKind != SecretOwnerUser {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("unsupported owner kind")
	}
	if input.Filters.Availability != "" && input.Filters.Availability != SecretAvailabilityDirect &&
		input.Filters.Availability != SecretAvailabilityGrant {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("unsupported availability source")
	}
	metadataFilter, err := secretMetadataFilterJSON(input.Filters.Metadata)
	if err != nil {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("%v", err)
	}
	input.List = listing.Normalize(input.List)
	params := dbsqlc.ListProjectAvailableSecretsParams{
		OrgID:               input.OrgID,
		ProjectID:           input.ProjectID,
		MetadataFilter:      metadataFilter,
		OwnerKind:           input.Filters.OwnerKind,
		AvailabilitySources: input.Filters.AvailabilitySources,
		Kinds:               input.Filters.Kinds,
		RowLimit:            int64(input.Limit) + 1,
		SortField:           input.List.SortField, SortDesc: input.List.SortDesc,
		NamePattern: input.List.NamePattern,
	}
	if input.Filters.Availability != "" && len(input.Filters.AvailabilitySources) == 0 {
		params.AvailabilitySources = []string{input.Filters.Availability}
	}
	if !listing.SortAllowed(
		input.List.SortField,
		"name", "created_at", "updated_at", "owner_kind", "kind", "availability_source",
	) {
		return ListProjectSecretAccessesResult{}, invalidSecretRequest("unsupported sort")
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListProjectAvailableSecrets(ctx, params)
	if err != nil {
		return ListProjectSecretAccessesResult{}, fmt.Errorf("list project available secrets: %w", err)
	}
	result := ListProjectSecretAccessesResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Accesses = make([]ProjectSecretAccessRecord, 0, len(rows))
	for _, row := range rows {
		result.Accesses = append(result.Accesses, secretAccessFromListProject(row, input.ProjectID))
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) GetProjectAvailableSecret(
	ctx context.Context,
	orgID, projectID, secretID ID,
) (ProjectSecretAccessRecord, error) {
	row, err := s.q.GetProjectAvailableSecret(
		ctx,
		dbsqlc.GetProjectAvailableSecretParams{
			OrgID:     orgID,
			ProjectID: projectID,
			SecretID:  secretID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectSecretAccessRecord{}, storeerr.ErrNotFound
		}
		return ProjectSecretAccessRecord{}, fmt.Errorf("get project available secret: %w", err)
	}
	return secretAccessFromProjectAvailable(row, projectID), nil
}

func (s *Store) GetProjectAvailableSecretForPrincipal(
	ctx context.Context,
	orgID, projectID, secretID ID,
	actor PrincipalRecord,
) (ProjectSecretAccessRecord, error) {
	if err := s.authorizeProjectSecretsList(ctx, orgID, projectID, actor); err != nil {
		return ProjectSecretAccessRecord{}, err
	}
	return s.GetProjectAvailableSecret(ctx, orgID, projectID, secretID)
}

func getSecretTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, secretID ID,
) (SecretRecord, error) {
	row, err := qtx.GetSecret(ctx, dbsqlc.GetSecretParams{OrgID: orgID, ID: secretID})
	if err != nil {
		return SecretRecord{}, fmt.Errorf("get secret: %w", err)
	}
	return secretFromGet(row), nil
}
