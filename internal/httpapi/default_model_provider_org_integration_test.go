//go:build integration

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/modelprovider"
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

type hostedCredentialProvisionerFunc func(
	context.Context,
	modelprovider.HostedCredentialRequest,
) (modelprovider.ProvisionHostedCredentialResponse, error)

func (f hostedCredentialProvisionerFunc) ProvisionHostedCredential(
	ctx context.Context,
	request modelprovider.HostedCredentialRequest,
) (modelprovider.ProvisionHostedCredentialResponse, error) {
	return f(ctx, request)
}

func TestCreateOrganizationReportsHostedCredentialIssuanceConflict(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	provisioner := hostedCredentialProvisionerFunc(func(
		context.Context,
		modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		return modelprovider.ProvisionHostedCredentialResponse{}, modelprovider.ErrHostedCredentialConflict
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	_, token := createOrgRouteUser(t, pool, store, "default-provider-conflict")

	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Conflicting Credential Org"}`,
		"conflicting-credential-org",
		http.StatusConflict,
		authHeaders(token),
	)
	if !strings.Contains(response["error"].(string), "blocked by an unresolved credential attempt") {
		t.Fatalf("conflict response = %+v", response)
	}
}

func TestCreateOrganizationRejectsKnownCapacityFailureBeforeHostedProvisioning(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	provisionCalls := 0
	provisioner := hostedCredentialProvisionerFunc(func(
		context.Context,
		modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		provisionCalls++
		return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "must-not-be-issued"}, nil
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	user, token := createOrgRouteUser(t, pool, store, "default-provider-capacity")
	for index := 0; ; index++ {
		_, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
			UserID:         user.ID,
			Name:           fmt.Sprintf("Capacity Org %03d", index),
			IdempotencyKey: fmt.Sprintf("capacity-org-%03d", index),
		})
		if errors.Is(err, storeerr.ErrUnauthorized) {
			break
		}
		if err != nil {
			t.Fatalf("fill organization capacity %d: %v", index, err)
		}
		if index >= 1000 {
			t.Fatal("organization capacity was not enforced")
		}
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Over Capacity Org"}`,
		"over-capacity-org",
		http.StatusForbidden,
		authHeaders(token),
	)
	if provisionCalls != 0 {
		t.Fatalf("hosted provision calls = %d, want none for a known capacity failure", provisionCalls)
	}
}

