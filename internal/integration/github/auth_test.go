package github

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fixtureKey = sync.OnceValues(func() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) })

func testCredentials(t *testing.T) Credentials {
	t.Helper()
	key, err := fixtureKey()
	if err != nil {
		t.Fatal(err)
	}
	return Credentials{
		AppID: 123, WebhookSecret: "test-webhook-secret",
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		})),
	}
}

func testScope() Scope {
	return Scope{RepositoryID: 789, Owner: "octo-org", Repository: "repo.name_1-2", PullRequest: 42}
}

func testRepository() Repository {
	return Repository{ID: 789, Name: "repo.name_1-2", Owner: User{Login: "octo-org"}}
}

const testRepositoryListing = `{"total_count":1,
	"repositories":[{"id":789,"name":"repo.name_1-2","owner":{"login":"octo-org"}}]}`
const testPullJSON = `{"id":7,"number":42,"head":{"sha":"commit"},"base":{"repo":{"id":789}}}`

func preparationJSON(r *http.Request) string {
	if r.Method != http.MethodGet {
		return ""
	}
	if r.URL.Path == "/installation/repositories" {
		return testRepositoryListing
	}
	if r.URL.Path == pullPath(testRepository(), 42) && r.Header.Get("Accept") == jsonMediaType {
		return testPullJSON
	}
	return ""
}

func servePreparedPull(w http.ResponseWriter, r *http.Request) bool {
	body := preparationJSON(r)
	if body == "" {
		return false
	}
	fmt.Fprint(w, body)
	return true
}

func withPreparedPull(handler http.HandlerFunc) http.HandlerFunc {
	return withToken(func(w http.ResponseWriter, r *http.Request) {
		if !servePreparedPull(w, r) {
			handler(w, r)
		}
	})
}

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{
		Credentials: testCredentials(t), InstallationID: 456, APIURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func tokenResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = fmt.Fprintf(w, `{"token":"installation-token","expires_at":%q}`,
		time.Now().Add(time.Hour).Format(time.RFC3339))
}

func withToken(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/456/access_tokens" {
			tokenResponse(w)
			return
		}
		if r.URL.Path == "/installation/repositories" {
			fmt.Fprint(w, testRepositoryListing)
			return
		}
		handler(w, r)
	}
}

