package httpapi

import (
	"context"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func secretListFilters(metadata *openapi.SecretMetadataFilter) secretstore.SecretListFilters {
	filters := secretstore.SecretListFilters{Metadata: map[string]string{}}
	if metadata != nil {
		filters.Metadata = map[string]string(*metadata)
	}
	return filters
}

func oauthAccessTokenLifetime(seconds *int32, startedAt time.Time) secrets.OAuthAccessTokenLifetime {
	if seconds == nil {
		return secrets.OAuthAccessTokenLifetime{}
	}
	return secrets.NewOAuthAccessTokenLifetime(time.Duration(*seconds)*time.Second, startedAt)
}

func newSecretOwner(record secretstore.SecretRecord) (openapi.SecretOwner, error) {
	var owner openapi.SecretOwner
	switch record.OwnerKind {
	case secretstore.SecretOwnerOrg:
		err := owner.FromOrgSecretOwner(openapi.OrgSecretOwner{Kind: openapi.OrgSecretOwnerKindOrg})
		return owner, err
	case secretstore.SecretOwnerProject:
		projectID, err := publicID(publicid.KindProject, record.OwnerProjectID)
		if err != nil {
			return owner, err
		}
		err = owner.FromProjectSecretOwner(openapi.ProjectSecretOwner{
			Kind: openapi.ProjectSecretOwnerKindProject, ProjectId: projectID,
		})
		return owner, err
	case secretstore.SecretOwnerUser:
		userID, err := publicID(publicid.KindUser, record.OwnerUserID)
		if err != nil {
			return owner, err
		}
		err = owner.FromUserSecretOwner(openapi.UserSecretOwner{Kind: openapi.UserSecretOwnerKindUser, UserId: userID})
		return owner, err
	default:
		return owner, errors.New("unsupported secret owner kind")
	}
}

func newSecretResponse(record secretstore.SecretRecord) (openapi.Secret, error) {
	id, err := publicID(publicid.KindSecret, record.ID)
	if err != nil {
		return openapi.Secret{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.Secret{}, err
	}
	owner, err := newSecretOwner(record)
	if err != nil {
		return openapi.Secret{}, err
	}
	metadata, err := resourcemeta.FromJSON(record.Metadata)
	if err != nil {
		return openapi.Secret{}, err
	}
	response := openapi.Secret{
		Id: id, OrgId: orgID, ManagementKind: openapi.ManagementKind(record.ManagementKind),
		Owner: owner, Name: record.Name, Kind: openapi.SecretKind(record.Kind),
		Metadata: metadata, CurrentVersionNumber: record.CurrentVersionNumber,
		PayloadKeys: append([]string(nil), record.PayloadKeys...),
		CreatedAt:   record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	return response, nil
}

func newProjectSecretAccessResponse(record secretstore.ProjectSecretAccessRecord) (openapi.ProjectSecretAccess, error) {
	secret, err := newSecretResponse(record.Secret)
	if err != nil {
		return openapi.ProjectSecretAccess{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.Availability.ProjectID)
	if err != nil {
		return openapi.ProjectSecretAccess{}, err
	}
	var availability openapi.SecretAvailability
	switch record.Availability.Source {
	case secretstore.SecretAvailabilityDirect:
		err = availability.FromDirectSecretAvailability(openapi.DirectSecretAvailability{
			Source: openapi.DirectSecretAvailabilitySourceDirect, ProjectId: projectID,
		})
	case secretstore.SecretAvailabilityGrant:
		var grantID string
		grantID, err = publicID(publicid.KindSecretGrant, record.Availability.GrantID)
		if err == nil {
			err = availability.FromGrantedSecretAvailability(openapi.GrantedSecretAvailability{
				Source:    openapi.GrantedSecretAvailabilitySourceGrant,
				ProjectId: projectID, GrantId: grantID,
			})
		}
	default:
		err = errors.New("unsupported secret availability source")
	}
	if err != nil {
		return openapi.ProjectSecretAccess{}, err
	}
	return openapi.ProjectSecretAccess{Secret: secret, Availability: availability}, nil
}

func newSecretGrantResponse(record secretstore.SecretGrantRecord) (openapi.SecretGrant, error) {
	id, err := publicID(publicid.KindSecretGrant, record.ID)
	if err != nil {
		return openapi.SecretGrant{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.SecretGrant{}, err
	}
	secretID, err := publicID(publicid.KindSecret, record.SecretID)
	if err != nil {
		return openapi.SecretGrant{}, err
	}
	projectID, err := publicID(publicid.KindProject, record.TargetProjectID)
	if err != nil {
		return openapi.SecretGrant{}, err
	}
	return openapi.SecretGrant{
		Id: id, OrgId: orgID, SecretId: secretID, TargetProjectId: projectID,
		CreatedAt: record.CreatedAt,
	}, nil
}

func secretAPIError(ctx context.Context, err error) apierror.ResponseError {
	switch {
	case errors.Is(err, storeerr.ErrUnauthorized):
		return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	case errors.Is(err, storeerr.ErrConflict):
		return apierror.FromCode(openapi.ErrorCodeConflict, "conflict")
	case errors.Is(err, storeerr.ErrStateTransitionConflict):
		return apierror.FromCode(openapi.ErrorCodeConflict, "cluster-managed secrets cannot be changed")
	case errors.Is(err, storeerr.ErrInvalidSecretName):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	case errors.Is(err, storeerr.ErrInvalidRequest), errors.Is(err, storeerr.ErrInvalidSecretRequest):
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid secret request")
	case storeerr.IsNotFound(err):
		return apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	default:
		logpkg.Error(ctx, err)
		return apierror.FromCode(openapi.ErrorCodeInternalError, "internal server error")
	}
}

func accountPrincipalFromContext(ctx context.Context) (identitystore.PrincipalRecord, *apierror.ResponseError) {
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return identitystore.PrincipalRecord{}, &err
	}
	return principal, nil
}

func parseSecretOwnerInput(
	input openapi.SecretOwnerInput,
	principal identitystore.PrincipalRecord,
) (string, storage.ID, storage.ID, *apierror.ResponseError) {
	kind, err := input.Discriminator()
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
		return "", storage.NilID, storage.NilID, &apiErr
	}
	switch kind {
	case secretstore.SecretOwnerOrg:
		if _, err := input.AsOrgSecretOwnerInput(); err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
			return "", storage.NilID, storage.NilID, &apiErr
		}
		return kind, storage.NilID, storage.NilID, nil
	case secretstore.SecretOwnerProject:
		owner, err := input.AsProjectSecretOwnerInput()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
			return "", storage.NilID, storage.NilID, &apiErr
		}
		projectID, ok := parseOpenAPIPublicID(publicid.KindProject, owner.ProjectId)
		if !ok {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner project_id")
			return "", storage.NilID, storage.NilID, &apiErr
		}
		return kind, projectID, storage.NilID, nil
	case secretstore.SecretOwnerUser:
		if _, err := input.AsUserSecretOwnerInput(); err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner")
			return "", storage.NilID, storage.NilID, &apiErr
		}
		if principal.Type != identitystore.PrincipalTypeUser {
			apiErr := apierror.FromCode(
				openapi.ErrorCodeInvalidRequest,
				"user-owned secrets require a user principal",
			)
			return "", storage.NilID, storage.NilID, &apiErr
		}
		return kind, storage.NilID, principal.ID, nil
	default:
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner kind")
		return "", storage.NilID, storage.NilID, &apiErr
	}
}

