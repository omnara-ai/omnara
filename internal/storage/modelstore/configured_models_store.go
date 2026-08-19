package modelstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateConfiguredModel(
	ctx context.Context,
	input CreateConfiguredModelInput,
) (ConfiguredModelRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("begin create configured model: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	record, err := s.createConfiguredModelTx(ctx, qtx, input, management.Tenant)
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	if record.Created {
		if err := resourceguard.Lock(
			ctx,
			qtx,
			resourceConfiguredModels,
			input.OrgID.String()+":"+input.ModelProviderConfigID.String(),
		); err != nil {
			return ConfiguredModelRecord{}, err
		}
		limits, err := resourceguard.ResolveLimits(ctx, qtx, input.OrgID)
		if err != nil {
			return ConfiguredModelRecord{}, err
		}
		modelCount, err := qtx.CountActiveConfiguredModelsForProvider(
			ctx,
			dbsqlc.CountActiveConfiguredModelsForProviderParams{
				OrgID:                 input.OrgID,
				ModelProviderConfigID: input.ModelProviderConfigID,
			},
		)
		if err != nil {
			return ConfiguredModelRecord{}, fmt.Errorf("count active configured models: %w", err)
		}
		if modelCount > limits.MaxActiveConfiguredModelsPerProvider {
			return ConfiguredModelRecord{}, resourceLimitExceeded(
				"active configured models",
				limits.MaxActiveConfiguredModelsPerProvider,
			)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("commit create configured model: %w", err)
	}
	return record, nil
}

func (s *Store) createConfiguredModelTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateConfiguredModelInput,
	managementKind management.Kind,
) (ConfiguredModelRecord, error) {
	input = normalizeCreateConfiguredModelInput(input)
	if isNilID(input.OrgID) || isNilID(input.ModelProviderConfigID) || input.Name == "" || input.ProviderModelSlug == "" {
		return ConfiguredModelRecord{}, errors.New(
			"org, provider config, configured model name, and provider model slug are required",
		)
	}
	if input.MaxOutputTokens <= 0 {
		return ConfiguredModelRecord{}, fmt.Errorf(
			"max_output_tokens must be positive: %w",
			storeerr.ErrInvalidModelProviderConfig,
		)
	}
	if err := management.Validate(managementKind); err != nil {
		return ConfiguredModelRecord{}, err
	}
	providerConfigRow, err := qtx.LockModelProviderConfigForConfiguredModelCreate(
		ctx,
		dbsqlc.LockModelProviderConfigForConfiguredModelCreateParams{
			OrgID: input.OrgID,
			ID:    input.ModelProviderConfigID,
		},
	)
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	if managementKind == management.Cluster && management.Kind(providerConfigRow.ManagementKind) != management.Cluster {
		return ConfiguredModelRecord{}, fmt.Errorf(
			"cluster-managed configured models require a cluster-managed provider: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if _, err := ValidateAPIVariantOptions(input.APIVariantOptions); err != nil {
		return ConfiguredModelRecord{}, err
	}
	if err := validateConfiguredModelOptions(
		modelprotocol.APIFormat(providerConfigRow.ApiFormat),
		configuredModelOptionsFromCreate(input),
	); err != nil {
		return ConfiguredModelRecord{}, err
	}
	row, err := qtx.InsertConfiguredModel(
		ctx,
		dbsqlc.InsertConfiguredModelParams{
			OrgID:                     input.OrgID,
			ModelProviderConfigID:     input.ModelProviderConfigID,
			ManagementKind:            string(managementKind),
			Name:                      input.Name,
			ProviderModelSlug:         input.ProviderModelSlug,
			ContextWindowTokens:       int32(input.ContextWindowTokens),
			MaxOutputTokens:           int32(input.MaxOutputTokens),
			DefaultMaxOutputTokens:    storeutil.Int32Ptr(input.DefaultMaxOutputTokens),
			DefaultCacheRetention:     storeutil.TextFromEmpty(input.DefaultCacheRetention),
			SupportsTools:             boolPtrDefault(input.SupportsTools, true),
			SupportsReasoning:         input.SupportsReasoning,
			DefaultReasoningEffort:    input.DefaultReasoningEffort,
			SupportedReasoningEfforts: input.SupportedReasoningEfforts,
			InputModalities:           input.InputModalities,
			OutputModalities:          input.OutputModalities,
			ApiVariantOptions:         input.APIVariantOptions,
		},
	)
	if err == nil {
		record := configuredModelRecordFromInsertSQLC(row)
		record.Created = true
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return ConfiguredModelRecord{}, configuredModelNameConflict(input.Name)
		}
		return ConfiguredModelRecord{}, fmt.Errorf("insert configured model: %w", err)
	}
	existingRow, err := qtx.GetConfiguredModelByName(
		ctx,
		dbsqlc.GetConfiguredModelByNameParams{
			OrgID:                 input.OrgID,
			ModelProviderConfigID: input.ModelProviderConfigID,
			Name:                  input.Name,
		},
	)
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("get configured model by name: %w", err)
	}
	record := configuredModelRecordFromGetByNameSQLC(existingRow)
	if record.ManagementKind != managementKind || !sameConfiguredModelIntent(record, input) {
		return ConfiguredModelRecord{}, configuredModelNameConflict(input.Name)
	}
	return record, nil
}

