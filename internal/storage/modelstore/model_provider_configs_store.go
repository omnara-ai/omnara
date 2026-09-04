package modelstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateModelProviderConfig(
	ctx context.Context,
	input CreateModelProviderConfigInput,
) (ModelProviderConfigRecord, error) {
	input.managementKind = management.Tenant
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("begin create model provider config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	record, err := s.createModelProviderConfigTx(ctx, qtx, input)
	if err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if record.Created {
		if err := resourceguard.Lock(
			ctx,
			qtx,
			resourceModelProviderConfigs,
			input.OrgID.String(),
		); err != nil {
			return ModelProviderConfigRecord{}, err
		}
		limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
		if err != nil {
			return ModelProviderConfigRecord{}, err
		}
		configCount, err := qtx.CountActiveTenantModelProviderConfigsForOrg(
			ctx,
			dbsqlc.CountActiveTenantModelProviderConfigsForOrgParams{OrgID: input.OrgID},
		)
		if err != nil {
			return ModelProviderConfigRecord{}, fmt.Errorf(
				"count tenant model provider configs: %w",
				err,
			)
		}
		if configCount > limits.MaxActiveTenantModelProviderConfigsPerOrg {
			return ModelProviderConfigRecord{}, resourceLimitExceeded(
				"active model provider configs",
				limits.MaxActiveTenantModelProviderConfigsPerOrg,
			)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("commit create model provider config: %w", err)
	}
	return record, nil
}

func (s *Store) createModelProviderConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateModelProviderConfigInput,
) (ModelProviderConfigRecord, error) {
	if isNilID(input.OrgID) || input.Name == "" || input.APIFormat == "" || input.BaseURL == "" ||
		isNilID(input.CredentialSecretID) {
		return ModelProviderConfigRecord{}, errors.New("org, name, API format, base url, and credential secret are required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("model provider config name", input.Name)
	if err != nil {
		return ModelProviderConfigRecord{}, storeerr.InvalidRequest(err)
	}
	input.Name = normalizedName
	if err := validateModelProviderAPIFormat(input.APIFormat); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	input.BaseURL, err = normalizeModelProviderBaseURL(input.BaseURL)
	if err != nil {
		return ModelProviderConfigRecord{}, err
	}
	input.EndpointPath = normalizeModelProviderEndpointPath(input.APIFormat, input.EndpointPath)
	if err := validateModelProviderEndpointPath(input.EndpointPath); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	input.AuthKind = normalizeModelProviderAuthKind(input.APIFormat, input.AuthKind)
	input.AuthOptions = normalizeModelProviderAuthOptions(input.APIFormat, input.AuthKind, input.AuthOptions)
	if err := ValidateModelProviderAuth(input.AuthKind, input.AuthOptions); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if input.APIVariant == "" {
		input.APIVariant = modelprotocol.APIVariantDefault
	}
	if err := validateModelProviderAPIVariant(input.APIFormat, input.APIVariant); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if err := validateModelProviderAuthAPIVariant(input.AuthKind, input.APIVariant); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if err := validateModelProviderSigV4EndpointRegion(input.BaseURL, input.AuthKind, input.AuthOptions); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	input.RequestTimeoutMS = normalizeModelProviderRequestTimeoutMS(input.RequestTimeoutMS)
	if err := validateModelProviderRequestTimeoutMS(input.RequestTimeoutMS); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if err := management.Validate(input.managementKind); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if err := validateModelProviderCredentialTx(
		ctx,
		qtx,
		input.OrgID,
		input.CredentialSecretID,
		input.managementKind,
		input.AuthKind,
	); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	row, err := qtx.InsertModelProviderConfig(
		ctx,
		dbsqlc.InsertModelProviderConfigParams{
			OrgID:              input.OrgID,
			ManagementKind:     string(input.managementKind),
			Name:               input.Name,
			ApiFormat:          string(input.APIFormat),
			ApiVariant:         string(input.APIVariant),
			BaseUrl:            input.BaseURL,
			EndpointPath:       input.EndpointPath,
			RequestTimeoutMs:   int32(input.RequestTimeoutMS),
			AuthKind:           input.AuthKind,
			AuthOptions:        input.AuthOptions,
			CredentialSecretID: &input.CredentialSecretID,
		},
	)
	if err == nil {
		record := modelProviderConfigRecordFromSQLC(row)
		record.Created = true
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return ModelProviderConfigRecord{}, modelProviderConfigNameConflict(input.Name)
		}
		return ModelProviderConfigRecord{}, fmt.Errorf("insert model provider config: %w", err)
	}
	existingRow, err := qtx.GetModelProviderConfigByName(
		ctx,
		dbsqlc.GetModelProviderConfigByNameParams{OrgID: input.OrgID, Name: input.Name},
	)
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("get model provider config by name: %w", err)
	}
	record := modelProviderConfigRecordFromSQLC(existingRow)
	if !sameModelProviderConfigIntent(record, input) {
		return ModelProviderConfigRecord{}, modelProviderConfigNameConflict(input.Name)
	}
	return record, nil
}

func modelProviderConfigNameConflict(name string) error {
	return fmt.Errorf(
		"a model provider config named %q already exists with a different configuration: %w",
		name,
		storeerr.ErrIdempotencyConflict,
	)
}

func (s *Store) GetModelProviderConfig(ctx context.Context, orgID, id ID) (ModelProviderConfigRecord, error) {
	row, err := s.q.GetModelProviderConfig(ctx, dbsqlc.GetModelProviderConfigParams{OrgID: orgID, ID: id})
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("get model provider config: %w", err)
	}
	return modelProviderConfigRecordFromSQLC(row), nil
}

