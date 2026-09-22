package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type githubIntakeFixture struct {
	app            integrationstore.ProjectAppRecord
	credential     secretstore.SecretPayloadRecord
	lookupErr      error
	secretErr      error
	acceptErr      error
	accepted       []integrationstore.VerifiedIntegrationReceipt
	secretRead     *secretstore.ReadProjectAvailableSecretPayloadInput
	commit         func(context.Context) error
	appsByIdentity map[string]integrationstore.ProjectAppRecord
	candidates     []integrationstore.ProjectAppRecord
	resolverErr    error
	appLookups     int
	pages          int
	apps           []integrationstore.ProjectAppRecord
	secretErrors   map[uuid.UUID]error
	credentials    map[uuid.UUID]secretstore.SecretPayloadRecord
	acceptErrors   map[uuid.UUID]error
	secretReads    []secretstore.ReadProjectAvailableSecretPayloadInput
}

func newGitHubIntakeFixture() *githubIntakeFixture {
	return &githubIntakeFixture{
		app: integrationstore.ProjectAppRecord{
			ID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), CredentialSecretID: uuid.New(),
			Provider: "github", ProviderTenantID: "123", ProviderAccountRef: "456",
			State: integrationstore.ProjectAppStateActive,
		},
		credential: secretstore.SecretPayloadRecord{CurrentVersionID: uuid.New(), Payload: secrets.Payload{
			secrets.KeyAppID: "123", secrets.KeyWebhookSecret: "webhook-secret", secrets.KeyPrivateKey: "private-key",
		}},
	}
}

func (f *githubIntakeFixture) ListProjectAppsByProviderIdentity(
	_ context.Context, provider, appID, installationID string, after uuid.UUID, limit int,
) ([]integrationstore.ProjectAppRecord, error) {
	f.pages++
	if f.lookupErr != nil {
		if storeerr.IsNotFound(f.lookupErr) {
			return nil, nil
		}
		return nil, f.lookupErr
	}
	apps := f.apps
	if apps == nil {
		apps = []integrationstore.ProjectAppRecord{f.app}
		if f.appsByIdentity != nil {
			apps = nil
			for _, app := range f.appsByIdentity {
				apps = append(apps, app)
			}
		}
	}
	var result []integrationstore.ProjectAppRecord
	for _, app := range apps {
		if app.Provider == provider && app.ProviderTenantID == appID && app.ProviderAccountRef == installationID &&
			app.State == integrationstore.ProjectAppStateActive && app.ID.String() > after.String() {
			result = append(result, app)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID.String() < result[j].ID.String() })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (f *githubIntakeFixture) credentialApps(
	_ context.Context, _ string, _ int,
) ([]integrationstore.ProjectAppRecord, error) {
	f.appLookups++
	if f.candidates != nil {
		return f.candidates, f.resolverErr
	}
	return []integrationstore.ProjectAppRecord{f.app}, f.resolverErr
}

func (f *githubIntakeFixture) ReadProjectAvailableSecretPayload(
	_ context.Context, input secretstore.ReadProjectAvailableSecretPayloadInput,
) (secretstore.SecretPayloadRecord, error) {
	f.secretRead = &input
	f.secretReads = append(f.secretReads, input)
	if err := f.secretErrors[input.ProjectID]; err != nil {
		return secretstore.SecretPayloadRecord{}, err
	}
	if credential, ok := f.credentials[input.ProjectID]; ok {
		return credential, nil
	}
	return f.credential, f.secretErr
}

func (f *githubIntakeFixture) AcceptIntegrationReceipt(
	ctx context.Context, input integrationstore.VerifiedIntegrationReceipt,
) (integrationstore.IntegrationInboxRecord, bool, error) {
	if f.commit != nil {
		if err := f.commit(ctx); err != nil {
			return integrationstore.IntegrationInboxRecord{}, false, err
		}
	}
	if err := f.acceptErrors[input.AppID]; err != nil {
		return integrationstore.IntegrationInboxRecord{}, false, err
	}
	if f.acceptErr != nil {
		return integrationstore.IntegrationInboxRecord{}, false, f.acceptErr
	}
	f.accepted = append(f.accepted, input)
	return integrationstore.IntegrationInboxRecord{}, len(f.accepted) == 1, nil
}

const githubIntakeBody = `{
  "action":"created", "installation":{"id":456}, "repository":{"id":1001},
  "issue":{"number":42,"pull_request":{"url":"https://api.github.com/repos/owner/repo/pulls/42"}},
  "comment":{"id":3001,"body":"@helper hi","user":{"id":71,"login":"human","type":"User"}},
  "sender":{"id":71,"login":"human","type":"User"}
}`

func githubIntakeRequest(t *testing.T, f *githubIntakeFixture, raw string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/integrations/github/123/events", strings.NewReader(raw))
	r = r.WithContext(t.Context())
	r.SetPathValue("app_id", "123")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(github.EventHeader, "issue_comment")
	r.Header.Set(github.DeliveryHeader, "delivery-1")
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	_, _ = mac.Write([]byte(raw))
	r.Header.Set(github.SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return r
}

func TestGitHubHTTPIntakePersistsExactBodyBeforeAcknowledgement(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	f.commit = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > githubIntakeTimeout {
			t.Error("intake context is not bounded")
		}
		return ctx.Err()
	}
	h := &githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}
	mux := http.NewServeMux()
	mux.Handle("POST "+GitHubEventsPath, h)
	for range 2 {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, githubIntakeRequest(t, f, githubIntakeBody))
		if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("response: %d %s", w.Code, w.Body.String())
		}
	}
	if len(f.accepted) != 2 || string(f.accepted[0].Payload) != githubIntakeBody ||
		f.accepted[0].ReceiptKey != "github:issue_comment:delivery-1" ||
		f.accepted[0].ProjectID != f.app.ProjectID || f.accepted[0].AppID != f.app.ID {
		t.Fatalf("receipt: %+v", f.accepted)
	}
	if f.secretRead == nil || f.secretRead.OrgID != f.app.OrgID ||
		f.secretRead.ProjectID != f.app.ProjectID || f.secretRead.SecretID != f.app.CredentialSecretID ||
		f.secretRead.Kind != secrets.KindGitHubAppCredentials {
		t.Fatalf("secret scope: %+v", f.secretRead)
	}
}

