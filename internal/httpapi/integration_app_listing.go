package httpapi

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

func (s strictOpenAPIServer) ListIntegrationApps(
	ctx context.Context, request openapi.ListIntegrationAppsRequestObject,
) (openapi.ListIntegrationAppsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	response, err := s.integrationAppPage(ctx, org.ID, uuid.Nil, request.Params.Limit, request.Params.Cursor)
	return openapi.ListIntegrationApps200JSONResponse(response), err
}

func (s strictOpenAPIServer) ListEligibleIntegrationApps(
	ctx context.Context, request openapi.ListEligibleIntegrationAppsRequestObject,
) (openapi.ListEligibleIntegrationAppsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	response, err := s.integrationAppPage(ctx, scope.project.OrgID, scope.project.ID,
		request.Params.Limit, request.Params.Cursor)
	return openapi.ListEligibleIntegrationApps200JSONResponse(response), err
}

func (s strictOpenAPIServer) integrationAppPage(
	ctx context.Context, orgID, projectID uuid.UUID, rawLimit *int32, rawCursor *string,
) (openapi.ListIntegrationAppsResponse, error) {
	var response openapi.ListIntegrationAppsResponse
	limit, err := parseOpenAPIPageLimit(rawLimit)
	if err != nil {
		return response, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := orgID.String() + "/" + projectID.String()
	sort := "-created_at"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Sort: &sort, Cursor: rawCursor, ListKind: "integration_apps", Scope: scopeKey,
		IDKind: publicid.KindIntegrationApp, AllowedSorts: map[string]struct{}{"created_at": {}},
	})
	if err != nil {
		return response, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := integrationstore.ListIntegrationAppsInput{OrgID: orgID, EligibleProjectID: projectID, Limit: limit}
	if options.After.Set {
		created, err := time.Parse(time.RFC3339Nano, options.After.Key)
		if err != nil || options.After.IsNull || created.IsZero() {
			return response, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid integration app cursor")
		}
		input.After = listing.KeysetCursor{Set: true, ID: options.After.ID, CreatedAt: created}
	}
	page, err := s.server.store.Integrations().ListIntegrationApps(ctx, input)
	if err != nil {
		return response, apierror.OrgScoped(err)
	}
	response.Data = make([]openapi.IntegrationAppSummary, 0, len(page.Apps))
	for _, app := range page.Apps {
		item := openapi.IntegrationAppSummary{
			Provider: app.Provider, ProviderAppRef: app.ProviderAppRef, Name: app.DisplayName,
			State: openapi.IntegrationAppState(app.State), CreatedAt: app.CreatedAt, UpdatedAt: app.UpdatedAt,
		}
		item.Id, err = publicID(publicid.KindIntegrationApp, app.ID)
		if err != nil {
			return response, err
		}
		item.OrgId, err = publicID(publicid.KindOrganization, app.OrgID)
		if err != nil {
			return response, err
		}
		item.OwnerProjectId, err = idOrNil(publicid.KindProject, app.OwnerProjectID)
		if err != nil {
			return response, err
		}
		response.Data = append(response.Data, item)
	}
	var after listing.Cursor
	if page.Next.Set {
		after = listing.Cursor{Set: true, Key: page.Next.CreatedAt.UTC().Format(time.RFC3339Nano), ID: page.Next.ID}
	}
	next, err := encodeResourceListNextCursor(page.Next.Set, after, options,
		"integration_apps", scopeKey, publicid.KindIntegrationApp, nil)
	response.NextCursor = nullableFromPtr(next)
	return response, err
}
