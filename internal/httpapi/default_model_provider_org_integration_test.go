//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func TestCreateOrganizationQueuesDefaultProviderAfterCommit(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	handler := newIntegrationServer(pool, WithDefaultModelProvider(&template))
	store := integrationStoreForHandler(t, handler)
	user, token := createOrgRouteUser(t, pool, store, "postcommit-default-provider")

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Postcommit Provider Org"}`,
		"postcommit-provider-org",
		http.StatusCreated,
		authHeaders(token),
	)
	publicOrgID := created["org"].(map[string]any)["id"].(string)
	orgID, err := publicid.Decode(publicid.KindOrganization, publicOrgID)
	if err != nil {
		t.Fatalf("decode organization id: %v", err)
	}
	if _, err := store.Models().GetModelProviderConfigByName(ctx, orgID, template.Name); !storeerr.IsNotFound(err) {
		t.Fatalf("provider before post-commit provisioning error = %v, want not found", err)
	}

	claim, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx)
	if err != nil || !found {
		t.Fatalf("claim default provider provisioning: found=%t err=%v", found, err)
	}
	if claim.OrgID != orgID || claim.CreatorUserID != user.ID || claim.Attempt != 1 {
		t.Fatalf("unexpected provisioning claim: %+v", claim)
	}
	if err := store.Organizations().CompleteDefaultModelProviderProvisioning(
		ctx,
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			Claim:           claim,
			CredentialValue: "sk-cluster-openrouter",
		},
	); err != nil {
		t.Fatalf("complete default provider provisioning: %v", err)
	}
	provider, err := store.Models().GetModelProviderConfigByName(ctx, orgID, template.Name)
	if err != nil {
		t.Fatalf("get provisioned model provider: %v", err)
	}
	credential, err := store.Secrets().GetSecret(ctx, orgID, provider.CredentialSecretID)
	if err != nil {
		t.Fatalf("get provisioned credential: %v", err)
	}
	if provider.ManagementKind != management.Cluster || credential.ManagementKind != management.Cluster ||
		credential.OwnerKind != secretstore.SecretOwnerOrg ||
		!strings.HasPrefix(credential.Name, template.CredentialSecretName+"-") {
		t.Fatalf("unexpected provisioned resources: provider=%+v credential=%+v", provider, credential)
	}
	payload, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID: orgID, SecretID: credential.ID, ManagementKind: management.Cluster, Kind: secretstore.SecretKindGeneric,
	})
	if err != nil || payload.Payload[secrets.KeyValue] != "sk-cluster-openrouter" {
		t.Fatalf("credential payload = %+v err=%v", payload.Payload, err)
	}

	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Postcommit Provider Org"}`,
		"postcommit-provider-org",
		http.StatusOK,
		authHeaders(token),
	)
	if replayed["org"].(map[string]any)["id"] != publicOrgID {
		t.Fatalf("replayed organization = %+v, want %s", replayed["org"], publicOrgID)
	}
	if _, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx); err != nil || found {
		t.Fatalf("claim after completed replay: found=%t err=%v", found, err)
	}
}

func TestCreateOrganizationWithoutDefaultProviderDoesNotQueueProvisioning(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	_, token := createOrgRouteUser(t, pool, store, "no-default-provider")

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"No Default Provider Org"}`,
		"no-default-provider-org",
		http.StatusCreated,
		authHeaders(token),
	)
	if _, found, err := store.Organizations().ClaimDefaultModelProviderProvisioning(ctx); err != nil || found {
		t.Fatalf("claim without configured default provider: found=%t err=%v", found, err)
	}
}

func testDefaultOpenRouterTemplate() modelstore.DefaultModelProviderTemplate {
	template, err := modelstore.PrepareDefaultModelProviderTemplate(modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "omnara-openrouter",
		CredentialSecretName: "omnara-openrouter-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://openrouter.ai/api/v1",
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name:                "claude-sonnet-4.5",
			ProviderModelSlug:   "anthropic/claude-sonnet-4.5",
			ContextWindowTokens: 200000,
			MaxOutputTokens:     64000,
			SupportsReasoning:   true,
			InputModalities:     []string{"text", "image"},
			OutputModalities:    []string{"text"},
		}},
	})
	if err != nil {
		panic(err)
	}
	return template
}

func createOrgRouteUser(
	t *testing.T,
	pool *pgxpool.Pool,
	store *storage.Store,
	seed string,
) (identitystore.UserRecord, string) {
	t.Helper()
	user, err := storagetest.CreateVerifiedUser(context.Background(), pool, storagetest.CreateVerifiedUserInput{
		Email: seed + "@example.com", DisplayName: "Default Provider Owner",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		context.Background(),
		identitystore.CreatePersonalAccessTokenInput{UserID: user.ID, Name: "org creation"},
	)
	if err != nil {
		t.Fatalf("create personal access token: %v", err)
	}
	return user, pat.Token
}