type githubIntakeResponse struct {
	*httptest.ResponseRecorder
	status atomic.Int32
}

func (w *githubIntakeResponse) WriteHeader(status int) {
	w.status.Store(int32(status))
	w.ResponseRecorder.WriteHeader(status)
}

func TestGitHubHTTPIntakeWaitsForDurableCommit(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.commit = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	w := &githubIntakeResponse{ResponseRecorder: httptest.NewRecorder()}
	r := githubIntakeRequest(t, f, githubIntakeBody)
	go func() {
		defer close(done)
		(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).ServeHTTP(w, r)
	}()
	<-entered
	statusBeforeCommit := w.status.Load()
	close(release)
	<-done
	if statusBeforeCommit != 0 || w.Code != http.StatusNoContent || len(f.accepted) != 1 {
		t.Fatalf("ack ordering: before=%d after=%d receipts=%d", statusBeforeCommit, w.Code, len(f.accepted))
	}
}

func TestGitHubHTTPIntakeRejectsUnverifiedOrUncommitted(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		edit   func(*githubIntakeFixture, *http.Request)
	}{
		{"missing signature", http.StatusUnauthorized, func(_ *githubIntakeFixture, r *http.Request) {
			r.Header.Del(github.SignatureHeader)
		}},
		{"duplicate signature", http.StatusUnauthorized, func(_ *githubIntakeFixture, r *http.Request) {
			r.Header.Add(github.SignatureHeader, r.Header.Get(github.SignatureHeader))
		}},
		{"wrong secret", http.StatusUnauthorized, func(f *githubIntakeFixture, _ *http.Request) {
			f.credential.Payload[secrets.KeyWebhookSecret] = "rotated-secret"
		}},
		{"wrong credential App", http.StatusUnauthorized, func(f *githubIntakeFixture, _ *http.Request) {
			f.credential.Payload[secrets.KeyAppID] = "321"
		}},
		{"unmanaged installation", http.StatusNoContent, func(f *githubIntakeFixture, _ *http.Request) {
			f.app.ProviderAccountRef = "654"
		}},
		{"disabled", http.StatusNoContent, func(f *githubIntakeFixture, _ *http.Request) {
			f.app.State = integrationstore.ProjectAppStateDisconnected
		}},
		{"wrong provider", http.StatusUnauthorized, func(f *githubIntakeFixture, _ *http.Request) {
			f.app.Provider = "discord"
		}},
		{"unknown app", http.StatusNoContent, func(f *githubIntakeFixture, _ *http.Request) {
			f.lookupErr = storeerr.ErrNotFound
		}},
		{"bad path", http.StatusNotFound, func(_ *githubIntakeFixture, r *http.Request) {
			r.SetPathValue("app_id", "bad")
		}},
		{"lookup failure", http.StatusServiceUnavailable, func(f *githubIntakeFixture, _ *http.Request) {
			f.lookupErr = errors.New("private-key webhook-secret")
		}},
		{"secret failure", http.StatusServiceUnavailable, func(f *githubIntakeFixture, _ *http.Request) {
			f.secretErr = errors.New("private-key webhook-secret")
		}},
		{"commit failure", http.StatusServiceUnavailable, func(f *githubIntakeFixture, _ *http.Request) {
			f.acceptErr = errors.New("private-key webhook-secret")
		}},
		{"missing event", http.StatusBadRequest, func(_ *githubIntakeFixture, r *http.Request) {
			r.Header.Del(github.EventHeader)
		}},
		{"duplicate event", http.StatusBadRequest, func(_ *githubIntakeFixture, r *http.Request) {
			r.Header.Add(github.EventHeader, "pull_request")
		}},
		{"bad delivery", http.StatusBadRequest, func(_ *githubIntakeFixture, r *http.Request) {
			r.Header.Set(github.DeliveryHeader, "bad:delivery")
		}},
		{"ping cannot route unmanaged installation", http.StatusNoContent, func(f *githubIntakeFixture, r *http.Request) {
			f.app.ProviderAccountRef = "654"
			r.Header.Set(github.EventHeader, "ping")
		}},
		{"caller cancellation", http.StatusServiceUnavailable, func(f *githubIntakeFixture, _ *http.Request) {
			f.commit = func(context.Context) error { return context.Canceled }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubIntakeFixture()
			r := githubIntakeRequest(t, f, githubIntakeBody)
			tc.edit(f, r)
			w := httptest.NewRecorder()
			(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).ServeHTTP(w, r)
			if w.Code != tc.status || len(f.accepted) != 0 {
				t.Fatalf("response: %d %s receipts=%d", w.Code, w.Body.String(), len(f.accepted))
			}
			if strings.Contains(w.Body.String(), "webhook-secret") || strings.Contains(w.Body.String(), "private-key") {
				t.Fatalf("credential leak: %s", w.Body.String())
			}
		})
	}
}

