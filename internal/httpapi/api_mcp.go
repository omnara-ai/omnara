package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func (s *Server) newAPIMCPServer() (*mcp.Server, error) {
	spec, err := openapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load generated openapi spec: %w", err)
	}
	return apimcp.NewServer(spec, apimcp.Tools, apimcp.Options{
		Dispatch:    http.HandlerFunc(s.dispatchAPIRequest),
		APIBasePath: openAPIBasePath,
		Grants:      s.apiMCPGrants,
	})
}

func (s *Server) dispatchAPIRequest(w http.ResponseWriter, r *http.Request) {
	handler := s.apiDispatch.Load()
	if handler == nil {
		http.Error(w, "api handler is not initialized", http.StatusServiceUnavailable)
		return
	}
	(*handler).ServeHTTP(w, r)
}

func (s *Server) apiDispatchMiddlewares(mux *http.ServeMux) []middleware {
	middlewares := make([]middleware, 0, 4)
	if s.recorder != nil {
		middlewares = append(middlewares, s.recorder.Middleware(mux))
	}
	return append(middlewares, s.requestLog, attachMCPToolCall, s.openAPIRequestValidator)
}

func attachMCPToolCall(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if call, ok := apimcp.ToolCallFromContext(r.Context()); ok {
			logpkg.Attach(r.Context(), logpkg.Fields{
				"mcp.tool":             call.Tool,
				"openapi.operation_id": call.OperationID,
			})
		}
		next.ServeHTTP(w, r)
	})
}

type apiMCPGrants struct {
	server    *Server
	principal identitystore.PrincipalRecord
	roles     *identitystore.PrincipalRoles
}

func (s *Server) apiMCPGrants(ctx context.Context) apimcp.Grants {
	grants := &apiMCPGrants{server: s}
	if principal, ok := principalFromContext(ctx); ok {
		grants.principal = principal
	}
	return grants
}

var customScopeDiscovery = map[operationID]func(*apiMCPGrants, context.Context) (bool, error){
	operationCreateProjectMachineGrant: (*apiMCPGrants).anyOrgManage,
	operationDeleteProjectMachineGrant: (*apiMCPGrants).anyOrgManage,
}

func (g *apiMCPGrants) Allows(ctx context.Context, rawOperationID string) (bool, error) {
	opID := operationID(rawOperationID)
	policy, ok := g.server.openAPIAuthorizer.policy(opID)
	if !ok || !principalSatisfies(g.principal, policy.principal) {
		return false, nil
	}
	switch policy.scope.kind {
	case scopeKindNone:
		return true, nil
	case scopeKindCustom:
		discover, ok := customScopeDiscovery[opID]
		if !ok {
			return false, nil
		}
		return discover(g, ctx)
	case scopeKindOrg:
		return g.anyOrgRoleAllows(ctx, policy.scope.action)
	case scopeKindProject, scopeKindAgent:
		if allowed, err := g.anyOrgManage(ctx); err != nil || allowed {
			return allowed, err
		}
		return g.anyProjectRoleAllows(ctx, policy.scope.action)
	case scopeKindMachine:
		if allowed, err := g.anyOrgManage(ctx); err != nil || allowed {
			return allowed, err
		}
		if policy.scope.action != executionstore.MachineActionRead {
			return false, nil
		}
		roles, err := g.loadRoles(ctx)
		if err != nil {
			return false, err
		}
		return len(roles.ProjectRoles) > 0, nil
	default:
		return false, nil
	}
}

func (g *apiMCPGrants) loadRoles(ctx context.Context) (identitystore.PrincipalRoles, error) {
	if g.roles != nil {
		return *g.roles, nil
	}
	roles := identitystore.PrincipalRoles{}
	if g.server.store != nil && identitystore.IsAccountPrincipal(g.principal) {
		loaded, err := g.server.store.Identity().ListPrincipalRoles(ctx, g.principal)
		if err != nil {
			return identitystore.PrincipalRoles{}, err
		}
		roles = loaded
	}
	g.roles = &roles
	return roles, nil
}

func (g *apiMCPGrants) anyOrgManage(ctx context.Context) (bool, error) {
	return g.anyOrgRoleAllows(ctx, authz.OrgManage)
}

func (g *apiMCPGrants) anyOrgRoleAllows(ctx context.Context, action string) (bool, error) {
	roles, err := g.loadRoles(ctx)
	if err != nil {
		return false, err
	}
	for _, role := range roles.OrgRoles {
		if authz.OrgRoleAllows(role, action) {
			return true, nil
		}
	}
	return false, nil
}

func (g *apiMCPGrants) anyProjectRoleAllows(ctx context.Context, action string) (bool, error) {
	roles, err := g.loadRoles(ctx)
	if err != nil {
		return false, err
	}
	for _, role := range roles.ProjectRoles {
		if authz.ProjectRoleAllows(role, action) {
			return true, nil
		}
	}
	return false, nil
}
