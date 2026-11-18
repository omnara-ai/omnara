package httpapi

import (
	"context"
	"maps"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	otelconfig "github.com/omnara-ai/omnara/internal/otelexport/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s strictOpenAPIServer) GetTurnTrace(
	ctx context.Context,
	request openapi.GetTurnTraceRequestObject,
) (openapi.GetTurnTraceResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	turnID, ok := parseOpenAPIPublicID(publicid.KindAgentTurn, request.TurnID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	variant := oteltrace.VariantOTel
	if request.Params.Variant != nil {
		if variant, ok = oteltrace.ParseVariant(string(*request.Params.Variant)); !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported variant")
		}
	}
	turn, err := s.server.store.Execution().LoadTurnTrace(ctx, scope.project.ID, scope.agent.ID, turnID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	encoded, _, err := oteltrace.Encode(oteltrace.Build(turn, variant), oteltrace.EncodingJSON)
	if err != nil {
		return nil, err
	}
	return turnTraceResponse(encoded), nil
}

// turnTraceResponse is an already encoded OtelTrace, written as is instead of
// being decoded into the generated type only to be encoded again.
type turnTraceResponse []byte

func (body turnTraceResponse) VisitGetTurnTraceResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(body)
	return err
}

var errInvalidOtelExporterScope = apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid exporter scope")

func parseOtelExporterScope(input openapi.OtelExporterScope) (executionstore.OtelExporterScope, error) {
	invalid := errInvalidOtelExporterScope
	discriminator, err := input.Discriminator()
	if err != nil {
		return executionstore.OtelExporterScope{}, invalid
	}
	scopeType := executionstore.OtelExporterScopeType(discriminator)
	var projectID, agentID *string
	switch scopeType {
	case executionstore.OtelExporterScopeOrg:
	case executionstore.OtelExporterScopeProject:
		value, err := input.AsProjectOtelExporterScope()
		if err != nil {
			return executionstore.OtelExporterScope{}, invalid
		}
		projectID = &value.ProjectId
	case executionstore.OtelExporterScopeAgent:
		value, err := input.AsAgentOtelExporterScope()
		if err != nil {
			return executionstore.OtelExporterScope{}, invalid
		}
		projectID, agentID = &value.ProjectId, &value.AgentId
	default:
		return executionstore.OtelExporterScope{}, invalid
	}
	return parseOtelExporterScopeIDs(scopeType, projectID, agentID)
}

func parseOtelExporterScopeIDs(
	scopeType executionstore.OtelExporterScopeType, projectID, agentID *string,
) (executionstore.OtelExporterScope, error) {
	invalid := errInvalidOtelExporterScope
	scope := executionstore.OtelExporterScope{Type: scopeType}
	var ok bool
	if projectID != nil {
		if scope.ProjectID, ok = parseOpenAPIPublicID(publicid.KindProject, *projectID); !ok {
			return executionstore.OtelExporterScope{}, invalid
		}
	}
	if agentID != nil {
		if scope.AgentID, ok = parseOpenAPIPublicID(publicid.KindAgent, *agentID); !ok {
			return executionstore.OtelExporterScope{}, invalid
		}
	}
	if scope.Validate() != nil {
		return executionstore.OtelExporterScope{}, invalid
	}
	return scope, nil
}

func otelExporterScopeResponse(scope executionstore.OtelExporterScope) (openapi.OtelExporterScope, error) {
	var response openapi.OtelExporterScope
	if scope.Type == executionstore.OtelExporterScopeOrg {
		err := response.FromOrgOtelExporterScope(openapi.OrgOtelExporterScope{Type: openapi.OrgOtelExporterScopeTypeOrg})
		return response, err
	}
	projectID, err := publicID(publicid.KindProject, scope.ProjectID)
	if err != nil {
		return response, err
	}
	if scope.Type == executionstore.OtelExporterScopeProject {
		err = response.FromProjectOtelExporterScope(openapi.ProjectOtelExporterScope{
			Type: openapi.ProjectOtelExporterScopeTypeProject, ProjectId: projectID,
		})
		return response, err
	}
	agentID, err := publicID(publicid.KindAgent, scope.AgentID)
	if err != nil {
		return response, err
	}
	err = response.FromAgentOtelExporterScope(openapi.AgentOtelExporterScope{
		Type: openapi.AgentOtelExporterScopeTypeAgent, ProjectId: projectID, AgentId: agentID,
	})
	return response, err
}

func parseOtelExporterSecretHeaders(input openapi.OtelExporterSecretHeaders) (map[string]uuid.UUID, error) {
	result := make(map[string]uuid.UUID, len(input))
	for name, rawID := range input {
		id, ok := parseOpenAPIPublicID(publicid.KindSecret, rawID)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid secret_headers secret id")
		}
		result[name] = id
	}
	return result, nil
}