func TestGitHubHTTPIntakePingIsSignedAndAppScoped(t *testing.T) {
	t.Parallel()
	for _, signed := range []bool{false, true} {
		f := newGitHubIntakeFixture()
		r := githubIntakeRequest(t, f, `{"zen":"Keep it logically awesome","hook":{"id":123}}`)
		r.Header.Set(github.EventHeader, "ping")
		if !signed {
			r.Header.Del(github.SignatureHeader)
		}
		w := httptest.NewRecorder()
		(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).ServeHTTP(w, r)
		if signed && (w.Code != http.StatusNoContent || len(f.accepted) != 0) ||
			!signed && (w.Code != http.StatusUnauthorized || len(f.accepted) != 0) {
			t.Fatalf("ping signed=%v status=%d receipts=%d", signed, w.Code, len(f.accepted))
		}
	}
}

func TestGitHubHTTPIntakeBoundsAndWiring(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	h := &githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{strings.Repeat(" ", integrationstore.IntegrationInboxMaxPayloadBytes+1), http.StatusRequestEntityTooLarge},
		{"{", http.StatusBadRequest},
		{"null", http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, githubIntakeRequest(t, f, tc.body))
		if w.Code != tc.status || len(f.accepted) != 0 {
			t.Fatalf("status=%d want=%d receipts=%d", w.Code, tc.status, len(f.accepted))
		}
	}
	r := githubIntakeRequest(t, f, githubIntakeBody)
	r.Method = http.MethodGet
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method: %d", w.Code)
	}
	w = httptest.NewRecorder()
	(&Server{}).GitHubEventsHandler().ServeHTTP(w, githubIntakeRequest(t, f, githubIntakeBody))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured server: %d", w.Code)
	}
}