func TestConcurrentOrganizationCreationCommitsOnlyOneFinalOwnedSlot(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	var provisionCalls atomic.Int32
	// Hold both requests at provisioning until both have passed the capacity
	// preflight, so neither can commit first and turn the other into a
	// preflight rejection that skips provisioning.
	var provisioning sync.WaitGroup
	provisioning.Add(2)
	provisioner := hostedCredentialProvisionerFunc(func(
		_ context.Context,
		request modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		provisionCalls.Add(1)
		provisioning.Done()
		provisioning.Wait()
		return modelprovider.ProvisionHostedCredentialResponse{
			CredentialValue: "sk-final-slot-" + request.OrgID,
		}, nil
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	user, token := createOrgRouteUser(t, pool, store, "default-provider-final-slot")
	for index := 0; index < 99; index++ {
		if _, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
			UserID:         user.ID,
			Name:           fmt.Sprintf("Existing Org %03d", index),
			IdempotencyKey: fmt.Sprintf("existing-org-%03d", index),
		}); err != nil {
			t.Fatalf("create existing organization %d: %v", index, err)
		}
	}
	type response struct {
		status int
		body   string
	}
	start := make(chan struct{})
	responses := make(chan response, 2)
	request := func(name, idempotencyKey string) {
		<-start
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/orgs",
			strings.NewReader(`{"name":"`+name+`"}`),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		responses <- response{status: recorder.Code, body: recorder.Body.String()}
	}
	go request("Final Slot A", "final-slot-a")
	go request("Final Slot B", "final-slot-b")
	close(start)
	first := <-responses
	second := <-responses
	created := 0
	forbidden := 0
	for _, result := range []response{first, second} {
		switch result.status {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
			forbidden++
		default:
			t.Fatalf("concurrent organization response: status=%d body=%s", result.status, result.body)
		}
	}
	if created != 1 || forbidden != 1 {
		t.Fatalf("concurrent statuses: first=%+v second=%+v", first, second)
	}
	if calls := provisionCalls.Load(); calls != 2 {
		t.Fatalf("hosted provision calls = %d, want one per independent attempt", calls)
	}
	var ownedCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM org_memberships membership
		WHERE membership.user_id = $1 AND membership.role = 'owner'
	`, user.ID).Scan(&ownedCount); err != nil {
		t.Fatalf("read final owned capacity: %v", err)
	}
	if ownedCount != 100 {
		t.Fatalf("owned organizations=%d, want 100", ownedCount)
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
		UserID: user.ID, Name: "org creation", TokenID: seed,
	})
	if err != nil {
		t.Fatalf("create personal access token: %v", err)
	}
	return user, pat.Token
}

func TestCreateOrganizationProvisionsHostedCredentialBeforeAtomicLocalCreation(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	var provisionRequest modelprovider.HostedCredentialRequest
	provisioner := hostedCredentialProvisionerFunc(func(
		_ context.Context,
		request modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		provisionRequest = request
		return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "sk-cluster-openrouter"}, nil
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	user, token := createOrgRouteUser(t, pool, store, "default-provider-success")

	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Cluster Provider Org"}`,
		"cluster-provider-org",
		http.StatusCreated,
		authHeaders(token),
	)
	orgResponse := created["org"].(map[string]any)
	projectResponse := created["project"].(map[string]any)
	publicOrgID := orgResponse["id"].(string)
	if provisionRequest.OrgID != publicOrgID {
		t.Fatalf("unexpected provision request: %+v", provisionRequest)
	}
	publicCreatorUserID, err := publicid.Encode(publicid.KindUser, user.ID)
	if err != nil {
		t.Fatalf("encode creator user id: %v", err)
	}
	if provisionRequest.CreatorUserID != publicCreatorUserID {
		t.Fatalf("provision creator = %q, want %q", provisionRequest.CreatorUserID, publicCreatorUserID)
	}
	if !reflect.DeepEqual(provisionRequest.Template, template) {
		t.Fatalf("provision template = %+v, want canonical %+v", provisionRequest.Template, template)
	}
	orgID, err := publicid.Decode(publicid.KindOrganization, publicOrgID)
	if err != nil {
		t.Fatalf("decode organization id: %v", err)
	}
	projectID, err := publicid.Decode(publicid.KindProject, projectResponse["id"].(string))
	if err != nil {
		t.Fatalf("decode project id: %v", err)
	}
	provider, err := store.Models().GetModelProviderConfigByName(ctx, orgID, template.Name)
	if err != nil {
		t.Fatalf("get cluster model provider: %v", err)
	}
	if provider.ManagementKind != management.Cluster {
		t.Fatalf("unexpected cluster model provider: %+v", provider)
	}
	credential, err := store.Secrets().GetSecret(ctx, orgID, provider.CredentialSecretID)
	if err != nil {
		t.Fatalf("get cluster credential: %v", err)
	}
	if credential.ManagementKind != management.Cluster || credential.OwnerKind != secretstore.SecretOwnerOrg {
		t.Fatalf("unexpected cluster credential: %+v", credential)
	}
	payload, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          orgID,
		SecretID:       credential.ID,
		ManagementKind: management.Cluster,
		Kind:           secretstore.SecretKindGeneric,
	})
	if err != nil {
		t.Fatalf("read cluster credential payload: %v", err)
	}
	if payload.Payload[secrets.KeyValue] != "sk-cluster-openrouter" {
		t.Fatalf("cluster credential value = %q", payload.Payload[secrets.KeyValue])
	}
	if _, err := store.Secrets().ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID:          orgID,
		SecretID:       credential.ID,
		ManagementKind: management.Tenant,
		Kind:           secretstore.SecretKindGeneric,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("read cluster credential as tenant-managed error = %v, want not found", err)
	}
	models, err := store.Models().ListConfiguredModels(ctx, modelstore.ListConfiguredModelsInput{
		OrgID: orgID, ProviderConfigID: provider.ID, Limit: 10,
	})
	if err != nil || len(models.Models) != 1 {
		t.Fatalf("list cluster configured models: models=%+v err=%v", models.Models, err)
	}
	if _, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		orgID,
		projectID,
		models.Models[0].ID,
	); err != nil {
		t.Fatalf("get default project model grant: %v", err)
	}
	publicProviderID, err := publicid.Encode(publicid.KindModelProviderConfig, provider.ID)
	if err != nil {
		t.Fatalf("encode model provider id: %v", err)
	}
	publicModelID, err := publicid.Encode(publicid.KindConfiguredModel, models.Models[0].ID)
	if err != nil {
		t.Fatalf("encode configured model id: %v", err)
	}
	publicSecretID, err := publicid.Encode(publicid.KindSecret, credential.ID)
	if err != nil {
		t.Fatalf("encode credential secret id: %v", err)
	}
	if _, err := store.Execution().ResolveEnvironmentSecrets(
		ctx,
		orgID,
		storage.NilID,
		[]byte(`{}`),
		[]byte(`{"OPENROUTER_API_KEY":"`+publicSecretID+`"}`),
	); err == nil || !errors.Is(err, storeerr.ErrPermanentEnvironment) {
		t.Fatalf("resolve cluster credential in org-scoped environment error = %v, want permanent environment error", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+publicOrgID+"/model-provider-configs/"+publicProviderID,
		`{"base_url":"https://proxy.example.test/v1"}`,
		"",
		http.StatusConflict,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+publicOrgID+"/model-provider-configs/"+publicProviderID+"/models",
		`{"name":"tenant-added","provider_model_slug":"example/tenant-added","context_window_tokens":8192,"max_output_tokens":4096}`,
		"",
		http.StatusConflict,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPatch,
		"/api/v1/orgs/"+publicOrgID+"/secrets/"+publicSecretID,
		`{"name":"tenant-renamed"}`,
		"",
		http.StatusConflict,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+publicOrgID+"/model-provider-configs",
		`{"name":"reused-hosted-key","preset":"openrouter","credential_secret_id":"`+publicSecretID+`"}`,
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+publicOrgID+"/secrets/"+publicSecretID+"/grants",
		`{"target_project_id":"`+projectResponse["id"].(string)+`"}`,
		"",
		http.StatusConflict,
		authHeaders(token),
	)
	grantList := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+publicOrgID+"/secrets/"+publicSecretID+"/grants",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)["data"].([]any)
	if len(grantList) != 0 {
		t.Fatalf("cluster credential grants = %+v, want empty", grantList)
	}
	poolCredential := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+publicOrgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"tenant-pool-key","material":{"kind":"generic","value":"pool-key"}}`,
		"",
		http.StatusCreated,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+publicOrgID+"/machine-pools",
		`{"name":"hosted-key-env","provider":"unikraft","default_machine_memory_mb":1024,"default_machine_cpu":1,"default_machine_env":{},"default_machine_secret_env":{"OPENROUTER_API_KEY":"`+publicSecretID+`"},"default_machine_provider_options":{"image":"test","metro":"sfo"},"default_cwd":"/workspace","provider_config":{},"provider_auth_secret_id":"`+poolCredential["id"].(string)+`","max_total_machines":1,"max_total_cpu":2,"max_total_memory_mb":4096,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
		"",
		http.StatusNotFound,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPut,
		"/api/v1/orgs/"+publicOrgID+"/model-provider-configs/"+publicProviderID+"/models/"+publicModelID,
		`{"name":"tenant-renamed"}`,
		"",
		http.StatusConflict,
		authHeaders(token),
	)

	providerList := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+publicOrgID+"/model-provider-configs",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)["data"].([]any)
	foundProvider := false
	for _, raw := range providerList {
		item := raw.(map[string]any)
		if item["name"] == template.Name {
			foundProvider = true
			if item["management_kind"] != string(management.Cluster) {
				t.Fatalf("unexpected public cluster provider: %+v", item)
			}
		}
	}
	if !foundProvider {
		t.Fatalf("cluster provider missing from tenant-visible collection: %+v", providerList)
	}
	secretList := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/orgs/"+publicOrgID+"/secrets",
		"",
		"",
		http.StatusOK,
		authHeaders(token),
	)["data"].([]any)
	foundSecret := false
	for _, raw := range secretList {
		item := raw.(map[string]any)
		if item["name"] == template.CredentialSecretName {
			foundSecret = true
			if item["management_kind"] != string(management.Cluster) {
				t.Fatalf("unexpected public cluster secret: %+v", item)
			}
		}
	}
	if !foundSecret {
		t.Fatalf("cluster credential missing from tenant-visible collection: %+v", secretList)
	}
}

