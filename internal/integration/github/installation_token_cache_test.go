package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func sharedTokenConfig(t *testing.T, server *httptest.Server) Config {
	t.Helper()
	return Config{
		Credentials: testCredentials(t), InstallationID: 456,
		HTTPClient: server.Client(), APIURL: server.URL,
		CredentialSecretID: uuid.New(), CredentialVersionID: uuid.New(),
		BeforeRequest: func(context.Context) error { return nil },
	}
}

func TestSharedInstallationTokensIsolateIdentityRepositoryAndPermission(t *testing.T) {
	var mints atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var grant struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&grant); err != nil || len(grant.RepositoryIDs) != 1 ||
			len(grant.Permissions) != 2 || grant.Permissions["metadata"] != "read" {
			t.Error("token lost its repository or permission restriction")
		}
		fmt.Fprintf(w, `{"token":"token-%d","expires_at":%q}`, mints.Add(1), time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	otherOrigin := httptest.NewServer(handler)
	defer otherOrigin.Close()
	config := sharedTokenConfig(t, server)
	get := func(config Config, repo int64, write bool) string {
		t.Helper()
		client, err := NewClient(config)
		require.NoError(t, err)
		token, err := client.installationToken(t.Context(), repo, write)
		require.NoError(t, err)
		return token
	}
	first := get(config, 789, false)
	for range 10 {
		require.Equal(t, first, get(config, 789, false))
	}
	require.EqualValues(t, 1, mints.Load(), "fresh clients reuse the same grant")
	for _, dimension := range []string{"secret", "version", "app", "installation", "origin", "repository", "permission"} {
		t.Run(dimension, func(t *testing.T) {
			changed, repo, write := config, int64(789), false
			switch dimension {
			case "secret":
				changed.CredentialSecretID = uuid.New()
			case "version":
				changed.CredentialVersionID = uuid.New()
			case "app":
				changed.Credentials.AppID++
			case "installation":
				changed.InstallationID++
			case "origin":
				changed.APIURL = otherOrigin.URL
			case "repository":
				repo++
			case "permission":
				write = true
			}
			before := mints.Load()
			token := get(changed, repo, write)
			require.NotEqual(t, first, token)
			require.Equal(t, token, get(changed, repo, write))
			require.Equal(t, before+1, mints.Load())
		})
	}
}