func parseSecretMaterial(
	input openapi.SecretMaterial,
	receivedAt time.Time,
) (secrets.Material, *apierror.ResponseError) {
	kind, err := input.Discriminator()
	if err != nil {
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid secret material")
		return nil, &apiErr
	}
	switch secrets.Kind(kind) {
	case secrets.KindGeneric:
		material, err := input.AsGenericSecretMaterial()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid generic secret material")
			return nil, &apiErr
		}
		return secrets.GenericMaterial{Value: material.Value}, nil
	case secrets.KindOAuthTokenSet:
		material, err := input.AsOAuthTokenSetSecretMaterial()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid OAuth token-set material")
			return nil, &apiErr
		}
		oauth := secrets.OAuthTokenSetMaterial{
			AccessToken:         material.AccessToken,
			AccessTokenLifetime: oauthAccessTokenLifetime(material.AccessTokenExpiresInSeconds, receivedAt),
			IDToken:             valueOrEmpty(material.IdToken),
			MCPURL:              valueOrEmpty(material.McpUrl),
			Scopes:              valueOrEmpty(material.Scopes),
			TokenType:           valueOrEmpty(material.TokenType),
		}
		if material.Refresh != nil {
			oauth.Refresh = &secrets.OAuthRefreshMaterial{
				RefreshToken:  material.Refresh.RefreshToken,
				TokenEndpoint: material.Refresh.TokenEndpoint,
				ClientID:      material.Refresh.ClientId,
				ClientSecret:  valueOrEmpty(material.Refresh.ClientSecret),
				Resource:      material.Refresh.Resource,
			}
		}
		return oauth, nil
	case secrets.KindAWSCredentials:
		material, err := input.AsAWSCredentialsSecretMaterial()
		if err != nil {
			apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid AWS credentials material")
			return nil, &apiErr
		}
		return secrets.AWSCredentialsMaterial{
			AccessKeyID:     material.AccessKeyId,
			SecretAccessKey: material.SecretAccessKey,
			SessionToken:    valueOrEmpty(material.SessionToken),
			RoleARN:         valueOrEmpty(material.RoleArn),
			ExternalID:      valueOrEmpty(material.ExternalId),
		}, nil
	default:
		apiErr := apierror.FromCode(openapi.ErrorCodeInvalidRequest, "unsupported secret material kind")
		return nil, &apiErr
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func canonicalSecret(
	ctx context.Context,
	store *secretstore.Store,
	orgID storage.ID,
	rawID string,
	principal identitystore.PrincipalRecord,
) (secretstore.SecretRecord, *apierror.ResponseError) {
	secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, rawID)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return secretstore.SecretRecord{}, &err
	}
	record, err := store.GetVisibleSecret(ctx, orgID, secretID, principal)
	if err != nil {
		apiErr := secretAPIError(ctx, err)
		return secretstore.SecretRecord{}, &apiErr
	}
	return record, nil
}