func TestGitHubHTTPMountedRouteUsesProviderAuthentication(t *testing.T) {
	t.Parallel()
	server, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/integrations/github/123/events"
	if requiresAuth(path) {
		t.Fatal("GitHub callbacks must not require a bearer token or browser session")
	}
	classified := false
	for _, route := range serverManualRouteContracts {
		if route.Pattern == GitHubEventsPath && route.Method == http.MethodPost {
			classified = route.Access == manualRouteAccessProviderSigned
		}
	}
	if !classified {
		t.Fatal("GitHub route must declare provider-signed access")
	}
	// The actual server stack must reach the mounted provider handler. A missing
	// store yields its 503, not bearer authentication, OpenAPI validation or 404.
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(githubIntakeBody))
	r.Header.Set("Authorization", "Bearer deliberately-invalid")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "GitHub intake unavailable") {
		t.Fatalf("mounted route: %d %s", w.Code, w.Body.String())
	}
}

func TestGitHubHTTPAppURLRoutesSeparateInstallationProjects(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	second := f.app
	second.ID, second.ProjectID, second.ProviderAccountRef = uuid.New(), uuid.New(), "789"
	f.appsByIdentity = map[string]integrationstore.ProjectAppRecord{
		"github:123:456": f.app,
		"github:123:789": second,
	}
	h := &githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}
	mux := http.NewServeMux()
	mux.Handle("POST "+GitHubEventsPath, h)
	for _, installation := range []string{"456", "789"} {
		body := strings.Replace(githubIntakeBody, `"id":456`, `"id":`+installation, 1)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, githubIntakeRequest(t, f, body))
		if w.Code != http.StatusNoContent {
			t.Fatalf("installation %s: %d %s", installation, w.Code, w.Body.String())
		}
	}
	if len(f.accepted) != 2 || f.accepted[0].AppID != f.app.ID ||
		f.accepted[0].ProjectID != f.app.ProjectID || f.accepted[1].AppID != second.ID ||
		f.accepted[1].ProjectID != second.ProjectID || f.appLookups != 0 {
		t.Fatalf("cross-project routing: %+v, App lookups=%d", f.accepted, f.appLookups)
	}
	if f.secretRead.ProjectID != second.ProjectID || f.secretRead.SecretID != second.CredentialSecretID {
		t.Fatalf("shared credential was not read in receiving project's grant scope: %+v", f.secretRead)
	}
	// Disabling one app never becomes an App-wide credential anchor.
	first := f.app
	first.State = integrationstore.ProjectAppStateDisconnected
	f.appsByIdentity["github:123:456"] = first
	for _, installation := range []string{"456", "789"} {
		body := strings.Replace(githubIntakeBody, `"id":456`, `"id":`+installation, 1)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, githubIntakeRequest(t, f, body))
		if w.Code != http.StatusNoContent {
			t.Fatalf("after disable, installation %s: %d", installation, w.Code)
		}
	}
	if len(f.accepted) != 3 || f.accepted[2].AppID != second.ID {
		t.Fatalf("disabled installation affected another: %+v", f.accepted)
	}
}

func TestGitHubHTTPAppPingAndUnmanagedInstallationDoNotChooseProject(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	f.app.State = integrationstore.ProjectAppStateDisconnected
	f.appsByIdentity = map[string]integrationstore.ProjectAppRecord{}
	h := &githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}
	for _, tc := range []struct{ eventType, body string }{
		{"ping", `{"zen":"Keep it logically awesome","hook":{"id":123}}`},
		{"installation", `{"action":"created","installation":{"id":789,"app_id":123}}`},
	} {
		w := httptest.NewRecorder()
		r := githubIntakeRequest(t, f, tc.body)
		r.Header.Set(github.EventHeader, tc.eventType)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent || len(f.accepted) != 0 {
			t.Fatalf("App probe created a project receipt: %d %+v", w.Code, f.accepted)
		}
	}
	if f.appLookups != 2 {
		t.Fatalf("App-level credential lookups=%d", f.appLookups)
	}
	// The same valid signature does not authorize a different App URL.
	w := httptest.NewRecorder()
	r := githubIntakeRequest(t, f, `{"zen":"Keep it logically awesome","hook":{"id":123}}`)
	r.Header.Set(github.EventHeader, "ping")
	r.SetPathValue("app_id", "999")
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-App credential selection: %d", w.Code)
	}
}