func (s *Store) GetModelProviderConfigByName(
	ctx context.Context,
	orgID ID,
	name string,
) (ModelProviderConfigRecord, error) {
	normalizedName, err := resourcename.CanonicalizeRequired("model provider config name", name)
	if err != nil {
		return ModelProviderConfigRecord{}, storeerr.InvalidRequest(err)
	}
	row, err := s.q.GetModelProviderConfigByName(
		ctx,
		dbsqlc.GetModelProviderConfigByNameParams{OrgID: orgID, Name: normalizedName},
	)
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("get model provider config by name: %w", err)
	}
	return modelProviderConfigRecordFromSQLC(row), nil
}

type ListModelProviderConfigsInput struct {
	OrgID ID
	Limit int
	List  listing.Options
}

type ListModelProviderConfigsResult struct {
	Configs []ModelProviderConfigRecord
	HasMore bool
	Next    listing.Cursor
}

func (s *Store) ListModelProviderConfigs(
	ctx context.Context,
	input ListModelProviderConfigsInput,
) (ListModelProviderConfigsResult, error) {
	if isNilID(input.OrgID) {
		return ListModelProviderConfigsResult{}, errors.New("org id is required")
	}
	if input.Limit <= 0 {
		return ListModelProviderConfigsResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListModelProviderConfigsResult{}, errors.New("unsupported sort")
	}
	params := dbsqlc.ListModelProviderConfigsParams{
		OrgID:       input.OrgID,
		RowLimit:    int64(input.Limit) + 1,
		SortField:   input.List.SortField,
		SortDesc:    input.List.SortDesc,
		NamePattern: input.List.NamePattern,
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListModelProviderConfigs(ctx, params)
	if err != nil {
		return ListModelProviderConfigsResult{}, fmt.Errorf("list model provider configs: %w", err)
	}
	result := ListModelProviderConfigsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Configs = make([]ModelProviderConfigRecord, 0, len(rows))
	for _, row := range rows {
		result.Configs = append(result.Configs, modelProviderConfigRecordFromListSQLC(row))
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func validateModelProviderCredentialTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, credentialSecretID ID,
	managementKind management.Kind,
	authKind string,
) error {
	credential, err := secretops.GetFacts(ctx, qtx, orgID, credentialSecretID)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return err
	}
	return validateModelProviderCredentialRecord(credential, managementKind, authKind)
}

func validateModelProviderCredentialRecord(
	credential secretops.Facts,
	managementKind management.Kind,
	authKind string,
) error {
	if credential.OwnerKind != secretstore.SecretOwnerOrg {
		return fmt.Errorf("model provider credential secret must be org-owned: %w", storeerr.ErrNotFound)
	}
	expectedKind, err := ModelProviderCredentialSecretKind(authKind)
	if err != nil {
		return err
	}
	if credential.Kind != expectedKind {
		return fmt.Errorf(
			"model provider credential secret kind %q does not match required kind %q: %w",
			credential.Kind,
			expectedKind,
			storeerr.ErrInvalidSecretRequest,
		)
	}
	if credential.ManagementKind != managementKind {
		return fmt.Errorf(
			"model provider and credential secret must have the same management kind: %w",
			storeerr.ErrInvalidSecretRequest,
		)
	}
	return nil
}

func (s *Store) PatchModelProviderConfig(
	ctx context.Context,
	input PatchModelProviderConfigInput,
) (ModelProviderConfigRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ID) {
		return ModelProviderConfigRecord{}, errors.New("org and provider config are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelProviderConfigRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	currentRow, err := qtx.LockModelProviderConfigForMutation(
		ctx,
		dbsqlc.LockModelProviderConfigForMutationParams{OrgID: input.OrgID, ID: input.ID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelProviderConfigRecord{}, fmt.Errorf(
			"model provider config not found for mutation: %w",
			storeerr.ErrNotFound,
		)
	}
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("lock model provider config for mutation: %w", err)
	}
	current := modelProviderConfigRecordFromSQLC(currentRow)
	if err := management.RequireTenant(current.ManagementKind, "model providers"); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	update := updateModelProviderConfigInputFromCurrent(current)
	applyModelProviderConfigPatch(&update, current, input)
	record, err := updateModelProviderConfigTx(ctx, qtx, update, management.Tenant)
	if err != nil {
		return ModelProviderConfigRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	return record, nil
}

func updateModelProviderConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input modelProviderConfigUpdate,
	managementKind management.Kind,
) (ModelProviderConfigRecord, error) {
	update, err := normalizeModelProviderConfigUpdate(
		ctx,
		input,
		func(ctx context.Context, orgID, credentialSecretID ID, authKind string) error {
			return validateModelProviderCredentialTx(
				ctx,
				qtx,
				orgID,
				credentialSecretID,
				managementKind,
				authKind,
			)
		},
	)
	if err != nil {
		return ModelProviderConfigRecord{}, err
	}
	row, err := qtx.UpdateModelProviderConfig(
		ctx,
		dbsqlc.UpdateModelProviderConfigParams{
			ManagementKind:     string(managementKind),
			OrgID:              update.OrgID,
			ID:                 update.ID,
			BaseUrl:            update.BaseURL,
			EndpointPath:       update.EndpointPath,
			RequestTimeoutMs:   int32(update.RequestTimeoutMS),
			AuthKind:           update.AuthKind,
			AuthOptions:        update.AuthOptions,
			CredentialSecretID: &update.CredentialSecretID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelProviderConfigRecord{}, fmt.Errorf(
			"model provider config update target not found: %w",
			storeerr.ErrNotFound,
		)
	}
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("update model provider config: %w", err)
	}
	return modelProviderConfigRecordFromSQLC(row), nil
}

func normalizeModelProviderConfigUpdate(
	ctx context.Context,
	input modelProviderConfigUpdate,
	validateCredential func(context.Context, ID, ID, string) error,
) (modelProviderConfigUpdate, error) {
	if isNilID(input.OrgID) || isNilID(input.ID) || input.BaseURL == "" || input.EndpointPath == "" ||
		input.AuthKind == "" ||
		isNilID(input.CredentialSecretID) {
		return modelProviderConfigUpdate{}, errors.New(
			"org, provider config, base url, endpoint path, auth kind, and credential secret are required",
		)
	}
	var err error
	input.BaseURL, err = normalizeModelProviderBaseURL(input.BaseURL)
	if err != nil {
		return modelProviderConfigUpdate{}, err
	}
	input.EndpointPath = strings.TrimSpace(input.EndpointPath)
	if err := validateModelProviderEndpointPath(input.EndpointPath); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	input.AuthKind = strings.TrimSpace(input.AuthKind)
	input.AuthOptions = storeutil.NormalizeJSON(input.AuthOptions)
	if err := ValidateModelProviderAuth(input.AuthKind, input.AuthOptions); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	input.RequestTimeoutMS = normalizeModelProviderRequestTimeoutMS(input.RequestTimeoutMS)
	if err := validateModelProviderRequestTimeoutMS(input.RequestTimeoutMS); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	if err := validateModelProviderAPIVariant(input.APIFormat, input.APIVariant); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	if err := validateModelProviderAuthAPIVariant(input.AuthKind, input.APIVariant); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	if err := validateModelProviderSigV4EndpointRegion(input.BaseURL, input.AuthKind, input.AuthOptions); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	if err := validateCredential(ctx, input.OrgID, input.CredentialSecretID, input.AuthKind); err != nil {
		return modelProviderConfigUpdate{}, err
	}
	return input, nil
}

func updateModelProviderConfigInputFromCurrent(
	current ModelProviderConfigRecord,
) modelProviderConfigUpdate {
	return modelProviderConfigUpdate{
		OrgID:              current.OrgID,
		ID:                 current.ID,
		BaseURL:            current.BaseURL,
		EndpointPath:       current.EndpointPath,
		RequestTimeoutMS:   current.RequestTimeoutMS,
		AuthKind:           current.AuthKind,
		AuthOptions:        current.AuthOptions,
		CredentialSecretID: current.CredentialSecretID,
		APIFormat:          current.APIFormat,
		APIVariant:         current.APIVariant,
	}
}

func applyModelProviderConfigPatch(
	update *modelProviderConfigUpdate,
	current ModelProviderConfigRecord,
	patch PatchModelProviderConfigInput,
) {
	if patch.BaseURL != nil {
		update.BaseURL = *patch.BaseURL
	}
	if patch.EndpointPath != nil {
		update.EndpointPath = *patch.EndpointPath
	}
	if patch.RequestTimeoutMS != nil {
		update.RequestTimeoutMS = *patch.RequestTimeoutMS
	}
	if patch.AuthKind != nil {
		update.AuthKind = *patch.AuthKind
	}
	if patch.AuthOptions != nil {
		update.AuthOptions = *patch.AuthOptions
	} else if patch.AuthKind != nil && update.AuthKind != current.AuthKind {
		update.AuthOptions = DefaultModelProviderAuthOptions(current.APIFormat, update.AuthKind)
	}
	if patch.CredentialSecretID != nil {
		update.CredentialSecretID = *patch.CredentialSecretID
	}
}

func (s *Store) DeleteModelProviderConfig(
	ctx context.Context,
	orgID, id ID,
) (ModelProviderConfigRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("begin delete model provider config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	locked, err := qtx.LockModelProviderConfigForMutation(
		ctx,
		dbsqlc.LockModelProviderConfigForMutationParams{OrgID: orgID, ID: id},
	)
	if errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return ModelProviderConfigRecord{}, storeerr.ErrNotFound
	} else if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("lock model provider config for delete: %w", err)
	}
	if err := management.RequireTenant(
		management.Kind(locked.ManagementKind),
		"model providers",
	); err != nil {
		return ModelProviderConfigRecord{}, err
	}
	hasModels, err := qtx.ModelProviderConfigHasActiveModels(
		ctx,
		dbsqlc.ModelProviderConfigHasActiveModelsParams{OrgID: orgID, ID: id},
	)
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("check model provider config active models: %w", err)
	}
	if hasModels {
		return ModelProviderConfigRecord{}, fmt.Errorf("model provider config has active models: %w", storeerr.ErrConflict)
	}
	row, err := qtx.DeleteModelProviderConfig(
		ctx,
		dbsqlc.DeleteModelProviderConfigParams{OrgID: orgID, ID: id},
	)
	if err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("delete model provider config: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ModelProviderConfigRecord{}, fmt.Errorf("commit delete model provider config: %w", err)
	}
	return modelProviderConfigRecordFromSQLC(row), nil
}

