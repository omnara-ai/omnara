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
	for _, tc := range []struct {
		provider                integrationdefinition.Provider
		kind, ref, conversation string
	}{
		{integrationdefinition.ProviderSlack, "channel", "C123", `{"channel_id":"C123"}`},
		{integrationdefinition.ProviderSlack, "dm", "D123", `{"channel_id":"D123"}`},
		{integrationdefinition.ProviderSlack, "thread", "C123:111.222", `{"channel_id":"C123","thread_ts":"111.222"}`},
		{integrationdefinition.ProviderDiscord, "channel", "123", `{"channel_id":"123"}`},
		{integrationdefinition.ProviderDiscord, "thread", "456", `{"thread_id":"456"}`},
		{integrationdefinition.ProviderGitHub, "pull_request", "123#42", `{"repository_id":123,"pull_request":42}`},
	} {
		t.Run(string(tc.provider)+"/"+tc.kind, func(t *testing.T) {
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
			require.Equal(t, string(tc.provider), response.IntegrationTarget.Provider)
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

func TestIntegrationResponseBotMention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		kind     integrationdefinition.Kind
		provider integrationdefinition.Provider
		identity string
		mention  string
	}{
		{"verified GitHub App", integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub,
			`{"app_slug":"review-helper","bot_login":"review-helper[bot]"}`, "@review-helper"},
		{"missing identity", integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub, "", ""},
		{"empty identity", integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub, `{}`, ""},
		{"missing slug", integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub,
			`{"bot_login":"review-helper[bot]"}`, ""},
		{"empty slug", integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub, `{"app_slug":""}`, ""},
		{"invalid identity", integrationdefinition.GitHubPR, integrationdefinition.ProviderGitHub, `{"app_slug":123}`, ""},
		{"other provider", integrationdefinition.SlackThread, integrationdefinition.ProviderSlack,
			`{"app_slug":"review-helper"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, state := range []integrationstore.IntegrationState{
				integrationstore.IntegrationStateActive, integrationstore.IntegrationStateDisconnected,
			} {
				t.Run(string(state), func(t *testing.T) {
					record := integrationstore.IntegrationRecord{
						ID: uuid.New(), ProjectID: uuid.New(), Name: "local-integration-name",
						IntegrationKind: tc.kind, Provider: tc.provider, State: state,
						ProviderAgentDisplayName: "Display label",
						ProviderIdentity:         json.RawMessage(tc.identity),
						Settings:                 json.RawMessage(`{}`), ProviderConfig: json.RawMessage(`{}`),
					}
					response, err := integrationResponse(record)
					require.NoError(t, err)
					raw, err := json.Marshal(response)
					require.NoError(t, err)
					var body map[string]any
					require.NoError(t, json.Unmarshal(raw, &body))
					if tc.mention == "" {
						require.NotContains(t, body, "bot_mention")
					} else {
						require.Equal(t, tc.mention, body["bot_mention"])
					}
				})
			}
		})
	}
}