func TestGitHubHTTPAppCredentialResolutionIsBoundedAndRequired(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*githubIntakeFixture, *githubIntakeHandler)
	}{
		{"missing resolver", func(_ *githubIntakeFixture, h *githubIntakeHandler) { h.credentialApps = nil }},
		{"resolver failure", func(f *githubIntakeFixture, _ *githubIntakeHandler) {
			f.resolverErr = errors.New("private-key webhook-secret")
		}},
		{"too many candidates", func(f *githubIntakeFixture, _ *githubIntakeHandler) {
			f.candidates = make([]integrationstore.ProjectAppRecord, githubWebhookCredentialLimit+1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubIntakeFixture()
			h := &githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}
			tc.edit(f, h)
			r := githubIntakeRequest(t, f, `{"zen":"Keep it logically awesome","hook":{"id":123}}`)
			r.Header.Set(github.EventHeader, "ping")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable || f.secretRead != nil || len(f.accepted) != 0 ||
				strings.Contains(w.Body.String(), "webhook-secret") {
				t.Fatalf("unbounded/failed resolver: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGitHubHTTPFanoutPagesEveryIndependentApp(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	for range 205 {
		app := f.app
		app.ID, app.ProjectID = uuid.New(), uuid.New()
		// Even a shared secret must be read through each receiving project's grant.
		f.apps = append(f.apps, app)
	}
	h := &githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}
	for range 2 {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, githubIntakeRequest(t, f, githubIntakeBody))
		if w.Code != http.StatusNoContent {
			t.Fatalf("fanout: %d %s", w.Code, w.Body.String())
		}
	}
	if f.pages != 6 || len(f.accepted) != 410 || len(f.secretReads) != 410 || f.appLookups != 0 {
		t.Fatalf("pages=%d receipts=%d credential reads=%d fallback=%d",
			f.pages, len(f.accepted), len(f.secretReads), f.appLookups)
	}
	for i, input := range f.accepted {
		if input.ProjectID != f.secretReads[i].ProjectID || string(input.Payload) != githubIntakeBody {
			t.Fatalf("receipt escaped credential scope: %+v", input)
		}
	}
}

func TestGitHubHTTPFanoutIsolatesBadAppsAndRetriesTransientFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		secretErr, receiptErr error
		invalidSecret         bool
		invalidAppID          bool
		status                int
	}{
		{name: "revoked grant", secretErr: storeerr.ErrNotFound, status: http.StatusNoContent},
		{name: "denied grant", secretErr: storeerr.ErrUnauthorized, status: http.StatusNoContent},
		{name: "invalid credentials", invalidSecret: true, status: http.StatusNoContent},
		{name: "malformed credential identity", invalidAppID: true, status: http.StatusNoContent},
		{
			name: "unknown decrypt error", secretErr: errors.New("decrypt failed: secret-canary"),
			status: http.StatusServiceUnavailable,
		},
		{name: "transient secret", secretErr: errors.New("secret store unavailable"), status: http.StatusServiceUnavailable},
		{name: "transient receipt", receiptErr: errors.New("database unavailable"), status: http.StatusServiceUnavailable},
		{name: "disconnected during admission", receiptErr: storeerr.ErrUnauthorized, status: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubIntakeFixture()
			bad := f.app
			bad.ID, bad.ProjectID = uuid.New(), uuid.New()
			f.apps = []integrationstore.ProjectAppRecord{bad, f.app}
			f.secretErrors = map[uuid.UUID]error{bad.ProjectID: tc.secretErr}
			f.acceptErrors = map[uuid.UUID]error{bad.ID: tc.receiptErr}
			if tc.invalidSecret || tc.invalidAppID {
				appID := "123"
				if tc.invalidAppID {
					appID = "malformed-app-id"
				}
				f.credentials = map[uuid.UUID]secretstore.SecretPayloadRecord{bad.ProjectID: {Payload: secrets.Payload{
					secrets.KeyAppID: appID, secrets.KeyWebhookSecret: "another-app-secret",
				}}}
			}
			var logs bytes.Buffer
			r := githubIntakeRequest(t, f, githubIntakeBody)
			r = r.WithContext(log.WithLogger(r.Context(), slog.New(slog.NewJSONHandler(&logs, nil))))
			w := httptest.NewRecorder()
			(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).ServeHTTP(w, r)
			if w.Code != tc.status || len(f.accepted) != 1 || f.accepted[0].AppID != f.app.ID || f.appLookups != 0 {
				t.Fatalf("response=%d receipts=%+v fallback=%d", w.Code, f.accepted, f.appLookups)
			}
			if tc.secretErr != nil || tc.receiptErr != nil || tc.invalidAppID {
				var entry struct {
					AppID         uuid.UUID `json:"app_id"`
					ProjectID     uuid.UUID `json:"project_id"`
					SetupRevision int64     `json:"setup_revision"`
					Provider      string    `json:"provider"`
					Stage         string    `json:"stage"`
					Retryable     bool      `json:"retryable"`
					ErrorType     string    `json:"error_type"`
				}
				if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
					t.Fatalf("decode failed-app diagnostic: %v", err)
				}
				stage := "credential_verification"
				if tc.receiptErr != nil {
					stage = "intake"
				}
				if entry.AppID != bad.ID || entry.ProjectID != bad.ProjectID || entry.Provider != bad.Provider ||
					entry.SetupRevision != bad.SetupRevision || entry.Stage != stage || entry.ErrorType == "" ||
					entry.Retryable != (tc.status == http.StatusServiceUnavailable) {
					t.Fatalf("wrong failed-app diagnostic: %+v", entry)
				}
			}
			for _, private := range []string{"secret-canary", "another-app-secret", "malformed-app-id", "@helper hi"} {
				if strings.Contains(logs.String(), private) || strings.Contains(w.Body.String(), private) {
					t.Fatalf("diagnostics exposed private content %q", private)
				}
			}
		})
	}
}

