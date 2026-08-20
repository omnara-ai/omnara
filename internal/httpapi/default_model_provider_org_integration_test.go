//go:build integration

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

const testHostedCompletionToken = "test-hosted-completion-token-at-least-32-bytes"

func testDefaultOpenRouterTemplate() modelstore.DefaultModelProviderTemplate {
	template, err := modelstore.PrepareDefaultModelProviderTemplate(modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "omnara-openrouter",
		CredentialSecretName: "omnara-openrouter-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://openrouter.ai/api/v1",
		EndpointPath:         "/chat/completions",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
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
	pat, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(context.Background(), identitystore.CreatePersonalAccessTokenInput{
		UserID: user.ID, Name: "org creation",
	})
	if err != nil {
		t.Fatalf("create personal access token: %v", err)
	}
	return user, pat.Token
}

func TestCreateOrganizationCompletesHostedCredentialLater(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedAPIToken(testHostedCompletionToken),
	)
	store := integrationStoreForHandler(t, handler)
	user, token := createOrgRouteUser(t, pool, store, "default-provider-async")

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Hosted Provider Org"}`,
		"hosted-provider-org",
		http.StatusCreated,
		authHeaders(token),
	)
	publicOrgID := created["org"].(map[string]any)["id"].(string)
	publicProjectID := created["project"].(map[string]any)["id"].(string)
	orgID, err := publicid.Decode(publicid.KindOrganization, publicOrgID)
	if err != nil {
		t.Fatalf("decode organization id: %v", err)
	}
	projectID, err := publicid.Decode(publicid.KindProject, publicProjectID)
	if err != nil {
		t.Fatalf("decode project id: %v", err)
	}
	if _, err := store.Models().GetModelProviderConfigByName(ctx, orgID, template.Name); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("provider before completion error = %v, want not found", err)
	}
	tenantCredential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     orgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      template.CredentialSecretName,
		Material:  secrets.GenericMaterial{Value: "tenant-secret-must-not-change"},
		Actor:     identitystore.NewUserPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create colliding tenant credential: %v", err)
	}

	completionBody := `{"org_id":"` + publicOrgID +
		`","provisioner":"` + template.Provisioner +
		`","credential_value":"sk-completed-openrouter"}`
	unauthorized := httptest.NewRequest(http.MethodPost, hostedCredentialCompletionPath, strings.NewReader(completionBody))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf(
			"unauthorized completion status=%d body=%s, want 401",
			unauthorizedRecorder.Code,
			unauthorizedRecorder.Body.String(),
		)
	}

	completeHostedCredentialForTest(t, handler, completionBody, http.StatusNoContent)
	provider, err := store.Models().GetModelProviderConfigByName(ctx, orgID, template.Name)
	if err != nil {
		t.Fatalf("get asynchronously completed provider: %v", err)
	}
	if provider.ManagementKind != management.Cluster {
		t.Fatalf("completed provider management kind = %q, want cluster", provider.ManagementKind)
	}
	credential, err := store.Secrets().GetSecret(ctx, orgID, provider.CredentialSecretID)
	if err != nil {
		t.Fatalf("get asynchronously completed credential: %v", err)
	}
	credentialNamePrefix := template.CredentialSecretName + "-"
	credentialNameID, parseErr := uuid.Parse(strings.TrimPrefix(credential.Name, credentialNamePrefix))
	if credential.ID == tenantCredential.ID ||
		!strings.HasPrefix(credential.Name, credentialNamePrefix) ||
		parseErr != nil || credentialNameID.Version() != 7 {
		t.Fatalf("completed credential does not have an isolated UUIDv7 name: %+v", credential)
	}
	payload, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          orgID,
		SecretID:       credential.ID,
		ManagementKind: management.Cluster,
		Kind:           secretstore.SecretKindGeneric,
	})
	if err != nil {
		t.Fatalf("read asynchronously completed credential: %v", err)
	}
	if payload.Payload[secrets.KeyValue] != "sk-completed-openrouter" {
		t.Fatalf("completed credential value = %q", payload.Payload[secrets.KeyValue])
	}
	tenantPayload, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          orgID,
		SecretID:       tenantCredential.ID,
		ManagementKind: management.Tenant,
		Kind:           secretstore.SecretKindGeneric,
	})
	if err != nil {
		t.Fatalf("read colliding tenant credential: %v", err)
	}
	if tenantPayload.Payload[secrets.KeyValue] != "tenant-secret-must-not-change" {
		t.Fatalf("tenant credential value = %q", tenantPayload.Payload[secrets.KeyValue])
	}
	models, err := store.Models().ListConfiguredModels(ctx, modelstore.ListConfiguredModelsInput{
		OrgID: orgID, ProviderConfigID: provider.ID, Limit: 10,
	})
	if err != nil || len(models.Models) != 1 {
		t.Fatalf("completed configured models=%+v err=%v", models.Models, err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		orgID,
		projectID,
		models.Models[0].ID,
	); err != nil {
		t.Fatalf("get completed default project model grant: %v", err)
	}

	retryBody := strings.Replace(completionBody, "sk-completed-openrouter", "sk-must-not-replace", 1)
	completeHostedCredentialForTest(t, handler, retryBody, http.StatusNoContent)
	payload, err = store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          orgID,
		SecretID:       credential.ID,
		ManagementKind: management.Cluster,
		Kind:           secretstore.SecretKindGeneric,
	})
	if err != nil {
		t.Fatalf("read credential after completion replay: %v", err)
	}
	if payload.Payload[secrets.KeyValue] != "sk-completed-openrouter" {
		t.Fatalf("completion replay replaced credential with %q", payload.Payload[secrets.KeyValue])
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Hosted Provider Org"}`,
		"hosted-provider-org",
		http.StatusOK,
		authHeaders(token),
	)
}

func completeHostedCredentialForTest(t *testing.T, handler http.Handler, body string, wantStatus int) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, hostedCredentialCompletionPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testHostedCompletionToken)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf(
			"hosted credential completion status=%d body=%s, want %d",
			recorder.Code,
			recorder.Body.String(),
			wantStatus,
		)
	}
}
