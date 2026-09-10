//go:build integration

package modelprovider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/stretchr/testify/require"
)

func TestResolverOutputAllowancePrecedenceAndWire(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	wrapper, err := secrets.NewLocalKeyWrapper(
		"test", map[string][]byte{"test": []byte("0123456789abcdef0123456789abcdef")},
	)
	require.NoError(t, err)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		DisplayName: "Allowance Tester", Email: "allowance@example.test",
	})
	require.NoError(t, err)
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Allowance Org", IdempotencyKey: "allowance-org",
	})
	require.NoError(t, err)
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: created.Org.ID, OwnerKind: secretstore.SecretOwnerOrg, Name: "allowance-key",
		Material: secrets.GenericMaterial{Value: "test-key"}, Actor: modelProviderUserPrincipal(user.ID),
	})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected provider request: %s %s", request.Method, request.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name                                               string
		format                                             modelprotocol.APIFormat
		variant                                            modelprotocol.APIVariant
		capacity, allowance, grantMax, grantDefault, agent *int
		contextWindow, want                                int
	}{
		{name: "messages-fallback", want: 64000},
		{name: "messages-context-fit", contextWindow: 32000, want: 64000},
		{name: "configured-default", allowance: new(12000), capacity: new(80000), want: 12000},
		{name: "configured-ceiling", capacity: new(80000), want: 80000},
		{name: "project-default", grantDefault: new(10000), want: 10000},
		{name: "project-ceiling", grantMax: new(22000), want: 22000},
		{name: "agent-default", grantDefault: new(10000), agent: new(11000), want: 11000},
		{name: "bedrock-fallback", variant: modelprotocol.APIVariantBedrock, want: 64000},
		{name: "chat-omission", format: modelprotocol.APIFormatOpenAIChatCompletions},
		{name: "responses-omission", format: modelprotocol.APIFormatOpenAIResponses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.format == "" {
				tc.format = modelprotocol.APIFormatAnthropicMessages
			}
			if tc.contextWindow == 0 {
				tc.contextWindow = 200000
			}
			provider, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
				OrgID: created.Org.ID, Name: tc.name, APIFormat: tc.format, APIVariant: tc.variant,
				BaseURL: server.URL + "/v1", CredentialSecretID: credential.ID,
			})
			require.NoError(t, err)
			configured, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
				OrgID: created.Org.ID, ModelProviderConfigID: provider.ID, Name: tc.name, ProviderModelSlug: tc.name,
				ContextWindowTokens: tc.contextWindow, MaxOutputTokens: tc.capacity, DefaultMaxOutputTokens: tc.allowance,
			})
			require.NoError(t, err)
			_, err = store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
				OrgID: created.Org.ID, ProjectID: created.Project.ID, ConfiguredModelID: configured.ID,
				MaxOutputTokens: tc.grantMax, DefaultMaxOutputTokens: tc.grantDefault,
			})
			require.NoError(t, err)
			resolver := Resolver{Models: store.Models(), Secrets: store.Secrets(), AllowLoopback: true}
			selection := model.Selection{
				OrgID: created.Org.ID.String(), ProjectID: created.Project.ID.String(),
				ConfiguredModelRevisionID: configured.CurrentRevisionID.String(),
				Overrides:                 agentconfig.ModelOverrides{DefaultMaxOutputTokens: tc.agent},
			}
			resolved, err := resolver.Resolve(ctx, selection)
			require.NoError(t, err)
			caps := resolved.Client.Capabilities()
			prepared, err := model.PrepareForSend(ctx, resolved.Client, model.PrepareForSendInput{
				Context: modelcontext.Bundle{SystemPrompt: "You are a concise assistant.", Messages: []modelcontext.Message{{
					ID: "input", Sequence: 1, Role: modelprotocol.RoleUser,
					Content: json.RawMessage(`[{"type":"text","text":"hello"}]`),
				}}},
				Policy: model.RequestPolicyFromCapabilities(caps), ErrorSource: "test",
			})
			require.NoError(t, err)
			require.True(t, prepared.InputBudget.Fits())
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(prepared.Body, &wire))
			if tc.want == 0 {
				for _, field := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens"} {
					require.NotContains(t, wire, field)
				}
			} else {
				remaining := caps.ContextWindowTokens -
					modelcontext.DefaultSafetyMarginTokens(caps.ContextWindowTokens) - prepared.InputTokenEstimate
				want := min(tc.want, remaining)
				var allowance int
				require.NoError(t, json.Unmarshal(wire["max_tokens"], &allowance))
				require.InDelta(t, want, allowance, 1)
				require.Equal(t, prepared.MaxOutputTokens, allowance)
			}
			stored, err := store.Models().GetConfiguredModelByName(ctx, created.Org.ID, provider.ID, tc.name)
			require.NoError(t, err)
			require.Equal(t, configured.CurrentRevisionID, stored.CurrentRevisionID)
			require.Equal(t, tc.capacity, stored.MaxOutputTokens)
			require.Equal(t, tc.allowance, stored.DefaultMaxOutputTokens)
		})
	}
}