func secretResponses(records []secretstore.SecretRecord) ([]openapi.Secret, error) {
	out := make([]openapi.Secret, 0, len(records))
	for _, record := range records {
		response, err := newSecretResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
	}
	return out, nil
}

func (s strictOpenAPIServer) CreateSecret(
	ctx context.Context,
	request openapi.CreateSecretRequestObject,
) (openapi.CreateSecretResponseObject, error) {
	receivedAt := time.Now()
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	ownerKind, ownerProjectID, ownerUserID, ownerErr := parseSecretOwnerInput(request.Body.Owner, principal)
	if ownerErr != nil {
		return nil, *ownerErr
	}
	metadata := request.Body.Metadata
	material, materialErr := parseSecretMaterial(request.Body.Material, receivedAt)
	if materialErr != nil {
		return nil, *materialErr
	}
	record, _, err := s.server.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          org.ID,
		OwnerKind:      ownerKind,
		OwnerProjectID: ownerProjectID,
		OwnerUserID:    ownerUserID,
		Name:           request.Body.Name,
		Metadata:       metadata,
		Material:       material,
		Actor:          principal,
	})
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	response, err := newSecretResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.CreateSecret201JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListSecrets(
	ctx context.Context,
	request openapi.ListSecretsRequestObject,
) (openapi.ListSecretsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := secretListFilters(request.Params.Metadata)
	kind := ""
	if request.Params.Kind != nil {
		kind = string(*request.Params.Kind)
		filters.Kinds = []string{kind}
	}
	if request.Params.OwnerKind != nil {
		filters.OwnerKind = string(*request.Params.OwnerKind)
	}
	if request.Params.OwnerProjectId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindProject, *request.Params.OwnerProjectId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid owner_project_id")
		}
		filters.OwnerProjectID = id
	}
	if request.Params.McpOauthFlowId != nil {
		id, ok := parseOpenAPIPublicID(publicid.KindMCPOAuthFlow, *request.Params.McpOauthFlowId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid mcp_oauth_flow_id")
		}
		filters.MCPOAuthFlowID = id
	}
	extra := struct {
		OwnerKind, OwnerProjectID string
		MCPOAuthFlowID            string
		Kind                      string
		Metadata                  map[string]string
	}{OwnerKind: filters.OwnerKind, Kind: kind, Metadata: filters.Metadata}
	if filters.OwnerProjectID != storage.NilID {
		extra.OwnerProjectID = filters.OwnerProjectID.String()
	}
	if filters.MCPOAuthFlowID != storage.NilID {
		extra.MCPOAuthFlowID = filters.MCPOAuthFlowID.String()
	}
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "secrets",
		Scope: org.ID.String(), IDKind: publicid.KindSecret,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Secrets().ListSecrets(ctx, secretstore.ListSecretsInput{
		OrgID: org.ID, Actor: principal, Filters: filters, Limit: limit, List: list,
	})
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	data, err := secretResponses(page.Secrets)
	if err != nil {
		return nil, err
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "secrets", org.ID.String(), publicid.KindSecret, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListSecrets200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func (s strictOpenAPIServer) GetSecret(
	ctx context.Context,
	request openapi.GetSecretRequestObject,
) (openapi.GetSecretResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	record, apiErr := canonicalSecret(ctx, s.server.store.Secrets(), org.ID, request.SecretID, principal)
	if apiErr != nil {
		return nil, *apiErr
	}
	response, err := newSecretResponse(record)
	if err != nil {
		return nil, err
	}
	return openapi.GetSecret200JSONResponse(response), nil
}

func (s strictOpenAPIServer) UpdateSecret(
	ctx context.Context,
	request openapi.UpdateSecretRequestObject,
) (openapi.UpdateSecretResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil || (request.Body.Name == nil && request.Body.Metadata == nil) {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "name or metadata is required")
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	record, apiErr := canonicalSecret(ctx, s.server.store.Secrets(), org.ID, request.SecretID, principal)
	if apiErr != nil {
		return nil, *apiErr
	}
	name := record.Name
	metadata, err := resourcemeta.FromJSON(record.Metadata)
	if err != nil {
		return nil, err
	}
	if request.Body.Name != nil {
		canonicalName, err := resourcename.CanonicalizeRequired("secret name", *request.Body.Name)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		}
		name = canonicalName
	}
	if request.Body.Metadata != nil {
		metadata = request.Body.Metadata
	}
	updated, err := s.server.store.Secrets().UpdateSecretMetadata(ctx, secretstore.UpdateSecretMetadataInput{
		OrgID: org.ID, SecretID: record.ID, Name: name, Metadata: metadata,
		Actor: principal,
	})
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	response, err := newSecretResponse(updated)
	if err != nil {
		return nil, err
	}
	return openapi.UpdateSecret200JSONResponse(response), nil
}