func otelExporterResponseFromRecord(record executionstore.OtelExporterRecord) (openapi.OtelExporter, error) {
	id, err := publicID(publicid.KindOtelExporter, record.ID)
	if err != nil {
		return openapi.OtelExporter{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.OtelExporter{}, err
	}
	scope, err := otelExporterScopeResponse(record.Scope)
	if err != nil {
		return openapi.OtelExporter{}, err
	}
	result := openapi.OtelExporter{
		Id: id, OrgId: orgID, Scope: scope, Endpoint: record.Settings.Endpoint,
		Variant:       openapi.OtelTraceVariant(record.Settings.Variant),
		Encoding:      openapi.OtelExporterEncoding(record.Settings.Encoding),
		Headers:       openapi.OtelExporterHeaders{},
		SecretHeaders: openapi.OtelExporterSecretHeaders{},
		SamplePercent: record.Settings.SamplePercent,
		CreatedAt:     record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	maps.Copy(result.Headers, record.Settings.Headers)
	for name, secretID := range record.Settings.SecretHeaders {
		if result.SecretHeaders[name], err = publicID(publicid.KindSecret, secretID); err != nil {
			return openapi.OtelExporter{}, err
		}
	}
	return result, nil
}

func (s strictOpenAPIServer) CreateOtelExporter(
	ctx context.Context,
	request openapi.CreateOtelExporterRequestObject,
) (openapi.CreateOtelExporterResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	scope, err := parseOtelExporterScope(request.Body.Scope)
	if err != nil {
		return nil, err
	}
	settings := otelconfig.Settings{
		Endpoint: request.Body.Endpoint, Variant: oteltrace.VariantOTel, SamplePercent: 100,
	}
	if request.Body.Variant != nil {
		settings.Variant = oteltrace.Variant(*request.Body.Variant)
	}
	if request.Body.Encoding != nil {
		settings.Encoding = oteltrace.Encoding(*request.Body.Encoding)
	}
	if request.Body.Headers != nil {
		settings.Headers = *request.Body.Headers
	}
	if request.Body.SecretHeaders != nil {
		if settings.SecretHeaders, err = parseOtelExporterSecretHeaders(*request.Body.SecretHeaders); err != nil {
			return nil, err
		}
	}
	if request.Body.SamplePercent != nil {
		settings.SamplePercent = *request.Body.SamplePercent
	}
	record, err := s.server.store.Execution().CreateOtelExporter(ctx, executionstore.CreateOtelExporterInput{
		OrgID: org.ID, Scope: scope, Settings: settings,
	})
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := otelExporterResponseFromRecord(record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateOtelExporter201JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListOtelExporters(
	ctx context.Context,
	request openapi.ListOtelExportersRequestObject,
) (openapi.ListOtelExportersResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	limit, after, err := parseOpenAPIPageParams(request.Params.Limit, request.Params.Cursor, publicid.KindOtelExporter)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := executionstore.ListOtelExportersInput{OrgID: org.ID, Limit: limit, After: after}
	if request.Params.ScopeType != nil {
		scope, err := parseOtelExporterScopeIDs(executionstore.OtelExporterScopeType(*request.Params.ScopeType),
			request.Params.ProjectId, request.Params.AgentId)
		if err != nil {
			return nil, err
		}
		input.Scope = &scope
	} else if request.Params.ProjectId != nil || request.Params.AgentId != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "project or agent ID requires scope_type")
	}
	page, err := s.server.store.Execution().ListOtelExporters(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response := openapi.ListOtelExportersResponse{Data: make([]openapi.OtelExporter, 0, len(page.Exporters))}
	var last executionstore.OtelExporterRecord
	for _, record := range page.Exporters {
		exporter, err := otelExporterResponseFromRecord(record)
		if err != nil {
			return nil, err
		}
		response.Data = append(response.Data, exporter)
		last = record
	}
	next, err := encodeNextCursor(page.HasMore, last.CreatedAt, publicid.KindOtelExporter, last.ID)
	if err != nil {
		return nil, err
	}
	response.NextCursor = nullableFromPtr(next)
	return openapi.ListOtelExporters200JSONResponse(response), nil
}

func (s strictOpenAPIServer) GetOtelExporter(
	ctx context.Context,
	request openapi.GetOtelExporterRequestObject,
) (openapi.GetOtelExporterResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindOtelExporter, request.ExporterID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	record, err := s.server.store.Execution().GetOtelExporter(ctx, org.ID, id)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := otelExporterResponseFromRecord(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetOtelExporter200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateOtelExporter(
	ctx context.Context,
	request openapi.UpdateOtelExporterRequestObject,
) (openapi.UpdateOtelExporterResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindOtelExporter, request.ExporterID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	input := executionstore.UpdateOtelExporterInput{
		OrgID: org.ID, ExporterID: id, Endpoint: request.Body.Endpoint, SamplePercent: request.Body.SamplePercent,
	}
	if request.Body.Variant != nil {
		variant := oteltrace.Variant(*request.Body.Variant)
		input.Variant = &variant
	}
	if request.Body.Encoding != nil {
		encoding := oteltrace.Encoding(*request.Body.Encoding)
		input.Encoding = &encoding
	}
	if request.Body.Headers != nil {
		input.Headers = *request.Body.Headers
	}
	if request.Body.SecretHeaders != nil {
		if input.SecretHeaders, err = parseOtelExporterSecretHeaders(*request.Body.SecretHeaders); err != nil {
			return nil, err
		}
	}
	record, err := s.server.store.Execution().UpdateOtelExporter(ctx, input)
	if err != nil {
		return nil, apierror.OrgScoped(err)
	}
	response, err := otelExporterResponseFromRecord(record)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateOtelExporter200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteOtelExporter(
	ctx context.Context,
	request openapi.DeleteOtelExporterRequestObject,
) (openapi.DeleteOtelExporterResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindOtelExporter, request.ExporterID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err := s.server.store.Execution().DeleteOtelExporter(ctx, org.ID, id); err != nil {
		return nil, apierror.OrgScoped(err)
	}
	return openapi.DeleteOtelExporter204Response{}, nil
}
