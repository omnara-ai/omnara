//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

type publicHTTPProject struct {
	OrgID         string
	ProjectID     string
	AdminUserID   string
	OrgUUID       storage.ID
	ProjectUUID   storage.ID
	AdminUserUUID storage.ID
	AdminToken    string
	AdminSession  string
	AdminCSRF     string
	ProjectPath   string
	Store         *storage.Store
}

func openIntegrationDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
}

func authHeaders(token string) map[string]string {
	if token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + token}
}

func browserAuthHeaders(sessionToken, csrfToken string) map[string]string {
	if sessionToken == "" || csrfToken == "" {
		return nil
	}
	return map[string]string{
		"Cookie":                httpauth.BrowserSessionCookieName + "=" + sessionToken + "; " + httpauth.CSRFCookieName + "=" + csrfToken,
		"Origin":                "http://example.com",
		httpauth.CSRFHeaderName: csrfToken,
	}
}

func (p publicHTTPProject) adminBrowserAuthHeaders() map[string]string {
	return browserAuthHeaders(p.AdminSession, p.AdminCSRF)
}

func requestJSONWithHeaders(
	t *testing.T,
	handler http.Handler,
	method, path, body, idempotencyKey string,
	wantStatus int,
	headers map[string]string,
) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf(
			"%s %s status=%d want=%d body=%s",
			method,
			path,
			rec.Code,
			wantStatus,
			rec.Body.String(),
		)
	}
	if rec.Body.Len() == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf(
			"decode response for %s %s: %v body=%s",
			method,
			path,
			err,
			rec.Body.String(),
		)
	}
	return out
}

func requestJSONFieldWithHeaders(
	t *testing.T,
	handler http.Handler,
	method, path, body, idempotencyKey string,
	wantStatus int,
	headers map[string]string,
	field string,
) any {
	t.Helper()
	return requestJSONWithHeaders(
		t,
		handler,
		method,
		path,
		body,
		idempotencyKey,
		wantStatus,
		headers,
	)[field]
}

func integrationKeyWrapper() secrets.KeyWrapper {
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"http-test-key",
		map[string][]byte{
			"http-test-key": []byte("0123456789abcdef0123456789abcdef"),
		},
	)
	if err != nil {
		panic(err)
	}
	return keyWrapper
}

func newIntegrationStore(pool *pgxpool.Pool) *storage.Store {
	return storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(integrationKeyWrapper()),
		storage.WithMachinePoolProviders(machinepool.DefaultCatalog()),
	)
}

func httpUserPrincipal(userID storage.ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func httpOmnaraActorID(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, userID storage.ID,
) storage.ID {
	t.Helper()
	params := httpOmnaraActorParams(t, orgID, userID)
	actors, err := store.Execution().ListActors(ctx, executionstore.ListActorsInput{
		ProjectID:        projectID,
		Provider:         params.Provider,
		ProviderTenantID: params.ProviderTenantID,
		ProviderUserID:   params.ProviderUserID,
	})
	if err != nil || len(actors) != 1 {
		t.Fatalf("lookup omnara actor: actors=%d err=%v", len(actors), err)
	}
	return actors[0].ID
}

func httpOmnaraActorParams(t *testing.T, orgID, userID storage.ID) *executionstore.ActorParams {
	t.Helper()
	params, err := executionstore.OmnaraActorParams(orgID, httpUserPrincipal(userID))
	if err != nil {
		t.Fatalf("omnara actor params: %v", err)
	}
	return params
}

func httpOmnaraActorPublicID(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, userID storage.ID,
) string {
	t.Helper()
	return testPublicID(
		t,
		publicid.KindActor,
		httpOmnaraActorID(t, ctx, store, orgID, projectID, userID),
	)
}

type integrationHTTPHandler struct {
	handler http.Handler
	pool    *pgxpool.Pool
	store   *storage.Store
}

func (h *integrationHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		h.handler.ServeHTTP(w, r)
		return
	}
	recorder := &responseContractRecorder{ResponseWriter: w, request: r}
	h.handler.ServeHTTP(recorder, r)
	validateResponseContract(r, recorder)
}

func newIntegrationHTTPHandler(
	handler http.Handler,
	pool *pgxpool.Pool,
	store *storage.Store,
) http.Handler {
	return &integrationHTTPHandler{handler: handler, pool: pool, store: store}
}

func integrationStoreForHandler(t testing.TB, handler http.Handler) *storage.Store {
	t.Helper()
	integrationHandler, ok := handler.(*integrationHTTPHandler)
	if !ok || integrationHandler.store == nil {
		t.Fatal("integration handler does not carry a store")
	}
	return integrationHandler.store
}

func integrationPoolForHandler(t testing.TB, handler http.Handler) *pgxpool.Pool {
	t.Helper()
	integrationHandler, ok := handler.(*integrationHTTPHandler)
	if !ok || integrationHandler.pool == nil {
		t.Fatal("integration handler does not carry a database pool")
	}
	return integrationHandler.pool
}

func newIntegrationServer(pool *pgxpool.Pool, opts ...Option) http.Handler {
	return newIntegrationServerWithStoreOptions(pool, nil, opts...)
}