func TestSharedInstallationTokensRefreshAndInvalidateOnlyRejectedValue(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var mints atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"token":"token-%d","expires_at":%q}`, mints.Add(1), now.Add(time.Hour).Format(time.RFC3339))
	}))
	defer server.Close()
	config := sharedTokenConfig(t, server)
	first, err := NewClient(config)
	require.NoError(t, err)
	second, err := NewClient(config)
	require.NoError(t, err)
	first.now, second.now = func() time.Time { return now }, func() time.Time { return now }
	old, err := first.installationToken(t.Context(), 789, false)
	require.NoError(t, err)
	now = now.Add(59*time.Minute + time.Second)
	fresh, err := second.installationToken(t.Context(), 789, false)
	require.NoError(t, err)
	require.NotEqual(t, old, fresh)
	first.invalidateToken(t.Context(), old)
	retained, err := first.installationToken(t.Context(), 789, false)
	require.NoError(t, err)
	require.Equal(t, fresh, retained, "a delayed 401 must not remove a newer token")
	require.EqualValues(t, 2, mints.Load())
	_, err = first.request(t.Context(), fresh, http.MethodGet, "/revoked", nil, &struct{}{})
	requireAPIError(t, err, PermanentFailure)
	replacement, err := second.installationToken(t.Context(), 789, false)
	require.NoError(t, err)
	require.NotEqual(t, fresh, replacement, "401 must invalidate across request clients")
	require.EqualValues(t, 3, mints.Load())
}

func TestSharedInstallationTokensConcurrentRefreshAndCanceledWaiter(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	var mints atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mints.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			tokenResponse(w)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer finish()
	config := sharedTokenConfig(t, server)
	clients := make([]*Client, 12)
	for i := range clients {
		var err error
		clients[i], err = NewClient(config)
		require.NoError(t, err)
	}
	done := make(chan error, len(clients))
	go func() { _, err := clients[0].installationToken(t.Context(), 789, false); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)
	go func() { _, err := clients[1].installationToken(ctx, 789, false); canceled <- err }()
	cancel()
	require.ErrorIs(t, <-canceled, context.Canceled)
	for _, client := range clients[1:] {
		go func() { _, err := client.installationToken(t.Context(), 789, false); done <- err }()
	}
	finish()
	for range clients {
		require.NoError(t, <-done)
	}
	require.EqualValues(t, 1, mints.Load())
}

func TestSharedInstallationTokensRetainEachRequestAuthority(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method == http.MethodPost {
			tokenResponse(w)
			return
		}
		servePreparedPull(w, r)
	}))
	defer server.Close()
	config := sharedTokenConfig(t, server)
	first, err := NewClient(config)
	require.NoError(t, err)
	_, err = first.GetPullRequest(t.Context(), testScope())
	require.NoError(t, err)
	before := requests.Load()
	denied := errors.New("this request lost authority")
	config.BeforeRequest = func(context.Context) error { return denied }
	second, err := NewClient(config)
	require.NoError(t, err)
	_, err = second.GetPullRequest(t.Context(), testScope())
	require.ErrorIs(t, err, denied)
	require.Equal(t, before, requests.Load(), "a warm token cannot borrow the first client's callback")
	_, err = first.GetPullRequest(t.Context(), testScope())
	require.NoError(t, err, "another request's denial must not replace the original callback")
}

func TestSharedInstallationTokensBoundedEvictionAndRetryAfterMintFailure(t *testing.T) {
	t.Parallel()
	cache, err := newInstallationTokenCache(2)
	require.NoError(t, err)
	var mints atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if mints.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		tokenResponse(w)
	}))
	defer server.Close()
	config := sharedTokenConfig(t, server)
	for i, repo := range []int64{789, 789, 790, 789, 791, 790} {
		client, err := NewClient(config)
		require.NoError(t, err)
		client.sharedTokens = cache
		_, err = client.installationToken(t.Context(), repo, false)
		if i == 0 {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	require.EqualValues(t, 5, mints.Load(), "failed mints are retryable and eviction follows recent use")
	require.Equal(t, 2, cache.entries.Len())
}

func TestSharedInstallationTokensRequireVersionedCredentialsAndAuthority(t *testing.T) {
	t.Parallel()
	config := Config{
		Credentials: testCredentials(t), InstallationID: 456,
		CredentialSecretID: uuid.New(), CredentialVersionID: uuid.New(),
		BeforeRequest: func(context.Context) error { return nil },
	}
	for _, field := range []string{"secret", "version", "authority"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			invalid := config
			switch field {
			case "secret":
				invalid.CredentialSecretID = uuid.Nil
			case "version":
				invalid.CredentialVersionID = uuid.Nil
			case "authority":
				invalid.BeforeRequest = nil
			}
			_, err := NewClient(invalid)
			require.Error(t, err)
		})
	}
}

func TestSharedInstallationTokensEvictionDuringMintDoesNotBlockOtherRepository(t *testing.T) {
	t.Parallel()
	cache, err := newInstallationTokenCache(1)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	var mints atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mints.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		tokenResponse(w)
	}))
	defer server.Close()
	defer finish()
	config := sharedTokenConfig(t, server)
	first, err := NewClient(config)
	require.NoError(t, err)
	second, err := NewClient(config)
	require.NoError(t, err)
	first.sharedTokens, second.sharedTokens = cache, cache
	done := make(chan error, 1)
	go func() { _, err := first.installationToken(t.Context(), 789, false); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = second.installationToken(ctx, 790, false)
	require.NoError(t, err, "an unrelated repository must not wait for the first refresh")
	finish()
	require.NoError(t, <-done)
	_, err = second.installationToken(t.Context(), 790, false)
	require.NoError(t, err)
	require.EqualValues(t, 2, mints.Load(), "an evicted refresh must not displace the newer cache entry")
	require.Equal(t, 1, cache.entries.Len())
}
