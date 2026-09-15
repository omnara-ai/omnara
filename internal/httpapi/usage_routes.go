package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s strictOpenAPIServer) GetOrgUsage(
	ctx context.Context,
	_ openapi.GetOrgUsageRequestObject,
) (openapi.GetOrgUsageResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.server.store.Execution().SumOrgModelUsage(ctx, org.ID)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	report, err := usageReportResponse(records)
	if err != nil {
		return nil, err
	}
	return openapi.GetOrgUsage200JSONResponse(report), nil
}

func (s strictOpenAPIServer) GetProjectUsage(
	ctx context.Context,
	_ openapi.GetProjectUsageRequestObject,
) (openapi.GetProjectUsageResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.server.store.Execution().SumProjectModelUsage(ctx, scope.org.ID, scope.project.ID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	report, err := usageReportResponse(records)
	if err != nil {
		return nil, err
	}
	return openapi.GetProjectUsage200JSONResponse(report), nil
}

func (s strictOpenAPIServer) GetAgentProfileUsage(
	ctx context.Context,
	request openapi.GetAgentProfileUsageRequestObject,
) (openapi.GetAgentProfileUsageResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	profileID, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, request.AgentProfileID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if _, err := s.server.store.Execution().GetAgentProfile(ctx, scope.project.ID, profileID); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	records, err := s.server.store.Execution().SumAgentProfileModelUsage(
		ctx, scope.org.ID, scope.project.ID, profileID,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	report, err := usageReportResponse(records)
	if err != nil {
		return nil, err
	}
	return openapi.GetAgentProfileUsage200JSONResponse(report), nil
}

func (s strictOpenAPIServer) GetAgentUsage(
	ctx context.Context,
	request openapi.GetAgentUsageRequestObject,
) (openapi.GetAgentUsageResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	agentIDs := []storage.ID{scope.agent.ID}
	if request.Params.IncludeSubagents != nil && *request.Params.IncludeSubagents {
		descendants, err := s.server.store.Execution().ListAgentDescendantIDs(ctx, scope.project.ID, scope.agent.ID)
		if err != nil {
			return nil, apierror.ProjectScoped(err)
		}
		agentIDs = append(agentIDs, descendants...)
	}
	records, err := s.server.store.Execution().SumAgentsModelUsage(ctx, scope.org.ID, scope.project.ID, agentIDs)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	report, err := usageReportResponse(records)
	if err != nil {
		return nil, err
	}
	return openapi.GetAgentUsage200JSONResponse(report), nil
}

func usageReportResponse(records []executionstore.ModelUsageRecord) (openapi.UsageReport, error) {
	byModel := make([]openapi.ModelUsageTotals, 0, len(records))
	for _, record := range records {
		configuredModelID, err := publicID(publicid.KindConfiguredModel, record.ConfiguredModelID)
		if err != nil {
			return openapi.UsageReport{}, err
		}
		providerConfigID, err := publicID(publicid.KindModelProviderConfig, record.ModelProviderConfigID)
		if err != nil {
			return openapi.UsageReport{}, err
		}
		byModel = append(byModel, openapi.ModelUsageTotals{
			Model: openapi.UsageModel{
				ConfiguredModelId:       configuredModelID,
				Name:                    record.ConfiguredModelName,
				ProviderModelSlug:       record.ProviderModelSlug,
				ModelProviderConfigId:   providerConfigID,
				ModelProviderConfigName: record.ModelProviderConfigName,
			},
			ModelCalls: record.Totals.ModelCalls,
			Tokens:     usageTokenTotalsResponse(record.Totals),
			Cost:       usageCostTotalsResponse(record.Totals),
		})
	}
	totals, err := executionstore.SumModelUsageTotals(records)
	if err != nil {
		return openapi.UsageReport{}, err
	}
	return openapi.UsageReport{
		Totals: openapi.UsageTotals{
			ModelCalls: totals.ModelCalls,
			Tokens:     usageTokenTotalsResponse(totals),
			Cost:       usageCostTotalsResponse(totals),
		},
		ByModel: byModel,
	}, nil
}

func usageTokenTotalsResponse(totals executionstore.ModelUsageTotals) openapi.UsageTokenTotals {
	return openapi.UsageTokenTotals{
		InputTokensTotal:      totals.InputTokensTotal,
		UncachedInputTokens:   totals.UncachedInputTokens,
		CacheReadInputTokens:  totals.CacheReadInputTokens,
		CacheWriteInputTokens: totals.CacheWriteInputTokens,
		OutputTokensTotal:     totals.OutputTokensTotal,
		ReasoningOutputTokens: totals.ReasoningOutputTokens,
	}
}

func usageCostTotalsResponse(totals executionstore.ModelUsageTotals) openapi.UsageCostTotals {
	return openapi.UsageCostTotals{
		ProviderReportedUsd:        string(totals.ProviderReportedCostUSD),
		ModelCallsWithReportedCost: totals.ModelCallsWithReportedCost,
	}
}