func TestCreateOrganizationDoesNotPersistLocalStateWhenProvisioningFails(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	var provisionRequests []modelprovider.HostedCredentialRequest
	provisioner := hostedCredentialProvisionerFunc(func(
		_ context.Context,
		request modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		provisionRequests = append(provisionRequests, request)
		return modelprovider.ProvisionHostedCredentialResponse{}, errors.New("hosted service unavailable")
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	user, token := createOrgRouteUser(t, pool, store, "default-provider-failure")

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Provisioning Failure Org"}`,
		"provisioning-failure-org",
		http.StatusServiceUnavailable,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Provisioning Failure Org"}`,
		"provisioning-failure-org",
		http.StatusServiceUnavailable,
		authHeaders(token),
	)
	if len(provisionRequests) != 2 || provisionRequests[0].OrgID == "" {
		t.Fatalf("unexpected failed provision requests: %+v", provisionRequests)
	}
	if provisionRequests[1].OrgID != provisionRequests[0].OrgID {
		t.Fatalf("retry changed hosted issuance identity: %+v", provisionRequests)
	}
	orgID, err := publicid.Decode(publicid.KindOrganization, provisionRequests[0].OrgID)
	if err != nil {
		t.Fatalf("decode attempted organization id: %v", err)
	}
	if _, err := store.Identity().GetOrg(ctx, orgID); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("get organization after failed provisioning error = %v, want not found", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM orgs WHERE name = 'Provisioning Failure Org'`).Scan(&count); err != nil {
		t.Fatalf("count failed organizations: %v", err)
	}
	if count != 0 {
		t.Fatalf("organizations persisted after provisioning failure = %d, want 0", count)
	}
	publicCreatorUserID, err := publicid.Encode(publicid.KindUser, user.ID)
	if err != nil {
		t.Fatalf("encode creator user id: %v", err)
	}
	for _, request := range provisionRequests {
		if request.CreatorUserID != publicCreatorUserID {
			t.Fatalf("failed attempt creator = %q, want %q", request.CreatorUserID, publicCreatorUserID)
		}
	}
}

func TestCreateOrganizationReplayDoesNotProvisionAnotherCredential(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	var requests []modelprovider.HostedCredentialRequest
	provisioner := hostedCredentialProvisionerFunc(func(
		_ context.Context,
		request modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		requests = append(requests, request)
		return modelprovider.ProvisionHostedCredentialResponse{
			CredentialValue: "sk-provision-attempt-" + request.OrgID,
		}, nil
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	_, token := createOrgRouteUser(t, pool, store, "default-provider-replay")

	first := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Provision Replay Org"}`,
		"provision-replay-org",
		http.StatusCreated,
		authHeaders(token),
	)
	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"Provision Replay Org"}`,
		"provision-replay-org",
		http.StatusOK,
		authHeaders(token),
	)
	firstOrgID := first["org"].(map[string]any)["id"].(string)
	replayedOrgID := replayed["org"].(map[string]any)["id"].(string)
	if replayedOrgID != firstOrgID {
		t.Fatalf("replayed org id = %q, want %q", replayedOrgID, firstOrgID)
	}
	if len(requests) != 1 {
		t.Fatalf("provision requests = %+v, want one call", requests)
	}
}

func TestCreateOrganizationWithoutIdempotencyKeyStartsFreshAttempts(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	template := testDefaultOpenRouterTemplate()
	var requests []modelprovider.HostedCredentialRequest
	provisioner := hostedCredentialProvisionerFunc(func(
		_ context.Context,
		request modelprovider.HostedCredentialRequest,
	) (modelprovider.ProvisionHostedCredentialResponse, error) {
		requests = append(requests, request)
		return modelprovider.ProvisionHostedCredentialResponse{CredentialValue: "sk-" + request.OrgID}, nil
	})
	handler := newIntegrationServer(
		pool,
		WithDefaultModelProvider(&template),
		WithHostedCredentialProvisioner(provisioner),
	)
	store := integrationStoreForHandler(t, handler)
	_, token := createOrgRouteUser(t, pool, store, "default-provider-unkeyed")
	first := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs", `{"name":"Unkeyed One"}`, "",
		http.StatusCreated, authHeaders(token),
	)
	second := requestJSONWithHeaders(
		t, handler, http.MethodPost, "/api/v1/orgs", `{"name":"Unkeyed Two"}`, "",
		http.StatusCreated, authHeaders(token),
	)
	firstID := first["org"].(map[string]any)["id"].(string)
	secondID := second["org"].(map[string]any)["id"].(string)
	if firstID == secondID || len(requests) != 2 || requests[0].OrgID == requests[1].OrgID {
		t.Fatalf("unkeyed attempts reused identity: first=%s second=%s requests=%+v", firstID, secondID, requests)
	}
}
