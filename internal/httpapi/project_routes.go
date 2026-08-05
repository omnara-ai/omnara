package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func principalFromContext(ctx context.Context) (identitystore.PrincipalRecord, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(identitystore.PrincipalRecord)
	return principal, ok
}

func projectResponse(record identitystore.ProjectRecord) (openapi.Project, error) {
	id, err := publicID(publicid.KindProject, record.ID)
	if err != nil {
		return openapi.Project{}, err
	}
	orgID, err := publicID(publicid.KindOrganization, record.OrgID)
	if err != nil {
		return openapi.Project{}, err
	}
	return openapi.Project{
		Id:        id,
		OrgId:     orgID,
		Name:      record.Name,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}, nil
}

func visibleProjectResponse(record identitystore.VisibleProjectRecord) (openapi.VisibleProject, error) {
	project, err := projectResponse(record.Project)
	if err != nil {
		return openapi.VisibleProject{}, err
	}
	return openapi.VisibleProject{
		Id:        project.Id,
		OrgId:     project.OrgId,
		Name:      project.Name,
		CreatedAt: project.CreatedAt,
		UpdatedAt: project.UpdatedAt,
		Access: openapi.ProjectAccess{
			CanRead:         identitystore.ProjectRolesAllow(record.Roles, identitystore.ProjectActionRead),
			CanManage:       identitystore.ProjectRolesAllow(record.Roles, identitystore.ProjectActionManage),
			CanManageAccess: identitystore.ProjectRolesAllow(record.Roles, identitystore.ProjectActionAccessManage),
			CanOperate:      identitystore.ProjectRolesAllow(record.Roles, identitystore.AgentActionOperate),
		},
	}, nil
}

func (s strictOpenAPIServer) CreateProject(
	ctx context.Context,
	request openapi.CreateProjectRequestObject,
) (openapi.CreateProjectResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createProject(ctx, org, request.Params, *request.Body)
}

func (s strictOpenAPIServer) DeleteProject(
	ctx context.Context,
	request openapi.DeleteProjectRequestObject,
) (openapi.DeleteProjectResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	projectID, ok := parseOpenAPIPublicID(publicid.KindProject, request.ProjectID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	project, err := s.server.store.Identity().GetProject(ctx, org.ID, projectID)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	logent.Project(ctx, project)
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	machines, err := s.server.store.Organizations().DeleteProject(ctx, project.OrgID, project.ID, principal)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	s.server.startPoolMachineDeletion(ctx, machines)
	return openapi.DeleteProject204Response{}, nil
}

func (s strictOpenAPIServer) createProject(
	ctx context.Context,
	org identitystore.OrgRecord,
	params openapi.CreateProjectParams,
	body openapi.CreateProjectJSONRequestBody,
) (openapi.CreateProjectResponseObject, error) {
	principal, _ := principalFromContext(ctx)
	idempotencyKey := ""
	if params.IdempotencyKey != nil {
		idempotencyKey = *params.IdempotencyKey
	}
	project, err := s.server.store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID:          org.ID,
		Creator:        principal,
		Name:           body.Name,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	logent.Project(ctx, project)
	response, err := projectResponse(project)
	if err != nil {
		return nil, err
	}
	if project.Created {
		return openapi.CreateProject201JSONResponse(response), nil
	}
	return openapi.CreateProject200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListVisibleProjects(
	ctx context.Context,
	request openapi.ListVisibleProjectsRequestObject,
) (openapi.ListVisibleProjectsResponseObject, error) {
	org, err := orgScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listVisibleProjects(ctx, request.Params, org)
}

func (s strictOpenAPIServer) listVisibleProjects(
	ctx context.Context,
	params openapi.ListVisibleProjectsParams,
	org identitystore.OrgRecord,
) (openapi.ListVisibleProjectsResponseObject, error) {
	principal, _ := principalFromContext(ctx)
	limit, after, err := parseOpenAPIPageParams(params.Limit, params.Cursor, publicid.KindProject)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Identity().ListVisibleProjectsForPrincipal(
		ctx,
		identitystore.ListVisibleProjectsForPrincipalInput{
			OrgID:     org.ID,
			Principal: principal,
			Limit:     limit,
			After:     after,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	out := make([]openapi.VisibleProject, 0, len(page.Projects))
	var last identitystore.VisibleProjectRecord
	for _, record := range page.Projects {
		response, err := visibleProjectResponse(record)
		if err != nil {
			return nil, err
		}
		out = append(out, response)
		last = record
	}
	nextCursor, err := encodeNextCursor(page.HasMore, last.Project.CreatedAt, publicid.KindProject, last.Project.ID)
	if err != nil {
		return nil, err
	}
	return openapi.ListVisibleProjects200JSONResponse(openapi.ListProjectsResponse{
		Data:       out,
		NextCursor: nullableFromPtr(nextCursor),
	}), nil
}
