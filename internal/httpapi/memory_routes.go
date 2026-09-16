package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
)

func memoryScope(ctx context.Context) (memorystore.Scope, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return memorystore.Scope{}, err
	}
	principal, e := accountPrincipalFromContext(ctx)
	if e != nil {
		return memorystore.Scope{}, *e
	}
	return memorystore.Scope{OrgID: scope.org.ID, ProjectID: scope.project.ID, Principal: principal}, nil
}

func memoryStoreResponse(r memorystore.Record) (openapi.MemoryStore, error) {
	id, err := publicID(publicid.KindMemoryStore, r.ID)
	return openapi.MemoryStore{
		Id:          id,
		Name:        r.Name,
		Description: r.Description,
		ReadOnly:    r.ReadOnly,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}, err
}

func (s strictOpenAPIServer) CreateMemoryStore(
	ctx context.Context,
	req openapi.CreateMemoryStoreRequestObject,
) (openapi.CreateMemoryStoreResponseObject, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "body is required")
	}
	description := ""
	if req.Body.Description != nil {
		description = *req.Body.Description
	}
	readOnly := req.Body.ReadOnly != nil && *req.Body.ReadOnly
	r, err := s.server.store.Memories().Create(ctx, scope, req.Body.Name, description, readOnly)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out, err := memoryStoreResponse(r)
	return openapi.CreateMemoryStore201JSONResponse(out), err
}

func (s strictOpenAPIServer) GetMemoryStore(
	ctx context.Context,
	req openapi.GetMemoryStoreRequestObject,
) (openapi.GetMemoryStoreResponseObject, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindMemoryStore, req.MemoryStoreID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	r, err := s.server.store.Memories().Get(ctx, scope, id)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out, err := memoryStoreResponse(r)
	return openapi.GetMemoryStore200JSONResponse(out), err
}

func (s strictOpenAPIServer) UpdateMemoryStore(
	ctx context.Context,
	req openapi.UpdateMemoryStoreRequestObject,
) (openapi.UpdateMemoryStoreResponseObject, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindMemoryStore, req.MemoryStoreID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if req.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "body is required")
	}
	r, err := s.server.store.Memories().Update(ctx, scope, id, req.Body.Description, req.Body.ReadOnly)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out, err := memoryStoreResponse(r)
	return openapi.UpdateMemoryStore200JSONResponse(out), err
}

func (s strictOpenAPIServer) DeleteMemoryStore(
	ctx context.Context,
	req openapi.DeleteMemoryStoreRequestObject,
) (openapi.DeleteMemoryStoreResponseObject, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindMemoryStore, req.MemoryStoreID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if err = s.server.store.Memories().Delete(ctx, scope, id); err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	return openapi.DeleteMemoryStore204Response{}, nil
}

func (s strictOpenAPIServer) ListMemoryStores(
	ctx context.Context,
	req openapi.ListMemoryStoresRequestObject,
) (openapi.ListMemoryStoresResponseObject, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(req.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	sort := "name"
	listScope := scope.OrgID.String() + "/" + scope.ProjectID.String()
	list, err := parseResourceListQuery(resourceListQueryInput{
		Sort: &sort, Cursor: req.Params.Cursor, ListKind: "memory_stores",
		Scope: listScope, IDKind: publicid.KindMemoryStore, AllowedSorts: sortSet("name"),
	})
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Memories().List(ctx, scope, list.After.Key, limit)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out := openapi.MemoryStoreList{Data: []openapi.MemoryStore{}, HasMore: page.HasMore}
	out.NextCursor, err = encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "memory_stores", listScope, publicid.KindMemoryStore, nil,
	)
	if err != nil {
		return nil, err
	}
	for _, r := range page.Records {
		item, e := memoryStoreResponse(r)
		if e != nil {
			return nil, e
		}
		out.Data = append(out.Data, item)
	}
	return openapi.ListMemoryStores200JSONResponse(out), nil
}
