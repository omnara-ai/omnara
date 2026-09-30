package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestAgentSelectedTargetConversation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ provider, kind, ref, conversation string }{
		{"slack", "channel", "C123", `{"channel_id":"C123"}`},
		{"slack", "dm", "D123", `{"channel_id":"D123"}`},
		{"slack", "thread", "C123:111.222", `{"channel_id":"C123","thread_ts":"111.222"}`},
		{"discord", "channel", "123", `{"channel_id":"123"}`},
		{"discord", "thread", "456", `{"thread_id":"456"}`},
		{"github", "pull_request", "123#42", `{"repository_id":123,"pull_request":42}`},
	} {
		t.Run(tc.provider+"/"+tc.kind, func(t *testing.T) {
			t.Parallel()
			record := executionstore.AgentRecord{
				ID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(),
				IntegrationTarget: executionstore.IntegrationTargetDisplay{
					Provider: tc.provider, ScopeKind: tc.kind, ScopeRef: tc.ref, DisplayName: "conversation",
				},
			}
			response, err := publicAgentResponseFromRecord(record)
			require.NoError(t, err)
			require.NotNil(t, response.IntegrationTarget)
			require.JSONEq(t, tc.conversation, string(response.IntegrationTarget.Conversation))
			require.Equal(t, tc.provider, response.IntegrationTarget.Provider)
			require.Equal(t, "conversation", response.IntegrationTarget.DisplayName)
		})
	}
}

func TestIntegrationResponseOmitsUnknownProviderIdentity(t *testing.T) {
	t.Parallel()
	record := integrationstore.IntegrationRecord{
		ID: uuid.New(), ProjectID: uuid.New(), IntegrationKind: integrationdefinition.GitHubPR,
		State: integrationstore.IntegrationStateDisconnected, Settings: json.RawMessage(`{}`), ProviderConfig: json.RawMessage(`{}`),
	}
	for _, known := range []bool{false, true} {
		if known {
			record.ProviderTenantID, record.ProviderAccountRef = "123", "456"
		}
		response, err := integrationResponse(record)
		require.NoError(t, err)
		raw, err := json.Marshal(response)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		if known {
			require.Equal(t, "123", body["provider_tenant_id"])
			require.Equal(t, "456", body["provider_account_ref"])
		} else {
			require.NotContains(t, body, "provider_tenant_id")
			require.NotContains(t, body, "provider_account_ref")
		}
	}
}
