package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestBuildOpenAPIAuthorizerRequiresExactOperationCoverage(t *testing.T) {
	policies := map[operationID]operationPolicy{
		"One": userPolicy(noScope()),
		"Two": userPolicy(noScope()),
	}
	if _, err := buildOpenAPIAuthorizer([]operationID{"One", "Two"}, policies); err != nil {
		t.Fatalf("complete policy set should build: %v", err)
	}
	if _, err := buildOpenAPIAuthorizer([]operationID{"One", "Two", "Three"}, policies); err == nil {
		t.Fatal("missing operation should fail")
	}
	if _, err := buildOpenAPIAuthorizer([]operationID{"One"}, policies); err == nil {
		t.Fatal("unknown policy should fail")
	}
}

func TestOpenAPIOperationPoliciesCoverEveryGeneratedOperation(t *testing.T) {
	if _, err := newOpenAPIAuthorizer(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteProjectRequiresOrganizationManage(t *testing.T) {
	policy := openAPIOperationPolicies[operationDeleteProject]
	if policy.principal != principalKindAccount {
		t.Fatalf("delete project principal = %v, want account", policy.principal)
	}
	if policy.scope.kind != scopeKindOrg || policy.scope.action != identitystore.OrgActionManage {
		t.Fatalf("delete project scope = %+v, want organization manage", policy.scope)
	}
}

func TestDeleteOrganizationRequiresOrganizationOwnership(t *testing.T) {
	policy := openAPIOperationPolicies[operationDeleteOrganization]
	if policy.principal != principalKindAccount {
		t.Fatalf("delete organization principal = %v, want account", policy.principal)
	}
	if policy.scope.kind != scopeKindOrg || policy.scope.action != identitystore.OrgActionOwn {
		t.Fatalf("delete organization scope = %+v, want organization own", policy.scope)
	}
}

func TestAuthorizeOperationPrincipalAllowsPublicWithoutPrincipal(t *testing.T) {
	if err := authorizeOperationPrincipal(context.Background(), principalKindPublic); err != nil {
		t.Fatalf("public operation without principal should be allowed: %v", err)
	}
	if err := authorizeOperationPrincipal(context.Background(), principalKindUser); err == nil {
		t.Fatal("user operation without principal should be forbidden")
	}
	ctx := context.WithValue(
		context.Background(),
		principalContextKey{},
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser},
	)
	if err := authorizeOperationPrincipal(ctx, principalKindUser); err != nil {
		t.Fatalf("user operation with user principal should be allowed: %v", err)
	}
}

func TestScopedOperationsDeclareAccountCompatiblePrincipal(t *testing.T) {
	for name, policy := range openAPIOperationPolicies {
		switch policy.scope.kind {
		case scopeKindOrg, scopeKindProject, scopeKindAgent, scopeKindMachine:
			if policy.principal != principalKindUser &&
				policy.principal != principalKindAccount &&
				policy.principal != principalKindBrowserSession {
				t.Errorf(
					"operation %s is resource-scoped but does not declare an account-compatible principal; the scope helper only authorizes account principals",
					name,
				)
			}
		case scopeKindNone, scopeKindCustom:
		}
	}
}

func TestOpenAPIDocumentedSecuritySchemesMatchAuthorizationPolicies(t *testing.T) {
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatalf("load generated openapi spec: %v", err)
	}
	for _, item := range spec.Paths.Map() {
		for _, operation := range item.Operations() {
			if operation == nil || operation.OperationID == "" {
				continue
			}
			policyName := operationID(strictOperationName(operation.OperationID))
			policy, ok := openAPIOperationPolicies[policyName]
			if !ok {
				t.Errorf("operation %s (%s) has no authorization policy", operation.OperationID, policyName)
				continue
			}
			security := spec.Security
			if operation.Security != nil {
				security = *operation.Security
			}
			switch {
			case securityAllowsOnly(security, "browserSessionCookie", "csrfHeader"):
				if policy.principal != principalKindBrowserSession {
					t.Errorf(
						"operation %s documents browser-session-only security but principal = %v, want principalKindBrowserSession",
						operation.OperationID,
						policy.principal,
					)
				}
			case securityAllowsOnly(security, "machineDaemonAuth"):
				if policy.principal != principalKindMachineDaemon {
					t.Errorf(
						"operation %s documents machine-daemon-only security but principal = %v, want principalKindMachineDaemon",
						operation.OperationID,
						policy.principal,
					)
				}
			}
		}
	}
}

func strictOperationName(operationID string) string {
	if operationID == "" {
		return ""
	}
	return strings.ToUpper(operationID[:1]) + operationID[1:]
}

func securityAllowsOnly(requirements openapi3.SecurityRequirements, names ...string) bool {
	if len(requirements) != 1 {
		return false
	}
	requirement := requirements[0]
	if len(requirement) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := requirement[name]; !ok {
			return false
		}
	}
	return true
}