func newIntegrationServerWithStoreOptions(
	pool *pgxpool.Pool,
	storeOpts []storage.Option,
	opts ...Option,
) http.Handler {
	keyWrapper := integrationKeyWrapper()
	baseStoreOpts := []storage.Option{
		storage.WithSecretKeyWrapper(keyWrapper),
		storage.WithMachinePoolProviders(machinepool.DefaultCatalog()),
	}
	baseStoreOpts = append(baseStoreOpts, storeOpts...)
	store := storage.NewStore(pool, baseStoreOpts...)
	// Defaults come first; later opts (caller-supplied) override.
	serverOpts := append(
		[]Option{
			WithSecretKeyWrapper(keyWrapper),
			WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
			WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
			WithModelDiscoverer(func(
				context.Context,
				modelstore.ModelProviderConfigRecord,
				string,
				bool,
			) ([]modelprovider.DiscoveredModel, error) {
				return nil, errors.New("model discovery is disabled in integration tests")
			}),
		},
		opts...,
	)
	server, err := New(testLogger(), store, serverOpts...)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		})
	}
	return newIntegrationHTTPHandler(
		withDefaultRequestOrigin(server.Handler(), defaultRequestOrigin(server.publicURL)),
		pool,
		store,
	)
}

func defaultRequestOrigin(publicURL string) string {
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func withDefaultRequestOrigin(handler http.Handler, origin string) http.Handler {
	if origin == "" {
		return handler
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "example.com" {
			clone := r.Clone(r.Context())
			clone.Host = parsed.Host
			if clone.Header.Get("Origin") == "http://example.com" {
				clone.Header.Set("Origin", origin)
			}
			r = clone
		}
		handler.ServeHTTP(w, r)
	})
}

func mustNewServer(t testing.TB, store *storage.Store, opts ...Option) *Server {
	t.Helper()
	// Defaults come first; later opts (caller-supplied) override.
	serverOpts := append(
		[]Option{
			WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
			WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		},
		opts...,
	)
	server, err := New(testLogger(), store, serverOpts...)
	if err != nil {
		t.Fatalf("create http api server: %v", err)
	}
	return server
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func requestJSONArrayWithHeaders(
	t *testing.T,
	handler http.Handler,
	method, path string,
	wantStatus int,
	headers map[string]string,
) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf(
			"%s %s status=%d want=%d body=%s",
			method,
			path,
			rec.Code,
			wantStatus,
			rec.Body.String(),
		)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf(
			"decode array response for %s %s: %v body=%s",
			method,
			path,
			err,
			rec.Body.String(),
		)
	}
	return out
}

func bootstrapPublicHTTPProject(
	t *testing.T,
	handler http.Handler,
	seed string,
) publicHTTPProject {
	t.Helper()
	store := integrationStoreForHandler(t, handler)
	pool := integrationPoolForHandler(t, handler)
	admin, err := storagetest.CreateVerifiedUser(context.Background(), pool, storagetest.CreateVerifiedUserInput{Email: seed + "-owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatalf("create owner user: %v", err)
	}
	adminPAT, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		context.Background(),
		identitystore.CreatePersonalAccessTokenInput{
			UserID: admin.ID,
			Name:   "test owner",
		},
	)
	if err != nil {
		t.Fatalf("create owner PAT: %v", err)
	}
	adminToken := adminPAT.Token
	adminSession := seed + "-admin-session"
	adminCSRF := seed + "-admin-csrf"
	if _, err := store.Identity().CreateBrowserSession(
		context.Background(),
		identitystore.CreateBrowserSessionInput{
			UserID:    admin.ID,
			Token:     adminSession,
			CSRFToken: adminCSRF,
			TTL:       time.Hour,
		},
	); err != nil {
		t.Fatalf("create owner browser session: %v", err)
	}
	created := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs",
		`{"name":"`+seed+` Org"}`,
		"idem-"+seed+"-org",
		http.StatusCreated,
		authHeaders(adminToken),
	)
	org := created["org"].(map[string]any)
	project := created["project"].(map[string]any)
	orgID := org["id"].(string)
	projectID := project["id"].(string)
	bootstrapDefaultPublicHTTPModelProvider(t, handler, orgID, projectID, adminToken)
	orgUUID := mustPublicHTTPID(t, publicid.KindOrganization, orgID)
	projectUUID := mustPublicHTTPID(t, publicid.KindProject, projectID)
	adminUserID, err := publicid.Encode(publicid.KindUser, admin.ID)
	if err != nil {
		t.Fatalf("encode admin user id: %v", err)
	}
	return publicHTTPProject{
		OrgID:         orgID,
		ProjectID:     projectID,
		AdminUserID:   adminUserID,
		OrgUUID:       orgUUID,
		ProjectUUID:   projectUUID,
		AdminUserUUID: mustPublicHTTPID(t, publicid.KindUser, adminUserID),
		AdminToken:    adminToken,
		AdminSession:  adminSession,
		AdminCSRF:     adminCSRF,
		ProjectPath:   "/api/v1/orgs/" + orgID + "/projects/" + projectID,
		Store:         store,
	}
}

