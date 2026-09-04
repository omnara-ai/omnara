package modelstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateProjectModelGrant(
	ctx context.Context,
	input CreateProjectModelGrantInput,
) (ProjectModelGrantRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.ConfiguredModelID) {
		return ProjectModelGrantRecord{}, errors.New("org, project, and configured model are required")
	}
	input = normalizeProjectModelGrantInput(input)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("begin create project model grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return ProjectModelGrantRecord{}, err
	}

	configuredModelRow, err := qtx.LockConfiguredModelForUse(
		ctx,
		dbsqlc.LockConfiguredModelForUseParams{OrgID: input.OrgID, ID: input.ConfiguredModelID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	} else if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("lock configured model for grant: %w", err)
	}
	configuredModel := configuredModelRecordFromLockForUseSQLC(configuredModelRow)
	providerConfig, err := qtx.GetModelProviderConfig(
		ctx,
		dbsqlc.GetModelProviderConfigParams{OrgID: input.OrgID, ID: configuredModel.ModelProviderConfigID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("load configured model provider config for grant: %w", err)
	}
	if err := validateProjectModelGrantForConfiguredModel(
		modelprotocol.APIFormat(providerConfig.ApiFormat),
		configuredModel,
		input,
	); err != nil {
		return ProjectModelGrantRecord{}, err
	}
	row, err := qtx.UpsertProjectModelGrant(ctx, dbsqlc.UpsertProjectModelGrantParams{
		OrgID:                     input.OrgID,
		ProjectID:                 input.ProjectID,
		ConfiguredModelID:         input.ConfiguredModelID,
		ContextWindowTokens:       storeutil.Int32Ptr(input.ContextWindowTokens),
		MaxOutputTokens:           storeutil.Int32Ptr(input.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.Int32Ptr(input.DefaultMaxOutputTokens),
		DefaultCacheRetention:     storeutil.TextFromEmpty(input.DefaultCacheRetention),
		SupportsTools:             input.SupportsTools,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
		InputModalities:           input.InputModalities,
		OutputModalities:          input.OutputModalities,
	})
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("upsert project model grant: %w", err)
	}
	record := projectModelGrantRecordFromUpsertSQLC(row)
	if !sameProjectModelGrantIntent(record, input) {
		return ProjectModelGrantRecord{}, storeerr.Tag(storeerr.ErrConflict, errors.New(
			"an active project grant for this configured model already exists with a different configuration",
		))
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("commit create project model grant: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateProjectModelGrant(
	ctx context.Context,
	input UpdateProjectModelGrantInput,
) (ProjectModelGrantRecord, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) || isNilID(input.ID) {
		return ProjectModelGrantRecord{}, storeerr.InvalidRequest(errors.New("org, project, and grant are required"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("begin update project model grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return ProjectModelGrantRecord{}, err
	}
	ref, err := qtx.GetProjectModelGrant(
		ctx,
		dbsqlc.GetProjectModelGrantParams{OrgID: input.OrgID, ProjectID: input.ProjectID, ID: input.ID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("get project model grant for update: %w", err)
	}
	configuredModelRow, err := qtx.LockConfiguredModelForUse(
		ctx,
		dbsqlc.LockConfiguredModelForUseParams{OrgID: input.OrgID, ID: ref.ConfiguredModelID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("lock configured model for grant update: %w", err)
	}
	// Re-read after the configured-model lock; grant mutations serialize on the model row.
	row, err := qtx.GetProjectModelGrant(
		ctx,
		dbsqlc.GetProjectModelGrantParams{OrgID: input.OrgID, ProjectID: input.ProjectID, ID: input.ID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("get project model grant for update: %w", err)
	}
	configuredModel := configuredModelRecordFromLockForUseSQLC(configuredModelRow)
	providerConfig, err := qtx.GetModelProviderConfig(
		ctx,
		dbsqlc.GetModelProviderConfigParams{OrgID: input.OrgID, ID: configuredModel.ModelProviderConfigID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("load configured model provider config for grant update: %w", err)
	}
	merged := applyProjectModelGrantPatch(projectModelGrantRecordFromSQLC(row), input)
	if _, err := EffectiveConfiguredModelForProjectGrant(
		modelprotocol.APIFormat(providerConfig.ApiFormat),
		configuredModel,
		merged,
	); err != nil {
		return ProjectModelGrantRecord{}, err
	}
	updatedRow, err := qtx.UpdateProjectModelGrant(ctx, dbsqlc.UpdateProjectModelGrantParams{
		ContextWindowTokens:       storeutil.Int32Ptr(merged.ContextWindowTokens),
		MaxOutputTokens:           storeutil.Int32Ptr(merged.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.Int32Ptr(merged.DefaultMaxOutputTokens),
		DefaultCacheRetention:     storeutil.TextFromEmpty(merged.DefaultCacheRetention),
		SupportsTools:             merged.SupportsTools,
		SupportsReasoning:         merged.SupportsReasoning,
		DefaultReasoningEffort:    merged.DefaultReasoningEffort,
		SupportedReasoningEfforts: merged.SupportedReasoningEfforts,
		InputModalities:           merged.InputModalities,
		OutputModalities:          merged.OutputModalities,
		OrgID:                     input.OrgID,
		ProjectID:                 input.ProjectID,
		ID:                        input.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectModelGrantRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("update project model grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("commit update project model grant: %w", err)
	}
	return projectModelGrantRecordFromSQLC(updatedRow), nil
}

func applyProjectModelGrantPatch(
	record ProjectModelGrantRecord,
	input UpdateProjectModelGrantInput,
) ProjectModelGrantRecord {
	if input.ContextWindowTokens.Set {
		record.ContextWindowTokens = cloneIntPtr(input.ContextWindowTokens.Value)
	}
	if input.MaxOutputTokens.Set {
		record.MaxOutputTokens = cloneIntPtr(input.MaxOutputTokens.Value)
	}
	if input.DefaultMaxOutputTokens.Set {
		record.DefaultMaxOutputTokens = cloneIntPtr(input.DefaultMaxOutputTokens.Value)
	}
	if input.DefaultCacheRetention != nil {
		record.DefaultCacheRetention = *input.DefaultCacheRetention
	}
	if input.SupportsTools.Set {
		record.SupportsTools = cloneBoolPtr(input.SupportsTools.Value)
	}
	if input.SupportsReasoning.Set {
		record.SupportsReasoning = cloneBoolPtr(input.SupportsReasoning.Value)
	}
	if input.DefaultReasoningEffort != nil {
		record.DefaultReasoningEffort = *input.DefaultReasoningEffort
	}
	if input.SupportedReasoningEfforts != nil {
		record.SupportedReasoningEfforts = append([]string(nil), (*input.SupportedReasoningEfforts)...)
	}
	if input.InputModalities != nil {
		record.InputModalities = append([]string(nil), (*input.InputModalities)...)
	}
	if input.OutputModalities != nil {
		record.OutputModalities = append([]string(nil), (*input.OutputModalities)...)
	}
	record.DefaultCacheRetention,
		record.DefaultReasoningEffort,
		record.SupportedReasoningEfforts,
		record.InputModalities,
		record.OutputModalities = normalizeConfiguredModelOptionFields(
		record.DefaultCacheRetention,
		record.DefaultReasoningEffort,
		record.SupportedReasoningEfforts,
		record.InputModalities,
		record.OutputModalities,
	)
	return record
}

func (s *Store) GetActiveProjectModelGrantForConfiguredModel(
	ctx context.Context,
	orgID, projectID, configuredModelID ID,
) (ProjectModelGrantRecord, error) {
	row, err := s.q.GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		dbsqlc.GetActiveProjectModelGrantForConfiguredModelParams{
			OrgID:             orgID,
			ProjectID:         projectID,
			ConfiguredModelID: configuredModelID,
		},
	)
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("get active project model grant for configured model: %w", err)
	}
	return projectModelGrantRecordFromActiveSQLC(row), nil
}

func (s *Store) ListProjectModelGrants(
	ctx context.Context,
	input ListProjectModelGrantsInput,
) (ListProjectModelGrantsResult, error) {
	if isNilID(input.OrgID) || isNilID(input.ProjectID) {
		return ListProjectModelGrantsResult{}, errors.New("org and project are required")
	}
	if input.Limit <= 0 {
		return ListProjectModelGrantsResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at", "updated_at") {
		return ListProjectModelGrantsResult{}, errors.New("unsupported sort")
	}
	params := dbsqlc.ListProjectModelGrantsParams{
		OrgID:     input.OrgID,
		ProjectID: input.ProjectID,
		RowLimit:  int64(input.Limit) + 1,
		SortField: input.List.SortField, SortDesc: input.List.SortDesc, NamePattern: input.List.NamePattern,
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListProjectModelGrants(ctx, params)
	if err != nil {
		return ListProjectModelGrantsResult{}, fmt.Errorf("list project model grants: %w", err)
	}
	result := ListProjectModelGrantsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Grants = make([]ProjectModelGrantListRecord, 0, len(rows))
	for _, row := range rows {
		result.Grants = append(result.Grants, ProjectModelGrantListRecord{
			Grant: ProjectModelGrantRecord{
				ID:                        row.ID,
				OrgID:                     row.OrgID,
				ProjectID:                 row.ProjectID,
				ConfiguredModelID:         row.ConfiguredModelID,
				ContextWindowTokens:       storeutil.IntPtr(row.ContextWindowTokens),
				MaxOutputTokens:           storeutil.IntPtr(row.MaxOutputTokens),
				DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
				DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
				SupportsTools:             cloneBoolPtr(row.SupportsTools),
				SupportsReasoning:         cloneBoolPtr(row.SupportsReasoning),
				DefaultReasoningEffort:    row.DefaultReasoningEffort,
				SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
				InputModalities:           nonNilStringSlice(row.InputModalities),
				OutputModalities:          nonNilStringSlice(row.OutputModalities),
				CreatedAt:                 row.CreatedAt,
				UpdatedAt:                 row.UpdatedAt,
			},
			Model: ConfiguredModelSummaryRecord{
				ID:                    row.ConfiguredModelID,
				OrgID:                 row.OrgID,
				ModelProviderConfigID: row.ModelProviderConfigID,
				Name:                  row.ModelName,
				ProviderConfigName:    row.ProviderConfigName,
				CreatedAt:             row.ModelCreatedAt,
				UpdatedAt:             row.ModelUpdatedAt,
			},
		})
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.ID}
	}
	return result, nil
}

func (s *Store) DeleteProjectModelGrant(
	ctx context.Context,
	orgID, projectID, id ID,
) (ProjectModelGrantRecord, error) {
	row, err := s.q.DeleteProjectModelGrant(
		ctx,
		dbsqlc.DeleteProjectModelGrantParams{OrgID: orgID, ProjectID: projectID, ID: id},
	)
	if err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("delete project model grant: %w", err)
	}
	return projectModelGrantRecordFromSQLC(row), nil
}

func projectModelGrantRecordFromSQLC(row dbsqlc.ProjectModelGrant) ProjectModelGrantRecord {
	return ProjectModelGrantRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ProjectID:                 row.ProjectID,
		ConfiguredModelID:         row.ConfiguredModelID,
		ContextWindowTokens:       storeutil.IntPtr(row.ContextWindowTokens),
		MaxOutputTokens:           storeutil.IntPtr(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             cloneBoolPtr(row.SupportsTools),
		SupportsReasoning:         cloneBoolPtr(row.SupportsReasoning),
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
	}
}

func projectModelGrantRecordFromActiveSQLC(
	row dbsqlc.ProjectModelGrant,
) ProjectModelGrantRecord {
	return ProjectModelGrantRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ProjectID:                 row.ProjectID,
		ConfiguredModelID:         row.ConfiguredModelID,
		ContextWindowTokens:       storeutil.IntPtr(row.ContextWindowTokens),
		MaxOutputTokens:           storeutil.IntPtr(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             cloneBoolPtr(row.SupportsTools),
		SupportsReasoning:         cloneBoolPtr(row.SupportsReasoning),
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
	}
}

func projectModelGrantRecordFromUpsertSQLC(row dbsqlc.UpsertProjectModelGrantRow) ProjectModelGrantRecord {
	return ProjectModelGrantRecord{
		ID:                        row.ID,
		OrgID:                     row.OrgID,
		ProjectID:                 row.ProjectID,
		ConfiguredModelID:         row.ConfiguredModelID,
		ContextWindowTokens:       storeutil.IntPtr(row.ContextWindowTokens),
		MaxOutputTokens:           storeutil.IntPtr(row.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.IntPtr(row.DefaultMaxOutputTokens),
		DefaultCacheRetention:     stringFromSQLCText(row.DefaultCacheRetention),
		SupportsTools:             cloneBoolPtr(row.SupportsTools),
		SupportsReasoning:         cloneBoolPtr(row.SupportsReasoning),
		DefaultReasoningEffort:    row.DefaultReasoningEffort,
		SupportedReasoningEfforts: nonNilStringSlice(row.SupportedReasoningEfforts),
		InputModalities:           nonNilStringSlice(row.InputModalities),
		OutputModalities:          nonNilStringSlice(row.OutputModalities),
		CreatedAt:                 row.CreatedAt,
		UpdatedAt:                 row.UpdatedAt,
		Created:                   row.Created,
	}
}
