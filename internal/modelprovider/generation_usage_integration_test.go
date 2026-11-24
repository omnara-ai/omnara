//go:build integration

package modelprovider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestLookupGenerationUsageNeedsNoProjectGrant(t *testing.T) {
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	wrapper, err := secrets.NewLocalKeyWrapper(
		"test", map[string][]byte{"test": []byte("0123456789abcdef0123456789abcdef")},
	)
	require.NoError(t, err)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		DisplayName: "Usage Tester", Email: "usage@example.test",
	})
	require.NoError(t, err)
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: user.ID, Name: "Usage Org", IdempotencyKey: "usage-org",
	})
	require.NoError(t, err)
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: created.Org.ID, OwnerKind: secretstore.SecretOwnerOrg, Name: "usage-key",
		Material: secrets.GenericMaterial{Value: "test-key"}, Actor: modelProviderUserPrincipal(user.ID),
	})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/generation" || request.URL.Query().Get("id") != "gen-1" ||
			request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected lookup: %s %s", request.Method, request.URL)
		}
		_, _ = w.Write([]byte(`{"data":{"total_cost":0.01,"is_byok":false,` +
			`"native_tokens_prompt":10,"native_tokens_completion":0}}`))
	}))
	t.Cleanup(server.Close)
	resolver := Resolver{Models: store.Models(), Secrets: store.Secrets(), HTTPTransport: NewHTTPTransport(true, nil)}
	create := func(
		name string, format modelprotocol.APIFormat, variant modelprotocol.APIVariant,
	) modelstore.ModelProviderConfigRecord {
		t.Helper()
		provider, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID: created.Org.ID, Name: name, APIFormat: format, APIVariant: variant,
			BaseURL: server.URL + "/v1", CredentialSecretID: credential.ID,
		})
		require.NoError(t, err)
		return provider
	}

	openRouter := create("openrouter", modelprotocol.APIFormatOpenAIChatCompletions, modelprotocol.APIVariantOpenRouter)
	usage, err := resolver.LookupGenerationUsage(ctx, created.Org.ID, openRouter.ID, "gen-1")
	require.NoError(t, err)
	require.Equal(t, modelenvelope.ProviderReportedCostUSD("0.01"), usage.ProviderReportedCostUSD)
	require.Equal(t, model.Usage{InputTokens: 10, UncachedInputTokens: 10}, usage.Usage)

	openAI := create("openai", modelprotocol.APIFormatOpenAIChatCompletions, modelprotocol.APIVariantDefault)
	_, err = resolver.LookupGenerationUsage(ctx, created.Org.ID, openAI.ID, "gen-1")
	require.ErrorContains(t, err, "does not report generation usage")
}