func configuredModelNameConflict(name string) error {
	return fmt.Errorf(
		"a configured model named %q already exists under this provider config with a different configuration: %w",
		name,
		storeerr.ErrIdempotencyConflict,
	)
}

func (s *Store) PatchConfiguredModel(
	ctx context.Context,
	input PatchConfiguredModelInput,
) (ConfiguredModelRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ModelProviderConfigID) || isNilID(input.ID) {
		return ConfiguredModelRecord{}, errors.New("org, provider config, and configured model are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	current, err := lockConfiguredModelCurrentRevisionForMutationTx(ctx, qtx, input.OrgID, input.ID)
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	if current.ModelProviderConfigID != input.ModelProviderConfigID {
		return ConfiguredModelRecord{}, fmt.Errorf(
			"configured model not found under provider config: %w",
			storeerr.ErrNotFound,
		)
	}
	if err := management.RequireTenant(current.ManagementKind, "configured models"); err != nil {
		return ConfiguredModelRecord{}, err
	}
	update := updateConfiguredModelInputFromCurrent(current)
	applyConfiguredModelPatch(&update, input)
	update = normalizeConfiguredModelUpdate(update)
	behaviorChanged := configuredModelBehaviorChanged(current, update)
	if !behaviorChanged {
		record := current
		if input.Name != nil {
			if update.Name == "" {
				return ConfiguredModelRecord{}, errors.New("configured model name is required")
			}
			if update.Name != current.Name {
				renamed, err := renameConfiguredModelTx(ctx, qtx, update)
				if err != nil {
					return ConfiguredModelRecord{}, err
				}
				record = renamed
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return ConfiguredModelRecord{}, err
		}
		return record, nil
	}
	record, err := updateConfiguredModelTx(ctx, qtx, update, management.Tenant)
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfiguredModelRecord{}, err
	}
	return record, nil
}

func lockConfiguredModelCurrentRevisionForMutationTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, configuredModelID ID,
) (ConfiguredModelRecord, error) {
	configuredModel, err := qtx.LockConfiguredModelForMutation(
		ctx,
		dbsqlc.LockConfiguredModelForMutationParams{OrgID: orgID, ID: configuredModelID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model not found for mutation: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("lock configured model for mutation: %w", err)
	}
	revisionRow, err := qtx.GetConfiguredModelRevisionForUse(
		ctx,
		dbsqlc.GetConfiguredModelRevisionForUseParams{OrgID: orgID, ID: configuredModel.CurrentRevisionID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRecord{}, fmt.Errorf(
			"configured model current revision not found for mutation: %w",
			storeerr.ErrNotFound,
		)
	}
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("load configured model current revision for mutation: %w", err)
	}
	return configuredModelRecordFromLockedConfiguredModelAndRevisionSQLC(
		configuredModel,
		configuredModelRevisionRecordFromSQLC(revisionRow),
	), nil
}

func lockConfiguredModelCurrentRevisionForDeleteTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, configuredModelID ID,
) (ConfiguredModelRecord, error) {
	configuredModel, err := qtx.LockConfiguredModelForDelete(
		ctx,
		dbsqlc.LockConfiguredModelForDeleteParams{OrgID: orgID, ID: configuredModelID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model not found for delete: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("lock configured model for delete: %w", err)
	}
	revisionRow, err := qtx.GetConfiguredModelRevisionDisplay(
		ctx,
		dbsqlc.GetConfiguredModelRevisionDisplayParams{OrgID: orgID, ID: configuredModel.CurrentRevisionID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRecord{}, fmt.Errorf(
			"configured model current revision not found for delete: %w",
			storeerr.ErrNotFound,
		)
	}
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("load configured model current revision for delete: %w", err)
	}
	revision := configuredModelRevisionDisplayRecordFromSQLC(revisionRow).ConfiguredModelRevisionRecord
	return configuredModelRecordFromLockedConfiguredModelAndRevisionSQLC(configuredModel, revision), nil
}

func updateConfiguredModelTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input configuredModelUpdate,
	managementKind management.Kind,
) (ConfiguredModelRecord, error) {
	input = normalizeConfiguredModelUpdate(input)
	if input.Name == "" {
		return ConfiguredModelRecord{}, errors.New("configured model name is required")
	}
	if input.ProviderModelSlug == "" {
		return ConfiguredModelRecord{}, errors.New("provider model slug is required")
	}
	providerConfig, err := qtx.GetModelProviderConfig(
		ctx,
		dbsqlc.GetModelProviderConfigParams{OrgID: input.OrgID, ID: input.ModelProviderConfigID},
	)
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	if _, err := ValidateAPIVariantOptions(input.APIVariantOptions); err != nil {
		return ConfiguredModelRecord{}, err
	}
	if err := validateConfiguredModelOptions(
		modelprotocol.APIFormat(providerConfig.ApiFormat),
		configuredModelOptionsFromUpdate(input),
	); err != nil {
		return ConfiguredModelRecord{}, err
	}
	row, err := qtx.UpdateConfiguredModel(
		ctx,
		dbsqlc.UpdateConfiguredModelParams{
			ManagementKind:            string(managementKind),
			OrgID:                     input.OrgID,
			ModelProviderConfigID:     input.ModelProviderConfigID,
			ID:                        input.ID,
			Name:                      input.Name,
			ProviderModelSlug:         input.ProviderModelSlug,
			ContextWindowTokens:       int32(input.ContextWindowTokens),
			MaxOutputTokens:           int32(input.MaxOutputTokens),
			DefaultMaxOutputTokens:    storeutil.Int32Ptr(input.DefaultMaxOutputTokens),
			DefaultCacheRetention:     storeutil.TextFromEmpty(input.DefaultCacheRetention),
			SupportsTools:             boolPtrDefault(input.SupportsTools, true),
			SupportsReasoning:         input.SupportsReasoning,
			DefaultReasoningEffort:    input.DefaultReasoningEffort,
			SupportedReasoningEfforts: input.SupportedReasoningEfforts,
			InputModalities:           input.InputModalities,
			OutputModalities:          input.OutputModalities,
			ApiVariantOptions:         input.APIVariantOptions,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model update target not found: %w", storeerr.ErrNotFound)
	}
	if storeutil.IsUniqueViolation(err) {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model name already exists: %w", storeerr.ErrConflict)
	}
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("update configured model: %w", err)
	}
	return configuredModelRecordFromUpdateSQLC(row), nil
}

func renameConfiguredModelTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input configuredModelUpdate,
) (ConfiguredModelRecord, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return ConfiguredModelRecord{}, errors.New("configured model name is required")
	}
	row, err := qtx.RenameConfiguredModel(
		ctx,
		dbsqlc.RenameConfiguredModelParams{
			OrgID:                 input.OrgID,
			ModelProviderConfigID: input.ModelProviderConfigID,
			ID:                    input.ID,
			Name:                  input.Name,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model rename target not found: %w", storeerr.ErrNotFound)
	}
	if storeutil.IsUniqueViolation(err) {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model name already exists: %w", storeerr.ErrConflict)
	}
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("rename configured model: %w", err)
	}
	return configuredModelRecordFromRenameSQLC(row), nil
}

func updateConfiguredModelInputFromCurrent(current ConfiguredModelRecord) configuredModelUpdate {
	supportsTools := current.SupportsTools
	return configuredModelUpdate{
		OrgID:                     current.OrgID,
		ModelProviderConfigID:     current.ModelProviderConfigID,
		ID:                        current.ID,
		Name:                      current.Name,
		ProviderModelSlug:         current.ProviderModelSlug,
		ContextWindowTokens:       current.ContextWindowTokens,
		MaxOutputTokens:           current.MaxOutputTokens,
		DefaultMaxOutputTokens:    cloneIntPtr(current.DefaultMaxOutputTokens),
		DefaultCacheRetention:     current.DefaultCacheRetention,
		SupportsTools:             &supportsTools,
		SupportsReasoning:         current.SupportsReasoning,
		DefaultReasoningEffort:    current.DefaultReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), current.SupportedReasoningEfforts...),
		InputModalities:           append([]string(nil), current.InputModalities...),
		OutputModalities:          append([]string(nil), current.OutputModalities...),
		APIVariantOptions:         storeutil.NormalizeJSON(current.APIVariantOptions),
	}
}

