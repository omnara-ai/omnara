package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const setupAppJSON = `{"id":123,"slug":"helper","name":"Helper Bot",` +
	`"owner":{"id":888,"login":"octo-org","type":"Organization"}}`
const setupInstallationJSON = `{"id":456,"app_id":123,"account":{"id":888,"login":"octo-org","type":"Organization"}}`

const setupEnterpriseAccountJSON = `{"id":777,"node_id":"enterprise-node","slug":"octo-business",` +
	`"name":"Octo Business","html_url":"https://untrusted.example/enterprise",` +
	`"avatar_url":"https://untrusted.example/avatar",` +
	`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
const setupEnterpriseAppJSON = `{"id":123,"slug":"helper","name":"Helper Bot",` +
	`"owner":` + setupEnterpriseAccountJSON + `}`
const setupEnterpriseInstallationJSON = `{"id":789,"app_id":123,"target_type":"Enterprise",` +
	`"account":` + setupEnterpriseAccountJSON + `}`

func testSetupClient(t *testing.T, credentials Credentials, handler http.HandlerFunc) (*SetupClient, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewSetupClient(SetupConfig{
		Credentials: credentials, APIURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func manifestFixture(t *testing.T, changes map[string]any) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(setupAppJSON), &body); err != nil {
		t.Fatal(err)
	}
	credential := testCredentials(t)
	body["pem"], body["webhook_secret"] = credential.PrivateKeyPEM, credential.WebhookSecret
	body["client_secret"] = "unused-oauth-secret"
	for key, value := range changes {
		body[key] = value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func checkSetupJWT(t *testing.T, r *http.Request) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		t.Error("setup read requires an App JWT")
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Issuer != "123" {
		t.Error("setup JWT has wrong App identity")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	key, keyErr := fixtureKey()
	if err != nil || keyErr != nil {
		t.Error("invalid signing fixture")
		return
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
		t.Error("setup JWT signature is invalid")
	}
	if r.Header.Get("X-Github-Api-Version") != APIVersion || r.Header.Get("Accept") != jsonMediaType {
		t.Error("setup requests must use versioned GitHub JSON")
	}
}

func TestSetupConvertManifest(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(fmt.Sprint(authenticated), func(t *testing.T) {
			body := manifestFixture(t, nil)
			var credentials Credentials
			if authenticated {
				credentials = testCredentials(t)
			}
			var calls atomic.Int32
			client, _ := testSetupClient(t, credentials, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/manifest-code/conversions" ||
					r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
					t.Error("conversion must make one unauthenticated code exchange")
				}
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, body)
			})
			client.beforeRequest = func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > OperationTimeout {
					t.Error("manifest exchange must share a bounded authority context")
				}
				return nil
			}
			result, err := client.ConvertManifest(t.Context(), "manifest-code")
			if err != nil || result.Credentials != testCredentials(t) || result.App.ID != 123 ||
				result.App.Name != "Helper Bot" || result.App.Owner.Login != "octo-org" || calls.Load() != 1 {
				t.Fatalf("conversion failed: %v; calls=%d", err, calls.Load())
			}
			publicMetadata, err := json.Marshal(result.App)
			if err != nil || strings.Contains(string(publicMetadata), "secret") ||
				strings.Contains(string(publicMetadata), "PRIVATE KEY") {
				t.Error("App metadata must not contain conversion credentials")
			}
		})
	}
}

func TestSetupConversionRejectsInvalidOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]any
	}{
		{"null webhook secret", map[string]any{"webhook_secret": nil}},
		{"empty webhook secret", map[string]any{"webhook_secret": ""}},
		{"invalid key", map[string]any{"pem": "private-provider-key"}},
		{"missing App ID", map[string]any{"id": 0}},
		{"invalid slug", map[string]any{"slug": "../other"}},
		{"missing owner", map[string]any{"owner": nil}},
		{"missing name", map[string]any{"name": ""}},
		{"enterprise owner with empty secret", map[string]any{
			"owner": json.RawMessage(setupEnterpriseAccountJSON), "webhook_secret": "",
		}},
		{"enterprise owner with invalid key", map[string]any{
			"owner": json.RawMessage(setupEnterpriseAccountJSON), "pem": "private-provider-key",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := manifestFixture(t, tc.changes)
			client, _ := testSetupClient(t, Credentials{}, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, body)
			})
			result, err := client.ConvertManifest(t.Context(), "code")
			requireAPIError(t, err, InvalidResponse)
			if result != (ManifestConversion{}) || strings.Contains(err.Error(), "private-provider") ||
				strings.Contains(err.Error(), "unused-oauth-secret") {
				t.Error("invalid conversion returned credential material")
			}
		})
	}
}

func TestSetupConversionPreservesCredentialsForUnsupportedOwner(t *testing.T) {
	body := manifestFixture(t, map[string]any{"owner": json.RawMessage(setupEnterpriseAccountJSON)})
	var calls atomic.Int32
	client, _ := testSetupClient(t, testCredentials(t), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/app-manifests/code/conversions":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, body)
		case "/app":
			checkSetupJWT(t, r)
			fmt.Fprint(w, setupEnterpriseAppJSON)
		default:
			t.Error("unexpected provider request")
			w.WriteHeader(http.StatusNotFound)
		}
	})
	result, err := client.ConvertManifest(t.Context(), "code")
	if err != nil || result.Credentials != testCredentials(t) || result.App.ID != 123 ||
		result.App.Owner.ID != 777 || result.App.Owner.Login != "" || result.App.Owner.Type != "" {
		t.Fatalf("valid one-time credentials must remain recoverable: %v", err)
	}
	app, err := client.App(t.Context())
	requireAPIError(t, err, UnsupportedAccount)
	if app != (AppMetadata{}) || calls.Load() != 2 {
		t.Fatal("credential recovery must not enable unsupported owner inspection or retry")
	}
}

func TestSetupConversionFailuresDoNotRetryOrLeak(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		code       ErrorCode
	}{
		{"rate limited", "private-provider-body", 429, RateLimited},
		{"provider failure", "private-provider-body", 503, DeliveryUnknown},
		{"invalid JSON", "private-provider-body", 201, DeliveryUnknown},
		{"oversize response", strings.Repeat("x", ResponseMaxBytes+1), 201, DeliveryUnknown},
		{"redirect", "private-provider-body", 302, PermanentFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testSetupClient(t, Credentials{}, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "http://"+r.Host+"/must-not-follow")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := client.ConvertManifest(t.Context(), "private-code")
			requireAPIError(t, err, tc.code)
			if calls.Load() != 1 || strings.Contains(err.Error(), "private-") {
				t.Fatalf("retried or leaked exchange: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestSetupConversionLostResponseIsNotRetried(t *testing.T) {
	for _, partialResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(partialResponse), func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testSetupClient(t, Credentials{}, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if partialResponse {
					w.Header().Set("Content-Length", "1024")
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, `{"pem":"private-provider-key"`)
					return
				}
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Error("fixture requires HTTP connection hijacking")
					return
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			})
			result, err := client.ConvertManifest(t.Context(), "private-code")
			requireAPIError(t, err, DeliveryUnknown)
			if calls.Load() != 1 || result != (ManifestConversion{}) || strings.Contains(err.Error(), "private-") {
				t.Fatalf("lost response retried or leaked credentials: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestSetupRejectsMissingCredentialsAndUnsafeCodesBeforeIO(t *testing.T) {
	var calls atomic.Int32
	client, _ := testSetupClient(t, Credentials{}, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := client.App(t.Context()); err == nil {
		t.Error("App read accepted missing credentials")
	}
	if _, err := client.ListInstallations(t.Context(), PageOptions{}); err == nil {
		t.Error("installation read accepted missing credentials")
	}
	for _, code := range []string{
		"", "../other", "a/b", "a%2Fb", "code?token=secret", "code\r\n", strings.Repeat("a", 513),
	} {
		if _, err := client.ConvertManifest(t.Context(), code); err == nil {
			t.Error("unsafe manifest code accepted")
		}
	}
	credentials := testCredentials(t)
	credentials.WebhookSecret = ""
	if _, err := NewSetupClient(SetupConfig{Credentials: credentials}); err == nil {
		t.Error("partial credentials accepted an empty webhook secret")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid setup performed provider I/O")
	}
}

func TestSetupAppVerifiesIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       ErrorCode
	}{
		{"valid", setupAppJSON, ""},
		{"personal", strings.Replace(setupAppJSON, `"Organization"`, `"User"`, 1), ""},
		{"wrong App", strings.Replace(setupAppJSON, `"id":123`, `"id":321`, 1), ScopeMismatch},
		{"wrong owner type", strings.Replace(setupAppJSON, `"Organization"`, `"Bot"`, 1), InvalidResponse},
		{"unsafe owner", strings.Replace(setupAppJSON, `"octo-org"`, `"../other"`, 1), InvalidResponse},
		{"enterprise owner", setupEnterpriseAppJSON, UnsupportedAccount},
		{"wrong enterprise App", strings.Replace(setupEnterpriseAppJSON, `"id":123`, `"id":321`, 1), ScopeMismatch},
		{"malformed enterprise owner", strings.Replace(setupEnterpriseAppJSON, `"octo-business"`, `""`, 1), InvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testSetupClient(t, testCredentials(t), func(w http.ResponseWriter, r *http.Request) {
				checkSetupJWT(t, r)
				if r.Method != http.MethodGet || r.URL.Path != "/app" {
					t.Error("App inspection must not request installation tokens")
				}
				fmt.Fprint(w, tc.body)
			})
			app, err := client.App(t.Context())
			if tc.code != "" {
				requireAPIError(t, err, tc.code)
				if app != (AppMetadata{}) {
					t.Error("unverified App metadata returned")
				}
			} else if err != nil || app.Slug != "helper" || app.Owner.ID != 888 {
				t.Fatalf("App inspection: %+v %v", app, err)
			}
		})
	}
}

func TestSetupListsOneVerifiedPageAtATime(t *testing.T) {
	var calls atomic.Int32
	client, _ := testSetupClient(t, testCredentials(t), func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		checkSetupJWT(t, r)
		if r.Method != http.MethodGet || r.URL.Path != "/app/installations" || r.URL.Query().Get("per_page") != "1" {
			t.Error("unexpected installation discovery request")
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<http://`+r.Host+`/app/installations?page=2&per_page=1>; rel="next"`)
			fmt.Fprint(w, "["+setupInstallationJSON+"]")
			return
		}
		fmt.Fprint(w, `[]`)
	})
	page, err := client.ListInstallations(t.Context(), PageOptions{PerPage: 1})
	if err != nil || page.NextPage != 2 || len(page.Installations) != 1 ||
		page.Installations[0].ID != 456 || calls.Load() != 1 {
		t.Fatalf("first page: %+v %v requests=%d", page, err, calls.Load())
	}
	page, err = client.ListInstallations(t.Context(), PageOptions{Page: page.NextPage, PerPage: 1})
	if err != nil || page.NextPage != 0 || page.Installations == nil || len(page.Installations) != 0 || calls.Load() != 2 {
		t.Fatalf("empty last page: %+v %v requests=%d", page, err, calls.Load())
	}
}

