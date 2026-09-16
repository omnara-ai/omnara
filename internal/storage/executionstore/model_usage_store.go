package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ModelUsageTotals struct {
	ModelCalls                 int64
	ModelCallsWithReportedCost int64
	InputTokensTotal           int64
	UncachedInputTokens        int64
	CacheReadInputTokens       int64
	CacheWriteInputTokens      int64
	OutputTokensTotal          int64
	ReasoningOutputTokens      int64
	ProviderReportedCostUSD    modelenvelope.ProviderReportedCostUSD
}

type ModelUsageRecord struct {
	ConfiguredModelID       uuid.UUID
	ConfiguredModelName     string
	ProviderModelSlug       string
	ModelProviderConfigID   uuid.UUID
	ModelProviderConfigName string
	Totals                  ModelUsageTotals
}

type UsageWindow struct {
	Since *time.Time
	Until *time.Time
}

func (w UsageWindow) validate() error {
	if w.Since != nil && w.Until != nil && !w.Until.After(*w.Since) {
		return errors.New("usage window until must be after since")
	}
	return nil
}

type SumOrgModelUsageInput struct {
	OrgID             uuid.UUID
	Window            UsageWindow
	IncludeProjectIDs []uuid.UUID
	ExcludeProjectIDs []uuid.UUID
}

type SumProjectModelUsageInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Window    UsageWindow
}

type SumAgentProfileModelUsageInput struct {
	OrgID            uuid.UUID
	ProjectID        uuid.UUID
	AgentProfileID   uuid.UUID
	IncludeSubagents bool
	Window           UsageWindow
}

type SumAgentsModelUsageInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	AgentIDs  []uuid.UUID
	Window    UsageWindow
}

func (s *Store) SumOrgModelUsage(ctx context.Context, input SumOrgModelUsageInput) ([]ModelUsageRecord, error) {
	if input.OrgID == uuid.Nil {
		return nil, errors.New("org is required")
	}
	if len(input.IncludeProjectIDs) > 0 && len(input.ExcludeProjectIDs) > 0 {
		return nil, errors.New("include and exclude project filters are mutually exclusive")
	}
	if err := input.Window.validate(); err != nil {
		return nil, err
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID:             input.OrgID,
		IncludeProjectIds: nilIfEmpty(input.IncludeProjectIDs),
		ExcludeProjectIds: nilIfEmpty(input.ExcludeProjectIDs),
		Since:             input.Window.Since,
		Until:             input.Window.Until,
	})
}

func (s *Store) SumProjectModelUsage(ctx context.Context, input SumProjectModelUsageInput) ([]ModelUsageRecord, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return nil, errors.New("org and project are required")
	}
	if err := input.Window.validate(); err != nil {
		return nil, err
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID:     input.OrgID,
		ProjectID: &input.ProjectID,
		Since:     input.Window.Since,
		Until:     input.Window.Until,
	})
}

func (s *Store) SumAgentProfileModelUsage(
	ctx context.Context,
	input SumAgentProfileModelUsageInput,
) ([]ModelUsageRecord, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil || input.AgentProfileID == uuid.Nil {
		return nil, errors.New("org, project, and agent profile are required")
	}
	if err := input.Window.validate(); err != nil {
		return nil, err
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID:                   input.OrgID,
		ProjectID:               &input.ProjectID,
		AgentProfileID:          &input.AgentProfileID,
		IncludeProfileSubagents: input.IncludeSubagents,
		Since:                   input.Window.Since,
		Until:                   input.Window.Until,
	})
}

func (s *Store) SumAgentsModelUsage(ctx context.Context, input SumAgentsModelUsageInput) ([]ModelUsageRecord, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return nil, errors.New("org and project are required")
	}
	if len(input.AgentIDs) == 0 {
		return nil, errors.New("at least one agent is required")
	}
	if err := input.Window.validate(); err != nil {
		return nil, err
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID:     input.OrgID,
		ProjectID: &input.ProjectID,
		AgentIds:  input.AgentIDs,
		Since:     input.Window.Since,
		Until:     input.Window.Until,
	})
}

func nilIfEmpty(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func (s *Store) sumModelUsage(
	ctx context.Context,
	params dbsqlc.SumModelCallUsageByModelParams,
) ([]ModelUsageRecord, error) {
	rows, err := s.q.SumModelCallUsageByModel(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("sum model usage: %w", err)
	}
	records := make([]ModelUsageRecord, 0, len(rows))
	for _, row := range rows {
		cost, ok := modelenvelope.ParseProviderReportedCostUSD(row.ProviderReportedCostUsd)
		if !ok {
			return nil, fmt.Errorf("sum model usage: invalid cost total %q", row.ProviderReportedCostUsd)
		}
		records = append(records, ModelUsageRecord{
			ConfiguredModelID:       row.ConfiguredModelID,
			ConfiguredModelName:     row.ConfiguredModelName,
			ProviderModelSlug:       row.ProviderModelSlug,
			ModelProviderConfigID:   row.ModelProviderConfigID,
			ModelProviderConfigName: row.ModelProviderConfigName,
			Totals: ModelUsageTotals{
				ModelCalls:                 row.ModelCalls,
				ModelCallsWithReportedCost: row.ModelCallsWithReportedCost,
				InputTokensTotal:           row.InputTokensTotal,
				UncachedInputTokens:        row.UncachedInputTokens,
				CacheReadInputTokens:       row.CacheReadInputTokens,
				CacheWriteInputTokens:      row.CacheWriteInputTokens,
				OutputTokensTotal:          row.OutputTokensTotal,
				ReasoningOutputTokens:      row.ReasoningOutputTokens,
				ProviderReportedCostUSD:    cost,
			},
		})
	}
	return records, nil
}

func SumModelUsageTotals(records []ModelUsageRecord) (ModelUsageTotals, error) {
	totals := ModelUsageTotals{ProviderReportedCostUSD: "0"}
	costs := make([]string, 0, len(records)+1)
	costs = append(costs, "0")
	for _, record := range records {
		totals.ModelCalls += record.Totals.ModelCalls
		totals.ModelCallsWithReportedCost += record.Totals.ModelCallsWithReportedCost
		totals.InputTokensTotal += record.Totals.InputTokensTotal
		totals.UncachedInputTokens += record.Totals.UncachedInputTokens
		totals.CacheReadInputTokens += record.Totals.CacheReadInputTokens
		totals.CacheWriteInputTokens += record.Totals.CacheWriteInputTokens
		totals.OutputTokensTotal += record.Totals.OutputTokensTotal
		totals.ReasoningOutputTokens += record.Totals.ReasoningOutputTokens
		costs = append(costs, string(record.Totals.ProviderReportedCostUSD))
	}
	cost, ok := modelenvelope.SumProviderReportedCostUSD(costs...)
	if !ok {
		return ModelUsageTotals{}, errors.New("sum model usage: invalid cost totals")
	}
	totals.ProviderReportedCostUSD = cost
	return totals, nil
}