func TestAppSigningAndInstallationHeaders(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	var tokens atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Github-Api-Version") != APIVersion ||
			r.Header.Get("Accept") != jsonMediaType || r.Header.Get("User-Agent") == "" {
			t.Error("missing GitHub headers")
		}
		if r.URL.Path == "/app/installations/456/access_tokens" {
			tokens.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Error("invalid token request method or content type")
			}
			jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			parts := strings.Split(jwt, ".")
			if len(parts) != 3 {
				t.Error("invalid JWT structure")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			header, _ := base64.RawURLEncoding.DecodeString(parts[0])
			if string(header) != `{"alg":"RS256","typ":"JWT"}` {
				t.Error("JWT must use RS256")
			}
			data, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				IAT int64  `json:"iat"`
				EXP int64  `json:"exp"`
				ISS string `json:"iss"`
			}
			if json.Unmarshal(data, &claims) != nil || claims.ISS != "123" ||
				claims.IAT != now.Add(-time.Minute).Unix() || claims.EXP != now.Add(9*time.Minute).Unix() {
				t.Error("invalid JWT clock or issuer claims")
			}
			key, _ := fixtureKey()
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
				t.Error("invalid JWT signature")
			}
			var input struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.RepositoryIDs) != 1 ||
				input.RepositoryIDs[0] != testScope().RepositoryID || len(input.Permissions) != 1 ||
				input.Permissions["pull_requests"] != "read" {
				t.Error("installation token is not least-privilege")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"installation-token","expires_at":%q}`, now.Add(time.Hour).Format(time.RFC3339))
			return
		}
		if r.Header.Get("Authorization") != "Bearer installation-token" {
			t.Error("incorrect installation authorization")
		}
		if r.URL.Path == "/installation/repositories" {
			fmt.Fprint(w, testRepositoryListing)
			return
		}
		fmt.Fprint(w, testPullJSON)
	})
	client.now = func() time.Time { return now }
	for range 2 {
		pr, err := client.GetPullRequest(t.Context(), testScope())
		if err != nil || pr.Head.SHA != "commit" {
			t.Fatalf("PR = %+v, error = %v", pr, err)
		}
	}
	if tokens.Load() != 1 {
		t.Fatal("installation token was not cached")
	}
}

func TestTokenCachePermissionsExpiryAndRepository(t *testing.T) {
	now := time.Now()
	var permissions []string
	var repositories []int64
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		permissions = append(permissions, input.Permissions["pull_requests"])
		repositories = append(repositories, input.RepositoryIDs[0])
		fmt.Fprintf(w, `{"token":"token-%d","expires_at":%q}`, len(permissions), now.Add(time.Hour).Format(time.RFC3339))
	})
	client.now = func() time.Time { return now }
	scope := testScope()
	for _, write := range []bool{false, false, true, false, true} {
		if _, err := client.installationToken(t.Context(), scope.RepositoryID, write); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(59*time.Minute + time.Second)
	if _, err := client.installationToken(t.Context(), scope.RepositoryID, false); err != nil {
		t.Fatal(err)
	}
	scope.RepositoryID = 790
	if _, err := client.installationToken(t.Context(), scope.RepositoryID, false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(permissions, ",") != "read,write,read,read" || repositories[3] != 790 {
		t.Fatalf("token grants = %v, repos = %v", permissions, repositories)
	}
}

func TestTokenCacheConcurrentAndCancellable(t *testing.T) {
	var count atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		tokenResponse(w)
	})
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if _, err := client.installationToken(t.Context(), testScope().RepositoryID, false); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if count.Load() != 1 {
		t.Fatalf("minted %d tokens", count.Load())
	}
	client.tokenGate <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err := client.installationToken(ctx, testScope().RepositoryID, false)
	<-client.tokenGate
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cache wait did not respect context: %v", err)
	}
}

func TestPrivateKeyFormatsAndInvalidCredentials(t *testing.T) {
	credentials := testCredentials(t)
	key, _ := fixtureKey()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{credentials.PrivateKeyPEM, string(pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: pkcs8,
	}))} {
		if _, err := parsePrivateKey(value); err != nil {
			t.Fatal(err)
		}
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecBytes, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"", "secret-not-a-key", credentials.PrivateKeyPEM + "trailing-secret", "-----BEGIN PRIVATE KEY-----\nYWJj\n-----END PRIVATE KEY-----",
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecBytes})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkcs8})),
	} {
		if _, err := parsePrivateKey(value); err == nil || strings.Contains(err.Error(), "secret-not-a-key") {
			t.Error("expected sanitized invalid-key error")
		}
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Credentials.AppID = 0 },
		func(c *Config) { c.InstallationID = 0 },
		func(c *Config) { c.Credentials.WebhookSecret = "" },
	} {
		config := Config{Credentials: credentials, InstallationID: 1}
		mutate(&config)
		if _, err := NewClient(config); err == nil {
			t.Fatal("accepted invalid credentials")
		}
	}
}

func TestTokenResponsesAndRevocation(t *testing.T) {
	for _, response := range []string{
		`{}`, `{"token":"x","expires_at":"2000-01-01T00:00:00Z"}`,
		`{"token":"x","expires_at":"bad"}`, `{"token":"x\r\nInjected: true","expires_at":"2099-01-01T00:00:00Z"}`,
	} {
		t.Run(response, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) })
			if _, err := client.GetPullRequest(t.Context(), testScope()); err == nil {
				t.Fatal("accepted malformed token response")
			}
		})
	}
	var tokens atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			tokens.Add(1)
			tokenResponse(w)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	for range 2 {
		_, err := client.GetPullRequest(t.Context(), testScope())
		if err == nil {
			t.Fatal("expected revoked-token error")
		}
	}
	if tokens.Load() != 2 {
		t.Fatal("401 did not invalidate the cached token for the next operation")
	}
}