func (s strictOpenAPIServer) CreateSecretVersion(
	ctx context.Context,
	request openapi.CreateSecretVersionRequestObject,
) (openapi.CreateSecretVersionResponseObject, error) {
	receivedAt := time.Now()
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	material, materialErr := parseSecretMaterial(request.Body.Material, receivedAt)
	if materialErr != nil {
		return nil, *materialErr
	}
	record, apiErr := canonicalSecret(ctx, s.server.store.Secrets(), org.ID, request.SecretID, principal)
	if apiErr != nil {
		return nil, *apiErr
	}
	updated, _, err := s.server.store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:    org.ID,
		SecretID: record.ID,
		Material: material,
		Actor:    principal,
	})
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	response, err := newSecretResponse(updated)
	if err != nil {
		return nil, err
	}
	return openapi.CreateSecretVersion200JSONResponse(response), nil
}

func (s strictOpenAPIServer) DeleteSecret(
	ctx context.Context,
	request openapi.DeleteSecretRequestObject,
) (openapi.DeleteSecretResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	record, apiErr := canonicalSecret(ctx, s.server.store.Secrets(), org.ID, request.SecretID, principal)
	if apiErr != nil {
		return nil, *apiErr
	}
	if _, err := s.server.store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: org.ID, SecretID: record.ID, Actor: principal,
	}); err != nil {
		return nil, secretAPIError(ctx, err)
	}
	return openapi.DeleteSecret204Response{}, nil
}

