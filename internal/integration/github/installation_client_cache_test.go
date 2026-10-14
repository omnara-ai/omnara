package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInstallationClientCacheIdentityAndReuse(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gitCredentialResponse(w, "cached-credential", "read", time.Now().Add(time.Hour))
	}))
	defer server.Close()
	cache, err := NewInstallationClientCache(InstallationClientCacheConfig{
		Capacity: 8, HTTPClient: server.Client(), APIURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials := testCredentials(t)
	secretID, versionID := uuid.New(), uuid.New()
	first, err := cache.Client(secretID, versionID, 456, credentials)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := first.InstallationGitCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	second, err := cache.Client(secretID, versionID, 456, credentials)
	if err != nil || second != first || second.beforeRequest != nil {
		t.Fatalf("client was not reused without a request authority closure: %v", err)
	}
	if _, err := second.InstallationGitCredentials(t.Context()); err != nil || calls.Load() != 1 {
		t.Fatalf("new request did not reuse credentials after original context ended: %v", err)
	}
	for _, dimension := range []string{"secret", "version", "installation", "app"} {
		t.Run(dimension, func(t *testing.T) {
			secret, version, installation, app := secretID, versionID, int64(456), credentials
			switch dimension {
			case "secret":
				secret = uuid.New()
			case "version":
				version = uuid.New()
			case "installation":
				installation++
			case "app":
				app.AppID++
			}
			client, err := cache.Client(secret, version, installation, app)
			if err != nil || client == first {
				t.Fatalf("client reused across %s: %v", dimension, err)
			}
			before := calls.Load()
			if _, err := client.InstallationGitCredentials(t.Context()); err != nil || calls.Load() != before+1 {
				t.Fatalf("credential reused across %s: %v", dimension, err)
			}
		})
	}
}

func TestInstallationClientCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache, err := NewInstallationClientCache(InstallationClientCacheConfig{Capacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	secretID, versionID := uuid.New(), uuid.New()
	credentials := testCredentials(t)
	get := func(installation int64) *Client {
		t.Helper()
		client, err := cache.Client(secretID, versionID, installation, credentials)
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	first, second := get(1), get(2)
	if get(1) != first {
		t.Fatal("cache miss without eviction")
	}
	get(3)
	if cache.clients.Len() != 2 || get(1) != first || get(2) == second {
		t.Fatal("cache did not bound clients or evict the least recently used entry")
	}
}

func TestInstallationClientCacheConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gitCredentialResponse(w, "shared-credential", "read", time.Now().Add(time.Hour))
	}))
	defer server.Close()
	cache, err := NewInstallationClientCache(InstallationClientCacheConfig{
		Capacity: 2, HTTPClient: server.Client(), APIURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	secretID, versionID := uuid.New(), uuid.New()
	credentials := testCredentials(t)
	var group sync.WaitGroup
	for range 24 {
		group.Go(func() {
			client, err := cache.Client(secretID, versionID, 456, credentials)
			if err != nil {
				t.Error(err)
				return
			}
			result, err := client.InstallationGitCredentials(t.Context())
			if err != nil || result.Token != "shared-credential" {
				t.Errorf("shared client did not issue credentials: %v", err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 || cache.clients.Len() != 1 {
		t.Fatal("concurrent requests did not share the client and token refresh")
	}
}

func TestInstallationClientCacheRejectsInvalidIdentityAndCredentials(t *testing.T) {
	for _, size := range []int{-1, 0} {
		if _, err := NewInstallationClientCache(InstallationClientCacheConfig{Capacity: size}); err == nil {
			t.Fatal("accepted unbounded cache capacity")
		}
	}
	cache, err := NewInstallationClientCache(InstallationClientCacheConfig{Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	credentials := testCredentials(t)
	secretID, versionID := uuid.New(), uuid.New()
	for _, dimension := range []string{"secret", "version", "installation", "app", "private key"} {
		t.Run(dimension, func(t *testing.T) {
			secret, version, installation, app := secretID, versionID, int64(456), credentials
			switch dimension {
			case "secret":
				secret = uuid.Nil
			case "version":
				version = uuid.Nil
			case "installation":
				installation = 0
			case "app":
				app.AppID = 0
			case "private key":
				app.PrivateKeyPEM = "invalid-private-key"
			}
			if client, err := cache.Client(secret, version, installation, app); err == nil || client != nil {
				t.Fatal("accepted invalid cache identity or credentials")
			}
			if cache.clients.Len() != 0 {
				t.Fatal("failed constructor cached a client")
			}
		})
	}
}
