package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func personalAccessTokenResponse(record identitystore.PersonalAccessTokenRecord) (openapi.PersonalAccessToken, error) {
	id, err := publicID(publicid.KindPersonalAccessToken, record.ID)
	if err != nil {
		return openapi.PersonalAccessToken{}, err
	}
	userID, err := publicID(publicid.KindUser, record.UserID)
	if err != nil {
		return openapi.PersonalAccessToken{}, err
	}
	return openapi.PersonalAccessToken{
		Id:         id,
		UserId:     userID,
		Name:       record.Name,
		TokenId:    record.TokenID,
		CreatedAt:  record.CreatedAt,
		LastUsedAt: nullableFromPtr(record.LastUsedAt),
		RevokedAt:  nullableFromPtr(record.RevokedAt),
	}, nil
}

func (s strictOpenAPIServer) CreatePersonalAccessToken(
	ctx context.Context,
	request openapi.CreatePersonalAccessTokenRequestObject,
) (openapi.CreatePersonalAccessTokenResponseObject, error) {
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID ||
		principal.BrowserSessionID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	created, err := s.server.store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID:         principal.ID,
			ActorPrincipal: principal,
			Name:           request.Body.Name,
		},
	)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	record, err := personalAccessTokenResponse(created.Record)
	if err != nil {
		return nil, err
	}
	response := openapi.CreatePersonalAccessTokenResponse{Token: created.Token, TokenRecord: record}
	return openapi.CreatePersonalAccessToken201JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListPersonalAccessTokens(
	ctx context.Context,
	request openapi.ListPersonalAccessTokensRequestObject,
) (openapi.ListPersonalAccessTokensResponseObject, error) {
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	limit, after, err := parseOpenAPIPageParams(
		request.Params.Limit,
		request.Params.Cursor,
		publicid.KindPersonalAccessToken,
	)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Identity().ListPersonalAccessTokensForUser(
		ctx,
		identitystore.ListPersonalAccessTokensInput{
			UserID: principal.ID,
			Limit:  limit,
			After:  after,
		},
	)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	data := make([]openapi.PersonalAccessToken, 0, len(page.Tokens))
	var last identitystore.PersonalAccessTokenRecord
	for _, token := range page.Tokens {
		response, err := personalAccessTokenResponse(token)
		if err != nil {
			return nil, err
		}
		data = append(data, response)
		last = token
	}
	nextCursor, err := encodeNextCursor(page.HasMore, last.CreatedAt, publicid.KindPersonalAccessToken, last.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListPersonalAccessTokens200JSONResponse(openapi.ListPersonalAccessTokensResponse{
		Data:       data,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}

func (s strictOpenAPIServer) RevokePersonalAccessToken(
	ctx context.Context,
	request openapi.RevokePersonalAccessTokenRequestObject,
) (openapi.RevokePersonalAccessTokenResponseObject, error) {
	if s.server.store == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	tokenID, ok := parseOpenAPIPublicID(publicid.KindPersonalAccessToken, request.TokenID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	revoked, err := s.server.store.Identity().RevokePersonalAccessToken(ctx, principal.ID, tokenID)
	if err != nil {
		return nil, apierror.UserScoped(err)
	}
	response, err := personalAccessTokenResponse(revoked)
	if err != nil {
		return nil, err
	}
	return openapi.RevokePersonalAccessToken200JSONResponse(response), nil
}