func createdModelProviderConfig(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	config, ok := response["config"].(map[string]any)
	if !ok {
		t.Fatalf("create model provider response has no config: %+v", response)
	}
	if _, ok := response["model_discovery"].(map[string]any); !ok {
		t.Fatalf("create model provider response has no model_discovery: %+v", response)
	}
	return config
}

func bootstrapDefaultPublicHTTPModelProvider(t *testing.T, handler http.Handler, orgID, projectID, adminToken string) {
	t.Helper()
	projectPath := "/api/v1/orgs/" + orgID + "/projects/" + projectID
	secret := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/secrets",
		`{"owner":{"kind":"org"},"name":"default-openai-provider-key","material":{"kind":"generic","value":"sk-test"}}`,
		"",
		http.StatusCreated,
		authHeaders(adminToken),
	)
	providerConfig := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/model-provider-configs",
		`{"name":"openai-prod","api_format":"openai-responses","api_variant":"default","base_url":"https://api.openai.com/v1","credential_secret_id":"`+secret["id"].(string)+`"}`,
		"",
		http.StatusCreated,
		authHeaders(adminToken),
	)
	configuredModel := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/model-provider-configs/"+createdModelProviderConfig(t, providerConfig)["id"].(string)+"/models",
		`{"name":"gpt-test","provider_model_slug":"gpt-test","context_window_tokens":128000,"max_output_tokens":8192,"default_max_output_tokens":4096}`,
		"",
		http.StatusCreated,
		authHeaders(adminToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		projectPath+"/model-grants",
		`{"configured_model_id":"`+configuredModel["id"].(string)+`"}`,
		"",
		http.StatusCreated,
		authHeaders(adminToken),
	)
}

func grantDefaultPublicHTTPModelToProject(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	projectID, adminToken string,
) {
	t.Helper()
	providerConfig, err := project.Store.Models().GetModelProviderConfigByName(
		context.Background(),
		project.OrgUUID,
		"openai-prod",
	)
	if err != nil {
		t.Fatalf("load default model provider config: %v", err)
	}
	configuredModel, err := project.Store.Models().GetConfiguredModelByName(
		context.Background(),
		project.OrgUUID,
		providerConfig.ID,
		"gpt-test",
	)
	if err != nil {
		t.Fatalf("load default configured model: %v", err)
	}
	configuredModelID, err := publicid.Encode(publicid.KindConfiguredModel, configuredModel.ID)
	if err != nil {
		t.Fatalf("encode default configured model id: %v", err)
	}
	path := "/api/v1/orgs/" + project.OrgID + "/projects/" + projectID + "/model-grants"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		`{"configured_model_id":"`+configuredModelID+`"}`,
		"",
		http.StatusCreated,
		authHeaders(adminToken),
	)
}

func createPublicHTTPAgent(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed string,
	token string,
) map[string]any {
	t.Helper()
	sourceYAML := "name: " + seed + " Agent\ninstruction: Help the user make progress.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n"
	config := createPublicHTTPAgentConfig(t, handler, project, seed, "yaml", sourceYAML, token, http.StatusCreated)
	return createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		seed,
		seed+" Agent",
		config["id"].(string),
		token,
		http.StatusCreated,
	)
}

func createPublicHTTPAgentConfig(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed, sourceFormat, source, token string,
	wantStatus int,
) map[string]any {
	t.Helper()
	return requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-configs",
		`{"source_format":"`+sourceFormat+`","source":`+quotedJSONString(
			source,
		)+`}`,
		"",
		wantStatus,
		authHeaders(token),
	)
}

func createPublicHTTPAgentProfile(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed, name, configID, token string,
	wantStatus int,
) map[string]any {
	t.Helper()
	return requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agent-profiles",
		`{"name":"`+name+`","config":"`+configID+`"}`,
		"idem-"+seed+"-agent-profile",
		wantStatus,
		authHeaders(token),
	)
}

func launchPublicHTTPAgent(
	t *testing.T,
	handler http.Handler,
	project publicHTTPProject,
	seed string,
	token string,
	wantStatus int,
) map[string]any {
	t.Helper()
	profile := createPublicHTTPAgent(t, handler, project, seed, token)
	profileID := profile["id"].(string)
	configID := profile["current_config"].(map[string]any)["id"].(string)
	return requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+profileID+`","config":"`+configID+`"}`,
		"idem-"+seed+"-agent",
		wantStatus,
		authHeaders(token),
	)
}

func quotedJSONString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func mustPublicHTTPID(
	t *testing.T,
	kind publicid.Kind,
	value string,
) storage.ID {
	t.Helper()
	id, err := publicid.Decode(kind, value)
	if err != nil {
		t.Fatalf("decode %s public id %q: %v", kind, value, err)
	}
	return id
}

func httpTestID(seed string) storage.ID {
	return uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte("omnara-http-integration:"+seed),
	)
}

func httpTestClaimInput() executionstore.ClaimNextAgentWorkInput {
	return executionstore.ClaimNextAgentWorkInput{
		WorkerProcessID: httpTestID("worker_process"),
		LeaseDuration:   executionstore.AgentRuntimeLockLeaseDuration,
	}
}
