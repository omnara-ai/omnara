package tools

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestAppToolScopePreparation(t *testing.T) {
	for _, scope := range []toolcatalog.AppToolScope{toolcatalog.AppToolScopeApp, "", "unknown"} {
		t.Run(string(scope), func(t *testing.T) {
			access := appToolAccess{Authority: agentconfig.AppToolAuthority{
				Definition: toolcatalog.AppToolDefinition{Scope: scope},
			}}
			conversation, err := (Executor{}).appToolConversation(t.Context(), Turn{}, access)
			if scope != toolcatalog.AppToolScopeApp {
				require.ErrorContains(t, err, "explicit scope")
				_, err = appToolPermissionSummary(access, json.RawMessage(`{}`))
				require.ErrorContains(t, err, "explicit scope")
				return
			}
			require.NoError(t, err)
			require.Empty(t, conversation)
			summary, err := appToolPermissionSummary(access, json.RawMessage(`{"query":"onboarding"}`))
			require.NoError(t, err)
			require.JSONEq(t, `{"arguments":{"query":"onboarding"}}`, string(summary))
		})
	}
}