func (s strictOpenAPIServer) CreateSecretGrant(
	ctx context.Context,
	request openapi.CreateSecretGrantRequestObject,
) (openapi.CreateSecretGrantResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	record, apiErr := canonicalSecret(ctx, s.server.store.Secrets(), org.ID, request.SecretID, principal)
	if apiErr != nil {
		return nil, *apiErr
	}
	projectID, ok := parseOpenAPIPublicID(publicid.KindProject, request.Body.TargetProjectId)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid target_project_id")
	}
	grant, err := s.server.store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID: org.ID, SecretID: record.ID, TargetProjectID: projectID,
		Actor: principal,
	})
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	response, err := newSecretGrantResponse(grant)
	if err != nil {
		return nil, err
	}
	return openapi.CreateSecretGrant201JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListSecretGrants(
	ctx context.Context,
	request openapi.ListSecretGrantsRequestObject,
) (openapi.ListSecretGrantsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, request.SecretID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	scopeKey := org.ID.String() + "/" + secretID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name,
		Sort: optionalString(request.Params.Sort), Cursor: request.Params.Cursor, ListKind: "secret_grants", Scope: scopeKey,
		IDKind: publicid.KindSecretGrant, AllowedSorts: sortSet("name", "created_at"),
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Secrets().ListSecretGrants(ctx, secretstore.ListSecretGrantsInput{
		OrgID: org.ID, SecretID: secretID, Actor: principal, Limit: limit, List: list,
	})
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	data := make([]openapi.SecretGrantListItem, 0, len(page.Grants))
	for _, item := range page.Grants {
		grant, err := newSecretGrantResponse(item.Grant)
		if err != nil {
			return nil, err
		}
		project, err := projectResponse(identitystore.ProjectRecord{
			ID: item.TargetProject.ID, OrgID: item.TargetProject.OrgID, Name: item.TargetProject.Name,
			CreatedAt: item.TargetProject.CreatedAt, UpdatedAt: item.TargetProject.UpdatedAt,
		})
		if err != nil {
			return nil, err
		}
		data = append(data, openapi.SecretGrantListItem{Grant: grant, TargetProject: project})
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "secret_grants", scopeKey, publicid.KindSecretGrant, nil,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListSecretGrants200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func (s strictOpenAPIServer) DeleteSecretGrant(
	ctx context.Context,
	request openapi.DeleteSecretGrantRequestObject,
) (openapi.DeleteSecretGrantResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, *principalErr
	}
	secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, request.SecretID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	grantID, ok := parseOpenAPIPublicID(publicid.KindSecretGrant, request.GrantID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if _, err := s.server.store.Secrets().DeleteSecretGrant(ctx, secretstore.DeleteSecretGrantInput{
		OrgID: org.ID, SecretID: secretID, GrantID: grantID, Actor: principal,
	}); err != nil {
		return nil, secretAPIError(ctx, err)
	}
	return openapi.DeleteSecretGrant204Response{}, nil
}

func (s strictOpenAPIServer) ListProjectAvailableSecrets(
	ctx context.Context,
	request openapi.ListProjectAvailableSecretsRequestObject,
) (openapi.ListProjectAvailableSecretsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, principalErr
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	filters := secretListFilters(request.Params.Metadata)
	kind := ""
	if request.Params.Kind != nil {
		kind = string(*request.Params.Kind)
		filters.Kinds = []string{kind}
	}
	if request.Params.OwnerKind != nil {
		filters.OwnerKind = string(*request.Params.OwnerKind)
	}
	if request.Params.AvailabilitySource != nil {
		filters.AvailabilitySources = []string{string(*request.Params.AvailabilitySource)}
	}
	extra := struct {
		OwnerKind           string
		AvailabilitySources []string
		Kind                string
		Metadata            map[string]string
	}{
		OwnerKind:           filters.OwnerKind,
		AvailabilitySources: filters.AvailabilitySources,
		Kind:                kind,
		Metadata:            filters.Metadata,
	}
	scopeKey := scope.project.OrgID.String() + "/" + scope.project.ID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: request.Params.Name, Sort: optionalString(request.Params.Sort),
		Cursor: request.Params.Cursor, ListKind: "project_available_secrets",
		Scope: scopeKey, IDKind: publicid.KindSecret,
		AllowedSorts: defaultResourceSorts, Extra: extra,
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Secrets().ListProjectAvailableSecretsForPrincipal(
		ctx,
		secretstore.ListProjectAvailableSecretsForPrincipalInput{
			ListProjectAvailableSecretsInput: secretstore.ListProjectAvailableSecretsInput{
				OrgID: scope.project.OrgID, ProjectID: scope.project.ID,
				Filters: filters, Limit: limit, List: list,
			},
			Actor: principal,
		},
	)
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	data := make([]openapi.ProjectSecretAccess, 0, len(page.Accesses))
	for _, access := range page.Accesses {
		response, err := newProjectSecretAccessResponse(access)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "project_available_secrets", scopeKey, publicid.KindSecret, extra,
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListProjectAvailableSecrets200JSONResponse{Data: data, NextCursor: nullableFromPtr(next)}, nil
}

func (s strictOpenAPIServer) GetProjectAvailableSecret(
	ctx context.Context,
	request openapi.GetProjectAvailableSecretRequestObject,
) (openapi.GetProjectAvailableSecretResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	principal, principalErr := accountPrincipalFromContext(ctx)
	if principalErr != nil {
		return nil, principalErr
	}
	secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, request.SecretID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	access, err := s.server.store.Secrets().GetProjectAvailableSecretForPrincipal(
		ctx,
		scope.project.OrgID,
		scope.project.ID,
		secretID,
		principal,
	)
	if err != nil {
		return nil, secretAPIError(ctx, err)
	}
	response, err := newProjectSecretAccessResponse(access)
	if err != nil {
		return nil, err
	}
	return openapi.GetProjectAvailableSecret200JSONResponse(response), nil
}
