package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/stretchr/testify/require"
)

func TestChannelConnectorBoundariesWithMCPOAuth(t *testing.T) {
	t.Parallel()
	spec, err := openapi.GetSpec()
	require.NoError(t, err)
	server := mustNewUnitServer(t)
	oauth := identitystore.NewOAuthAccessTokenPrincipal(uuid.New(), uuid.New())
	connector := identitystore.NewChannelConnectorPrincipal("gateway-test", nil)
	oauthContext := context.WithValue(t.Context(), principalContextKey{}, oauth)
	connectorContext := context.WithValue(t.Context(), principalContextKey{}, connector)
	missingConnectorIdentity := context.WithValue(
		t.Context(), principalContextKey{}, identitystore.NewChannelConnectorPrincipal("", nil),
	)
	oauthGrants := server.apiMCPGrants(oauthContext)
	connectorGrants := server.apiMCPGrants(connectorContext)

	// OAuth retains public user access; connector credentials do not acquire it.
	allowed, err := oauthGrants.Allows(t.Context(), string(operationGetCurrentUser))
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = connectorGrants.Allows(t.Context(), string(operationGetCurrentUser))
	require.NoError(t, err)
	require.False(t, allowed)

	manifest := make(map[string]struct{}, len(apimcp.Tools))
	for _, tool := range apimcp.Tools {
		manifest[strings.ToLower(tool.OperationID)] = struct{}{}
	}
	privateOperations := 0
	for path, item := range spec.Paths.Map() {
		if !strings.HasPrefix(path, "/channel-connector/") {
			continue
		}
		for _, operation := range item.Operations() {
			privateOperations++
			t.Run(operation.OperationID, func(t *testing.T) {
				t.Parallel()
				require.Equal(t, true, operation.Extensions["x-hidden"])
				require.NotNil(t, operation.Security)
				require.True(t, securityAllowsOnly(*operation.Security, "channelConnectorAuth"))
				require.NotContains(t, manifest, strings.ToLower(operation.OperationID))

				opID := operationID(strictOperationName(operation.OperationID))
				policy, ok := server.openAPIAuthorizer.policy(opID)
				require.True(t, ok)
				require.Equal(t, principalKindChannelConnector, policy.principal)
				require.NoError(t, authorizeOperationPrincipal(connectorContext, policy.principal))
				require.Error(t, authorizeOperationPrincipal(missingConnectorIdentity, policy.principal))
				require.Error(t, authorizeOperationPrincipal(oauthContext, policy.principal))
				require.Error(t, authorizeOperationPrincipal(t.Context(), policy.principal))

				// Neither principal can discover or invoke a private operation via MCP.
				for _, grants := range []apimcp.Grants{oauthGrants, connectorGrants} {
					allowed, err := grants.Allows(t.Context(), string(opID))
					require.NoError(t, err)
					require.False(t, allowed)
				}
			})
		}
	}
	require.Positive(t, privateOperations, "generated spec must include the private connector routes")
}