func applyConfiguredModelPatch(update *configuredModelUpdate, patch PatchConfiguredModelInput) {
	if patch.Name != nil {
		update.Name = *patch.Name
	}
	if patch.ProviderModelSlug != nil {
		update.ProviderModelSlug = *patch.ProviderModelSlug
	}
	if patch.ContextWindowTokens != nil {
		update.ContextWindowTokens = *patch.ContextWindowTokens
	}
	if patch.MaxOutputTokens != nil {
		update.MaxOutputTokens = *patch.MaxOutputTokens
	}
	applyNullableIntPatch(&update.DefaultMaxOutputTokens, patch.DefaultMaxOutputTokens)
	if patch.DefaultCacheRetention != nil {
		update.DefaultCacheRetention = *patch.DefaultCacheRetention
	}
	if patch.SupportsTools != nil {
		supportsTools := *patch.SupportsTools
		update.SupportsTools = &supportsTools
	}
	if patch.SupportsReasoning != nil {
		update.SupportsReasoning = *patch.SupportsReasoning
	}
	if patch.DefaultReasoningEffort != nil {
		update.DefaultReasoningEffort = *patch.DefaultReasoningEffort
	}
	if patch.SupportedReasoningEfforts != nil {
		update.SupportedReasoningEfforts = append([]string(nil), (*patch.SupportedReasoningEfforts)...)
	}
	if patch.InputModalities != nil {
		update.InputModalities = append([]string(nil), (*patch.InputModalities)...)
	}
	if patch.OutputModalities != nil {
		update.OutputModalities = append([]string(nil), (*patch.OutputModalities)...)
	}
	if patch.APIVariantOptions != nil {
		update.APIVariantOptions = *patch.APIVariantOptions
	}
}

func configuredModelBehaviorChanged(current ConfiguredModelRecord, update configuredModelUpdate) bool {
	return current.ProviderModelSlug != update.ProviderModelSlug ||
		current.ContextWindowTokens != update.ContextWindowTokens ||
		current.MaxOutputTokens != update.MaxOutputTokens ||
		!storeutil.SameIntPtr(current.DefaultMaxOutputTokens, update.DefaultMaxOutputTokens) ||
		current.DefaultCacheRetention != update.DefaultCacheRetention ||
		current.SupportsTools != boolPtrDefault(update.SupportsTools, true) ||
		current.SupportsReasoning != update.SupportsReasoning ||
		current.DefaultReasoningEffort != update.DefaultReasoningEffort ||
		!slices.Equal(current.SupportedReasoningEfforts, update.SupportedReasoningEfforts) ||
		!slices.Equal(current.InputModalities, update.InputModalities) ||
		!slices.Equal(current.OutputModalities, update.OutputModalities) ||
		!storeutil.SameJSON(
			storeutil.NormalizeJSON(current.APIVariantOptions),
			storeutil.NormalizeJSON(update.APIVariantOptions),
		)
}

func applyNullableIntPatch(target **int, value patch.NullableInt) {
	if value.Set {
		*target = cloneIntPtr(value.Value)
	}
}

func (s *Store) GetConfiguredModel(ctx context.Context, orgID, id ID) (ConfiguredModelRecord, error) {
	row, err := s.q.GetConfiguredModel(ctx, dbsqlc.GetConfiguredModelParams{OrgID: orgID, ID: id})
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("get configured model: %w", err)
	}
	return configuredModelRecordFromGetSQLC(row), nil
}

func (s *Store) GetConfiguredModelDisplay(ctx context.Context, orgID, id ID) (ConfiguredModelRecord, error) {
	row, err := s.q.GetConfiguredModelDisplay(ctx, dbsqlc.GetConfiguredModelDisplayParams{OrgID: orgID, ID: id})
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("get configured model display: %w", err)
	}
	return configuredModelRecordFromDisplaySQLC(row), nil
}

