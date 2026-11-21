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
	"github.com/omnara-ai/omnara/internal/storage/listing"
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

// parseOtelExporterScope returns the project an exporter scope names.
func parseOtelExporterScope(input openapi.OtelExporterScope) (uuid.UUID, error) {
	projectID, ok := parseOpenAPIPublicID(publicid.KindProject, input.ProjectId)
	if !ok || input.Type != openapi.OtelExporterScopeTypeProject {
		return uuid.Nil, errInvalidOtelExporterScope
	}
	return projectID, nil
}

func otelExporterScopeResponse(projectID uuid.UUID) (openapi.OtelExporterScope, error) {
	id, err := publicID(publicid.KindProject, projectID)
	return openapi.OtelExporterScope{Type: openapi.OtelExporterScopeTypeProject, ProjectId: id}, err
}

func parseOtelExporterAuthOptions(input *openapi.OtelExporterAuthOptions) otelconfig.AuthOptions {
	if input == nil {
		return otelconfig.AuthOptions{}
	}
	var options otelconfig.AuthOptions
	if input.HeaderName != nil {
		options.HeaderName = *input.HeaderName
	}
	if input.Username != nil {
		options.Username = *input.Username
	}
	return options
}

func otelExporterAuthOptionsResponse(options otelconfig.AuthOptions) openapi.OtelExporterAuthOptions {
	response := openapi.OtelExporterAuthOptions{}
	if options.HeaderName != "" {
		value := options.HeaderName
		response.HeaderName = &value
	}
	if options.Username != "" {
		value := options.Username
		response.Username = &value
	}
	return response
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
	scope, err := otelExporterScopeResponse(record.ProjectID)
	if err != nil {
		return openapi.OtelExporter{}, err
	}
	result := openapi.OtelExporter{
		Id: id, OrgId: orgID, Scope: scope, Endpoint: record.Settings.Endpoint,
		Variant:       openapi.OtelTraceVariant(record.Settings.Variant),
		Encoding:      openapi.OtelExporterEncoding(record.Settings.Encoding),
		AuthKind:      openapi.OtelExporterAuthKind(record.Settings.AuthKind),
		AuthOptions:   otelExporterAuthOptionsResponse(record.Settings.AuthOptions),
		Headers:       openapi.OtelExporterHeaders{},
		SecretHeaders: openapi.OtelExporterSecretHeaders{},
		SamplePercent: record.Settings.SamplePercent,
		CreatedAt:     record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if record.Settings.CredentialSecretID != uuid.Nil {
		secretID, err := publicID(publicid.KindSecret, record.Settings.CredentialSecretID)
		if err != nil {
			return openapi.OtelExporter{}, err
		}
		result.CredentialSecretId = &secretID
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
	projectID, err := parseOtelExporterScope(request.Body.Scope)
	if err != nil {
		return nil, err
	}
	settings := otelconfig.Settings{
		Endpoint: request.Body.Endpoint, Variant: oteltrace.VariantOTel,
		AuthKind: otelconfig.AuthKindNone, SamplePercent: 100,
	}
	if request.Body.Variant != nil {
		settings.Variant = oteltrace.Variant(*request.Body.Variant)
	}
	if request.Body.Encoding != nil {
		settings.Encoding = oteltrace.Encoding(*request.Body.Encoding)
	}
	if request.Body.AuthKind != nil {
		settings.AuthKind = otelconfig.AuthKind(*request.Body.AuthKind)
	}
	if request.Body.AuthOptions != nil {
		settings.AuthOptions = parseOtelExporterAuthOptions(request.Body.AuthOptions)
	}
	if request.Body.CredentialSecretId != nil {
		secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, *request.Body.CredentialSecretId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid credential_secret_id")
		}
		settings.CredentialSecretID = secretID
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
		OrgID: org.ID, ProjectID: projectID, Settings: settings,
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
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	input := executionstore.ListOtelExportersInput{OrgID: org.ID, Limit: limit}
	if request.Params.ProjectId != nil {
		projectID, ok := parseOpenAPIPublicID(publicid.KindProject, *request.Params.ProjectId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid project_id")
		}
		input.ProjectID = projectID
	}
	// The cursor is bound to the organization and the project filter, so one
	// list's position cannot be replayed against another.
	sort := "created_at"
	options, err := parseResourceListQuery(resourceListQueryInput{
		Cursor: request.Params.Cursor, Sort: &sort, AllowedSorts: sortSet(sort),
		ListKind: "otel_exporters", Scope: org.ID.String(),
		IDKind: publicid.KindOtelExporter, Extra: input.ProjectID,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if options.After.Set {
		createdAt, err := listing.ParseTimestampKey(options.After.Key)
		if err != nil || createdAt.IsZero() || options.After.IsNull {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, errMalformedCursor.Error())
		}
		input.After = listing.KeysetCursor{Set: true, CreatedAt: createdAt, ID: options.After.ID}
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
	next, err := encodeResourceListNextCursor(page.HasMore,
		listing.Cursor{Set: last.ID != uuid.Nil, Key: listing.TimestampKey(last.CreatedAt), ID: last.ID},
		options, "otel_exporters", org.ID.String(), publicid.KindOtelExporter, input.ProjectID)
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
	if request.Body.AuthKind != nil {
		authKind := otelconfig.AuthKind(*request.Body.AuthKind)
		input.AuthKind = &authKind
	}
	if request.Body.AuthOptions != nil {
		options := parseOtelExporterAuthOptions(request.Body.AuthOptions)
		input.AuthOptions = &options
	}
	if request.Body.CredentialSecretId.IsSpecified() {
		input.SetCredentialSecretID = true
		if !request.Body.CredentialSecretId.IsNull() {
			rawID, err := request.Body.CredentialSecretId.Get()
			if err != nil {
				return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid credential_secret_id")
			}
			secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, rawID)
			if !ok {
				return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid credential_secret_id")
			}
			input.CredentialSecretID = secretID
		}
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
