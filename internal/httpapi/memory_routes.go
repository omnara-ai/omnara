package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
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

func memoryStoreScope(ctx context.Context, publicID string) (memorystore.Scope, uuid.UUID, error) {
	scope, err := memoryScope(ctx)
	if err != nil {
		return scope, uuid.Nil, err
	}
	id, ok := parseOpenAPIPublicID(publicid.KindMemoryStore, publicID)
	if !ok {
		return scope, uuid.Nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	return scope, id, nil
}

func memoryStoreResponse(r memorystore.Record) (openapi.MemoryStore, error) {
	id, err := publicID(publicid.KindMemoryStore, r.ID)
	if err != nil {
		return openapi.MemoryStore{}, err
	}
	projectID, err := publicID(publicid.KindProject, r.ProjectID)
	return openapi.MemoryStore{
		Id:          id,
		ProjectId:   projectID,
		Name:        r.Name,
		Description: r.Description,
		AgentAccess: openapi.MemoryStoreAccess(r.AgentAccess),
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
	agentAccess := agentconfig.MemoryStoreAccessReadWrite
	if req.Body.AgentAccess != nil {
		agentAccess = agentconfig.MemoryStoreAccess(*req.Body.AgentAccess)
	}
	r, err := s.server.store.Memories().Create(ctx, scope, req.Body.Name, description, agentAccess)
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
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
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
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "body is required")
	}
	r, err := s.server.store.Memories().Update(
		ctx, scope, id, req.Body.Description, (*agentconfig.MemoryStoreAccess)(req.Body.AgentAccess),
	)
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
	scope, id, err := memoryStoreScope(ctx, req.MemoryStoreID)
	if err != nil {
		return nil, err
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
	out, err := listMemoryStores(
		req.Params, scope.OrgID.String()+"/"+scope.ProjectID.String(),
		func(list listing.Options, limit int) (memorystore.ListResult, error) {
			return s.server.store.Memories().List(ctx, scope, list, limit)
		},
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListMemoryStores200JSONResponse(out), nil
}

func (s strictOpenAPIServer) ListOrgMemoryStores(
	ctx context.Context,
	req openapi.ListOrgMemoryStoresRequestObject,
) (openapi.ListOrgMemoryStoresResponseObject, error) {
	scope, err := s.orgListScope(ctx, identitystore.ProjectActionRead)
	if err != nil {
		return nil, err
	}
	out, err := listMemoryStores(
		openapi.ListMemoryStoresParams(req.Params), scope.key,
		func(list listing.Options, limit int) (memorystore.ListResult, error) {
			return s.server.store.Memories().ListForProjects(ctx, scope.projectIDs, list, limit)
		},
	)
	if err != nil {
		return nil, err
	}
	return openapi.ListOrgMemoryStores200JSONResponse(out), nil
}

func listMemoryStores(
	params openapi.ListMemoryStoresParams,
	listScope string,
	listPage func(listing.Options, int) (memorystore.ListResult, error),
) (openapi.MemoryStoreList, error) {
	limit, err := parseOpenAPIPageLimit(params.Limit)
	if err != nil {
		return openapi.MemoryStoreList{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	sort := "name"
	list, err := parseResourceListQuery(resourceListQueryInput{
		Name: params.Name, Sort: &sort, Cursor: params.Cursor, ListKind: "memory_stores",
		Scope: listScope, IDKind: publicid.KindMemoryStore, AllowedSorts: sortSet("name"),
	})
	if err != nil {
		return openapi.MemoryStoreList{}, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := listPage(list, limit)
	if err != nil {
		return openapi.MemoryStoreList{}, apierror.ProjectScoped(err)
	}
	next, err := encodeResourceListNextCursor(
		page.HasMore, page.Next, list, "memory_stores", listScope, publicid.KindMemoryStore, nil,
	)
	if err != nil {
		return openapi.MemoryStoreList{}, err
	}
	out := openapi.MemoryStoreList{Data: []openapi.MemoryStore{}, NextCursor: nullableFromPtr(next)}
	for _, r := range page.Records {
		item, e := memoryStoreResponse(r)
		if e != nil {
			return openapi.MemoryStoreList{}, e
		}
		out.Data = append(out.Data, item)
	}
	return out, nil
}
