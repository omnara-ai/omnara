package tools

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestIntegrationToolScopePreparation(t *testing.T) {
	for _, scope := range []toolcatalog.IntegrationToolScope{toolcatalog.IntegrationToolScopeIntegration, "", "unknown"} {
		t.Run(string(scope), func(t *testing.T) {
			access := integrationToolAccess{Authority: agentconfig.IntegrationToolAuthority{
				Definition: toolcatalog.IntegrationToolDefinition{Scope: scope},
			}}
			conversation, err := (Executor{}).integrationToolConversation(t.Context(), Turn{}, access)
			if scope != toolcatalog.IntegrationToolScopeIntegration {
				require.ErrorContains(t, err, "explicit scope")
				_, err = integrationToolPermissionSummary(access, json.RawMessage(`{}`))
				require.ErrorContains(t, err, "explicit scope")
				return
			}
			require.NoError(t, err)
			require.Empty(t, conversation)
			summary, err := integrationToolPermissionSummary(access, json.RawMessage(`{"query":"onboarding"}`))
			require.NoError(t, err)
			require.JSONEq(t, `{"arguments":{"query":"onboarding"}}`, string(summary))
		})
	}
}