func (s *Store) GetConfiguredModelByName(
	ctx context.Context,
	orgID, providerConfigID ID,
	name string,
) (ConfiguredModelRecord, error) {
	row, err := s.q.GetConfiguredModelByName(
		ctx,
		dbsqlc.GetConfiguredModelByNameParams{OrgID: orgID, ModelProviderConfigID: providerConfigID, Name: name},
	)
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("get configured model by name: %w", err)
	}
	return configuredModelRecordFromGetByNameSQLC(row), nil
}

type ListConfiguredModelsInput struct {
	OrgID            ID
	ProviderConfigID ID
	Limit            int
	After            listing.KeysetCursor
}

type ListConfiguredModelsResult struct {
	Models  []ConfiguredModelRecord
	HasMore bool
}

func (s *Store) ListConfiguredModels(
	ctx context.Context,
	input ListConfiguredModelsInput,
) (ListConfiguredModelsResult, error) {
	if isNilID(input.OrgID) || isNilID(input.ProviderConfigID) {
		return ListConfiguredModelsResult{}, errors.New("org and provider config are required")
	}
	if input.Limit <= 0 {
		return ListConfiguredModelsResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListConfiguredModelsParams{
		OrgID:                 input.OrgID,
		ModelProviderConfigID: input.ProviderConfigID,
		RowLimit:              int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListConfiguredModels(ctx, params)
	if err != nil {
		return ListConfiguredModelsResult{}, fmt.Errorf("list configured models: %w", err)
	}
	result := ListConfiguredModelsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Models = make([]ConfiguredModelRecord, 0, len(rows))
	for _, row := range rows {
		result.Models = append(result.Models, configuredModelRecordFromListSQLC(row))
	}
	return result, nil
}

func (s *Store) GetConfiguredModelRevisionForUse(
	ctx context.Context,
	orgID, revisionID ID,
) (ConfiguredModelRevisionRecord, error) {
	row, err := s.q.GetConfiguredModelRevisionForUse(
		ctx,
		dbsqlc.GetConfiguredModelRevisionForUseParams{OrgID: orgID, ID: revisionID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfiguredModelRevisionRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ConfiguredModelRevisionRecord{}, fmt.Errorf("get configured model revision: %w", err)
	}
	return configuredModelRevisionRecordFromSQLC(row), nil
}

func (s *Store) GetConfiguredModelRevisionDisplay(
	ctx context.Context,
	orgID, revisionID ID,
) (ConfiguredModelRevisionDisplayRecord, error) {
	row, err := s.q.GetConfiguredModelRevisionDisplay(
		ctx,
		dbsqlc.GetConfiguredModelRevisionDisplayParams{OrgID: orgID, ID: revisionID},
	)
	if err != nil {
		return ConfiguredModelRevisionDisplayRecord{}, fmt.Errorf("get configured model revision display: %w", err)
	}
	return configuredModelRevisionDisplayRecordFromSQLC(row), nil
}

func (s *Store) DeleteConfiguredModel(
	ctx context.Context,
	orgID, id ID,
) (ConfiguredModelRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("begin delete configured model: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	lockedModel, err := lockConfiguredModelCurrentRevisionForDeleteTx(ctx, qtx, orgID, id)
	if err != nil {
		return ConfiguredModelRecord{}, err
	}
	if err := management.RequireTenant(lockedModel.ManagementKind, "configured models"); err != nil {
		return ConfiguredModelRecord{}, err
	}
	hasGrants, err := qtx.ConfiguredModelHasActiveGrants(
		ctx,
		dbsqlc.ConfiguredModelHasActiveGrantsParams{OrgID: orgID, ID: id},
	)
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("check configured model active grants: %w", err)
	}
	if hasGrants {
		return ConfiguredModelRecord{}, fmt.Errorf("configured model has active project grants: %w", storeerr.ErrConflict)
	}
	deleted, err := qtx.DeleteConfiguredModel(ctx, dbsqlc.DeleteConfiguredModelParams{
		OrgID: orgID, ID: id, ManagementKind: string(management.Tenant),
	})
	if err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("delete configured model: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfiguredModelRecord{}, fmt.Errorf("commit delete configured model: %w", err)
	}
	record := lockedModel
	record.DeletedAt = deleted.DeletedAt
	record.UpdatedAt = deleted.UpdatedAt
	return record, nil
}

func configuredModelRecordFromInsertSQLC(row dbsqlc.InsertConfiguredModelRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromGetSQLC(row dbsqlc.GetConfiguredModelRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromLockForUseSQLC(row dbsqlc.LockConfiguredModelForUseRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromDisplaySQLC(row dbsqlc.GetConfiguredModelDisplayRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromGetByNameSQLC(row dbsqlc.GetConfiguredModelByNameRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromListSQLC(row dbsqlc.ListConfiguredModelsRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromUpdateSQLC(row dbsqlc.UpdateConfiguredModelRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromRenameSQLC(row dbsqlc.RenameConfiguredModelRow) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ManagementKind:            management.Kind(row.ManagementKind),
		Name:                      row.Name,
		CurrentRevisionID:         row.CurrentRevisionID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		DeletedAt:                 row.DeletedAt,
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		RevisionCreatedAt:         row.RevisionCreatedAt,
	}
}

func configuredModelRecordFromLockedConfiguredModelAndRevisionSQLC(
	configuredModel dbsqlc.ConfiguredModel,
	revision ConfiguredModelRevisionRecord,
) ConfiguredModelRecord {
	return ConfiguredModelRecord{
		ID:                        configuredModel.ID,
		OrgID:                     configuredModel.OrgID,
		ModelProviderConfigID:     configuredModel.ModelProviderConfigID,
		ManagementKind:            management.Kind(configuredModel.ManagementKind),
		Name:                      configuredModel.Name,
		CurrentRevisionID:         configuredModel.CurrentRevisionID,
		ProviderModelSlug:         revision.ProviderModelSlug,
		ContextWindowTokens:       revision.ContextWindowTokens,
		MaxOutputTokens:           revision.MaxOutputTokens,
		DefaultMaxOutputTokens:    cloneIntPtr(revision.DefaultMaxOutputTokens),
		DefaultCacheRetention:     revision.DefaultCacheRetention,
		SupportsTools:             revision.SupportsTools,
		SupportsReasoning:         revision.SupportsReasoning,
		DefaultReasoningEffort:    revision.DefaultReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), revision.SupportedReasoningEfforts...),
		InputModalities:           append([]string(nil), revision.InputModalities...),
		OutputModalities:          append([]string(nil), revision.OutputModalities...),
		APIVariantOptions:         storeutil.NormalizeJSON(revision.APIVariantOptions),
		DeletedAt:                 configuredModel.DeletedAt,
		CreatedAt:                 configuredModel.CreatedAt,
		UpdatedAt:                 configuredModel.UpdatedAt,
		RevisionCreatedAt:         revision.CreatedAt,
	}
}

func configuredModelRevisionRecordFromSQLC(row dbsqlc.ConfiguredModelRevision) ConfiguredModelRevisionRecord {
	return ConfiguredModelRevisionRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ConfiguredModelID:         row.ConfiguredModelID,
		ModelProviderConfigID:     row.ModelProviderConfigID,
		ProviderModelSlug:         row.ProviderModelSlug,
		ContextWindowTokens:       int(row.ContextWindowTokens),
		MaxOutputTokens:           int(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             row.SupportsTools,
		SupportsReasoning:         row.SupportsReasoning,
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
		CreatedAt:                 row.CreatedAt,
	}
}

func configuredModelRevisionDisplayRecordFromSQLC(
	row dbsqlc.GetConfiguredModelRevisionDisplayRow,
) ConfiguredModelRevisionDisplayRecord {
	return ConfiguredModelRevisionDisplayRecord{
		ConfiguredModelRevisionRecord: ConfiguredModelRevisionRecord{
			ID:                        row.ID,
			OrgID:                     row.OrgID,
			ConfiguredModelID:         row.ConfiguredModelID,
			ModelProviderConfigID:     row.ModelProviderConfigID,
			ProviderModelSlug:         row.ProviderModelSlug,
			ContextWindowTokens:       int(row.ContextWindowTokens),
			MaxOutputTokens:           int(row.MaxOutputTokens),
			DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
			DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
			SupportsTools:             row.SupportsTools,
			SupportsReasoning:         row.SupportsReasoning,
			DefaultReasoningEffort:    row.DefaultReasoningEffort,
			SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
			InputModalities:           nonNilStringSlice(row.InputModalities),
			OutputModalities:          nonNilStringSlice(row.OutputModalities),
			APIVariantOptions:         storeutil.NormalizeJSON(row.ApiVariantOptions),
			CreatedAt:                 row.CreatedAt,
		},
		ConfiguredModelName: row.ConfiguredModelName,
		ProviderConfigName:  row.ProviderConfigName,
		APIFormat:           modelprotocol.APIFormat(row.ApiFormat),
		APIVariant:          modelprotocol.APIVariant(row.ApiVariant),
	}
}
