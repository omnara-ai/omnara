package executionstore

import (
	"context"
	"errors"
	"fmt"

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
	ConfiguredModelID       ID
	ConfiguredModelName     string
	ProviderModelSlug       string
	ModelProviderConfigID   ID
	ModelProviderConfigName string
	Totals                  ModelUsageTotals
}

func (s *Store) SumOrgModelUsage(ctx context.Context, orgID ID) ([]ModelUsageRecord, error) {
	if isNilID(orgID) {
		return nil, errors.New("org is required")
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{OrgID: orgID})
}

func (s *Store) SumProjectModelUsage(ctx context.Context, orgID, projectID ID) ([]ModelUsageRecord, error) {
	if isNilID(orgID) || isNilID(projectID) {
		return nil, errors.New("org and project are required")
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID: orgID, ProjectID: &projectID,
	})
}

func (s *Store) SumAgentProfileModelUsage(
	ctx context.Context,
	orgID, projectID, agentProfileID ID,
) ([]ModelUsageRecord, error) {
	if isNilID(orgID) || isNilID(projectID) || isNilID(agentProfileID) {
		return nil, errors.New("org, project, and agent profile are required")
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID: orgID, ProjectID: &projectID, AgentProfileID: &agentProfileID,
	})
}

func (s *Store) SumAgentsModelUsage(
	ctx context.Context,
	orgID, projectID ID,
	agentIDs []ID,
) ([]ModelUsageRecord, error) {
	if isNilID(orgID) || isNilID(projectID) {
		return nil, errors.New("org and project are required")
	}
	if len(agentIDs) == 0 {
		return nil, errors.New("at least one agent is required")
	}
	return s.sumModelUsage(ctx, dbsqlc.SumModelCallUsageByModelParams{
		OrgID: orgID, ProjectID: &projectID, AgentIds: agentIDs,
	})
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
