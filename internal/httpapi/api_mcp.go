package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
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
		Logger:      s.log,
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

type apiMCPGrants struct {
	principal  identitystore.PrincipalRecord
	roles      identitystore.PrincipalRoles
	authorizer operationAuthorizer
}

func (s *Server) apiMCPGrants(ctx context.Context) (apimcp.Grants, error) {
	principal, ok := principalFromContext(ctx)
	if !ok {
		return apiMCPGrants{authorizer: s.openAPIAuthorizer}, nil
	}
	grants := apiMCPGrants{principal: principal, authorizer: s.openAPIAuthorizer}
	if s.store == nil || !identitystore.IsAccountPrincipal(principal) {
		return grants, nil
	}
	roles, err := s.store.Identity().ListPrincipalRoles(ctx, principal)
	if err != nil {
		return nil, err
	}
	grants.roles = roles
	return grants, nil
}

func (g apiMCPGrants) Allows(opID string) bool {
	policy, ok := g.authorizer.policy(operationID(opID))
	if !ok {
		return false
	}
	if !principalSatisfies(g.principal, policy.principal) {
		return false
	}
	switch policy.scope.kind {
	case scopeKindNone, scopeKindCustom:
		return true
	case scopeKindOrg:
		return g.anyOrgRoleAllows(policy.scope.action)
	case scopeKindProject, scopeKindAgent:
		return g.anyProjectRoleAllows(policy.scope.action)
	case scopeKindMachine:
		return g.anyOrgRoleAllows(authz.OrgManage) ||
			(len(g.roles.ProjectRoles) > 0 && authz.MachineRoleAllows(policy.scope.action))
	default:
		return false
	}
}

func (g apiMCPGrants) anyOrgRoleAllows(action string) bool {
	for _, role := range g.roles.OrgRoles {
		if authz.OrgRoleAllows(role, action) {
			return true
		}
	}
	return false
}

func (g apiMCPGrants) anyProjectRoleAllows(action string) bool {
	for _, role := range g.roles.ProjectRoles {
		if authz.ProjectRoleAllows(role, action) {
			return true
		}
	}
	return false
}