func TestSetupRejectsUnverifiedInstallationPages(t *testing.T) {
	for _, tc := range []struct {
		name, body, link string
		code             ErrorCode
	}{
		{"null page", "null", "", InvalidResponse},
		{"too many", "[" + setupInstallationJSON + "," + setupInstallationJSON + "]", "", InvalidResponse},
		{
			"wrong App", "[" + strings.Replace(setupInstallationJSON, `"app_id":123`, `"app_id":321`, 1) + "]",
			"", ScopeMismatch,
		},
		{"missing account", `[{"id":456,"app_id":123}]`, "", InvalidResponse},
		{"cross origin", "[]", `<https://evil.example/app/installations?page=2&per_page=1>; rel="next"`, InvalidResponse},
		{"wrong path", "[]", `<$BASE/other?page=2&per_page=1>; rel="next"`, InvalidResponse},
		{"skipped page", "[]", `<$BASE/app/installations?page=3&per_page=1>; rel="next"`, InvalidResponse},
		{"changed size", "[]", `<$BASE/app/installations?page=2&per_page=100>; rel="next"`, InvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testSetupClient(t, testCredentials(t), func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", strings.ReplaceAll(tc.link, "$BASE", "http://"+r.Host))
				fmt.Fprint(w, tc.body)
			})
			page, err := client.ListInstallations(t.Context(), PageOptions{PerPage: 1})
			requireAPIError(t, err, tc.code)
			if page.Installations != nil || page.NextPage != 0 {
				t.Error("invalid page returned candidates")
			}
		})
	}
}