func modelProviderConfigRecordFromSQLC(row dbsqlc.ModelProviderConfig) ModelProviderConfigRecord {
	return ModelProviderConfigRecord{
		ID:                 row.ID,
		OrgID:              row.OrgID,
		ManagementKind:     management.Kind(row.ManagementKind),
		Name:               row.Name,
		APIFormat:          modelprotocol.APIFormat(row.ApiFormat),
		APIVariant:         modelprotocol.APIVariant(row.ApiVariant),
		BaseURL:            row.BaseUrl,
		EndpointPath:       row.EndpointPath,
		RequestTimeoutMS:   int(row.RequestTimeoutMs),
		AuthKind:           row.AuthKind,
		AuthOptions:        storeutil.NormalizeJSON(row.AuthOptions),
		CredentialSecretID: idFromSQLCPtr(row.CredentialSecretID),
		DeletedAt:          row.DeletedAt,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func modelProviderConfigRecordFromListSQLC(row dbsqlc.ListModelProviderConfigsRow) ModelProviderConfigRecord {
	return ModelProviderConfigRecord{
		ID:                 row.ID,
		OrgID:              row.OrgID,
		ManagementKind:     management.Kind(row.ManagementKind),
		Name:               row.Name,
		APIFormat:          modelprotocol.APIFormat(row.ApiFormat),
		APIVariant:         modelprotocol.APIVariant(row.ApiVariant),
		BaseURL:            row.BaseUrl,
		EndpointPath:       row.EndpointPath,
		RequestTimeoutMS:   int(row.RequestTimeoutMs),
		AuthKind:           row.AuthKind,
		AuthOptions:        storeutil.NormalizeJSON(row.AuthOptions),
		CredentialSecretID: idFromSQLCPtr(row.CredentialSecretID),
		DeletedAt:          row.DeletedAt,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}
