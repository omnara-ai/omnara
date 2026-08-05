package modelstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
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
		return ProjectModelGrantRecord{}, fmt.Errorf(
			"an active project grant for this configured model already exists with a different configuration: %w",
			storeerr.ErrIdempotencyConflict,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectModelGrantRecord{}, fmt.Errorf("commit create project model grant: %w", err)
	}
	return record, nil
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