func TestGitHubHTTPKnownInstallationNeverBorrowsFallbackCredentials(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	f.credentials = map[uuid.UUID]secretstore.SecretPayloadRecord{f.app.ProjectID: {Payload: secrets.Payload{
		secrets.KeyAppID: "123", secrets.KeyWebhookSecret: "other-secret",
	}}}
	w := httptest.NewRecorder()
	(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).
		ServeHTTP(w, githubIntakeRequest(t, f, githubIntakeBody))
	if w.Code != http.StatusUnauthorized || f.appLookups != 0 || len(f.accepted) != 0 {
		t.Fatalf("response=%d fallback=%d receipts=%+v", w.Code, f.appLookups, f.accepted)
	}
}

func TestGitHubSharedIntakeLookupHintRequiresVerification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		hints     []string
		signature bool
		want      int
		lookup    bool
	}{
		{"missing", nil, true, http.StatusBadRequest, false},
		{"duplicate", []string{"123", "123"}, true, http.StatusBadRequest, false},
		{"noncanonical", []string{"0123"}, true, http.StatusNotFound, false},
		{"comma separated", []string{"123,456"}, true, http.StatusNotFound, false},
		{"whitespace", []string{" 123 "}, true, http.StatusNotFound, false},
		{"negative", []string{"-1"}, true, http.StatusNotFound, false},
		{"zero", []string{"0"}, true, http.StatusNotFound, false},
		{"spoofed", []string{"789"}, true, http.StatusUnauthorized, true},
		{"unsigned", []string{"123"}, false, http.StatusUnauthorized, true},
		{"verified", []string{"123"}, true, http.StatusNoContent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubIntakeFixture()
			r := githubIntakeRequest(t, f, githubIntakeBody)
			r.SetPathValue("app_id", "")
			r.URL.Path = GitHubSharedEventsPath
			for _, hint := range tc.hints {
				r.Header.Add("X-Github-Hook-Installation-Target-Id", hint)
			}
			if !tc.signature {
				r.Header.Del(github.SignatureHeader)
			}
			w := httptest.NewRecorder()
			(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("response = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			if !tc.lookup && (f.pages != 0 || f.appLookups != 0 || len(f.secretReads) != 0) {
				t.Fatal("malformed hint reached credential lookup")
			}
			if tc.want != http.StatusNoContent && len(f.accepted) != 0 {
				t.Fatal("unverified hint admitted an event")
			}
		})
	}
}

func TestGitHubSharedIntakeFailsClosedBeforeConfiguration(t *testing.T) {
	t.Parallel()
	f := newGitHubIntakeFixture()
	f.apps = []integrationstore.ProjectAppRecord{}
	f.candidates = []integrationstore.ProjectAppRecord{}
	r := githubIntakeRequest(t, f, `{"zen":"bootstrap","hook":{"id":1}}`)
	r.SetPathValue("app_id", "")
	r.URL.Path = GitHubSharedEventsPath
	r.Header.Set("X-Github-Hook-Installation-Target-Id", "123")
	r.Header.Set(github.EventHeader, "ping")
	w := httptest.NewRecorder()
	(&githubIntakeHandler{store: f, secrets: f, credentialApps: f.credentialApps}).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || len(f.accepted) != 0 {
		t.Fatalf("early ping = %d %s, receipts %d", w.Code, w.Body.String(), len(f.accepted))
	}
}