func TestSetupEnterpriseInstallationRejectsWholePage(t *testing.T) {
	for _, tc := range []struct {
		name, records string
		code          ErrorCode
	}{
		{"enterprise only", setupEnterpriseInstallationJSON, UnsupportedAccount},
		{"supported first", setupInstallationJSON + "," + setupEnterpriseInstallationJSON, UnsupportedAccount},
		{"enterprise first", setupEnterpriseInstallationJSON + "," + setupInstallationJSON, UnsupportedAccount},
		{
			"wrong App", strings.Replace(setupEnterpriseInstallationJSON, `"app_id":123`, `"app_id":321`, 1),
			ScopeMismatch,
		},
		{
			"invalid installation ID", strings.Replace(setupEnterpriseInstallationJSON, `"id":789`, `"id":0`, 1),
			InvalidResponse,
		},
		{"missing enterprise ID", strings.Replace(setupEnterpriseInstallationJSON, `"id":777`, `"id":0`, 1), InvalidResponse},
		{
			"unsafe enterprise slug", strings.Replace(setupEnterpriseInstallationJSON, `"octo-business"`, `"../other"`, 1),
			InvalidResponse,
		},
		{
			"unknown account type", strings.Replace(setupInstallationJSON, `"Organization"`, `"Enterprise"`, 1),
			InvalidResponse,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testSetupClient(t, testCredentials(t), func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				checkSetupJWT(t, r)
				w.Header().Set("Link", `<http://`+r.Host+`/app/installations?page=2&per_page=2>; rel="next"`)
				fmt.Fprint(w, "["+tc.records+"]")
			})
			page, err := client.ListInstallations(t.Context(), PageOptions{PerPage: 2})
			requireAPIError(t, err, tc.code)
			if page.Installations != nil || page.NextPage != 0 || calls.Load() != 1 {
				t.Fatal("unsupported or malformed page must not return partial candidates or retry")
			}
		})
	}
}

func TestSetupAuthorityAndCancellation(t *testing.T) {
	var calls atomic.Int32
	client, _ := testSetupClient(t, testCredentials(t), func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	var checks int
	client.beforeRequest = func(ctx context.Context) error {
		checks++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > OperationTimeout {
			t.Error("setup authority hook has no operation deadline")
		}
		if checks == 2 {
			return context.Canceled
		}
		return nil
	}
	_, err := client.App(t.Context())
	if !errors.Is(err, context.Canceled) || checks != 2 || calls.Load() != 1 {
		t.Fatalf("retry bypassed current authority: %v checks=%d calls=%d", err, checks, calls.Load())
	}
	client.beforeRequest = func(context.Context) error { return context.Canceled }
	_, err = client.ConvertManifest(t.Context(), "code")
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("exchange bypassed revoked authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.ListInstallations(ctx, PageOptions{})
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("listing bypassed cancellation")
	}
}
