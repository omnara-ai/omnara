package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func gitCredentialResponse(w http.ResponseWriter, token, permission string, expires time.Time) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"token":%q,"expires_at":%q,"permissions":{"contents":%q,"pull_requests":"write"}}`,
		token, expires.Format(time.RFC3339), permission)
}

func TestInstallationGitCredentialsScopeCacheAndExpiry(t *testing.T) {
	for _, permission := range []string{"read", "write"} {
		t.Run(permission, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			var clock atomic.Int64
			clock.Store(now.Unix())
			var full, scoped atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/app/installations/456/access_tokens" ||
					!strings.HasPrefix(r.Header.Get("Authorization"), "Bearer eyJ") {
					t.Error("expected installation token issuance authenticated with an App JWT")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				expires := time.Unix(clock.Load(), 0).Add(time.Hour)
				if len(body) == 0 {
					gitCredentialResponse(w, fmt.Sprintf("full-%d", full.Add(1)), permission, expires)
					return
				}
				var input struct {
					RepositoryIDs []int64           `json:"repository_ids"`
					Permissions   map[string]string `json:"permissions"`
				}
				if json.Unmarshal(body, &input) != nil || len(input.RepositoryIDs) != 1 ||
					input.RepositoryIDs[0] != 789 || len(input.Permissions) != 2 ||
					input.Permissions["metadata"] != "read" {
					t.Error("PR token restrictions changed")
				}
				scoped.Add(1)
				fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`,
					"scoped-"+input.Permissions["pull_requests"], expires.Format(time.RFC3339))
			})
			client.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			first, err := client.InstallationGitCredentials(t.Context())
			if err != nil || first.Token != "full-1" || !first.ExpiresAt.Equal(now.Add(time.Hour)) {
				t.Fatalf("initial credential issuance failed: %v", err)
			}
			for _, write := range []bool{false, true} {
				if _, err := client.installationToken(t.Context(), 789, write); err != nil {
					t.Fatal(err)
				}
			}
			clock.Store(now.Add(44*time.Minute + 59*time.Second).Unix())
			cached, err := client.InstallationGitCredentials(t.Context())
			if err != nil || cached != first || full.Load() != 1 {
				t.Fatalf("credential cache miss before refresh margin: %v", err)
			}
			clock.Store(now.Add(45 * time.Minute).Unix())
			refreshed, err := client.InstallationGitCredentials(t.Context())
			if err != nil || refreshed.Token != "full-2" || full.Load() != 2 {
				t.Fatalf("credential was not refreshed at the 15-minute margin: %v", err)
			}
			for _, write := range []bool{false, true} {
				token, err := client.installationToken(t.Context(), 789, write)
				want := "scoped-read"
				if write {
					want = "scoped-write"
				}
				if err != nil || token != want || scoped.Load() != 2 {
					t.Fatalf("PR cache mixed with installation credentials or refreshed early: %v", err)
				}
			}
		})
	}
}

func TestInstallationGitCredentialsInvalidResponsesAndRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, token, expiry, permissions string
		contentsRequired                 bool
	}{
		{name: "empty token", expiry: now.Add(time.Hour).Format(time.RFC3339), permissions: `{"contents":"read"}`},
		{name: "blank token", token: " \t", expiry: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "newline token", token: "secret\npassword=injected", expiry: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "nul token", token: "secret\x00", expiry: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "missing expiry", token: "secret"},
		{name: "invalid expiry", token: "secret", expiry: "secret-invalid-date"},
		{name: "expired", token: "secret", expiry: now.Add(-time.Minute).Format(time.RFC3339)},
		{name: "short lifetime", token: "secret", expiry: now.Add(15 * time.Minute).Format(time.RFC3339)},
		{name: "missing permission", token: "secret", expiry: now.Add(time.Hour).Format(time.RFC3339),
			contentsRequired: true},
		{name: "no contents", token: "secret", expiry: now.Add(time.Hour).Format(time.RFC3339),
			permissions: `{"contents":"none","pull_requests":"write"}`, contentsRequired: true},
		{name: "unknown contents", token: "secret", expiry: now.Add(time.Hour).Format(time.RFC3339),
			permissions: `{"contents":"secret-invalid-permission"}`, contentsRequired: true},
		{name: "invalid permission type", token: "secret", expiry: now.Add(time.Hour).Format(time.RFC3339),
			permissions: `{"contents":123}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls, revoked atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" {
					revoked.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if calls.Add(1) > 1 {
					gitCredentialResponse(w, "recovered", "read", now.Add(time.Hour))
					return
				}
				permissions := tc.permissions
				if permissions == "" {
					permissions = `{}`
				}
				fmt.Fprintf(w, `{"token":%q,"expires_at":%q,"permissions":%s}`, tc.token, tc.expiry, permissions)
			})
			client.now = func() time.Time { return now }
			result, err := client.InstallationGitCredentials(t.Context())
			if err == nil || result != (InstallationCredentials{}) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid credential response was accepted or exposed: %v", err)
			}
			if tc.contentsRequired {
				if !errors.Is(err, ErrContentsPermissionRequired) || !strings.Contains(err.Error(), "approve") {
					t.Fatalf("expected actionable Contents approval error: %v", err)
				}
			} else {
				requireAPIError(t, err, InvalidResponse)
				if revoked.Load() != 0 {
					t.Fatal("malformed token response must not be used for revocation")
				}
			}
			result, err = client.InstallationGitCredentials(t.Context())
			if err != nil || result.Token != "recovered" || calls.Load() != 2 {
				t.Fatalf("invalid response was cached or prevented recovery: %v", err)
			}
		})
	}
}

func TestInstallationGitCredentialsRevokesMissingContents(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var minted, revoked atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/app/installations/456/access_tokens":
					if minted.Add(1) == 1 {
						gitCredentialResponse(w, "unused-installation-token", "none", time.Now().Add(time.Hour))
					} else {
						gitCredentialResponse(w, "usable-installation-token", "read", time.Now().Add(time.Hour))
					}
				case r.Method == http.MethodDelete && r.URL.Path == "/installation/token":
					revoked.Add(1)
					if r.Header.Get("Authorization") != "Bearer unused-installation-token" {
						t.Error("revocation must authenticate with the rejected installation token")
					}
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(status)
				default:
					t.Error("unexpected credential request")
					w.WriteHeader(http.StatusNotFound)
				}
			})
			result, err := client.InstallationGitCredentials(t.Context())
			if !errors.Is(err, ErrContentsPermissionRequired) || result != (InstallationCredentials{}) ||
				client.gitCredentials != (InstallationCredentials{}) {
				t.Fatalf("cleanup changed the permission error or retained the rejected token: %v", err)
			}
			if minted.Load() != 1 || revoked.Load() != 1 {
				t.Fatal("rejected token must receive exactly one best-effort revocation attempt")
			}
			for range 2 {
				result, err = client.InstallationGitCredentials(t.Context())
				if err != nil || result.Token != "usable-installation-token" {
					t.Fatalf("credential issuance did not recover after permission approval: %v", err)
				}
			}
			if minted.Load() != 2 || revoked.Load() != 1 {
				t.Fatal("usable credentials must be cached without being revoked")
			}
		})
	}
}

func TestInstallationGitCredentialsProviderErrorsDoNotLeak(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"message":%q,"token":"secret-provider-token"}`, r.Header.Get("Authorization"))
			})
			for range 2 {
				result, err := client.InstallationGitCredentials(t.Context())
				if err == nil || result != (InstallationCredentials{}) ||
					strings.Contains(err.Error(), "secret-provider-token") || strings.Contains(err.Error(), "eyJ") {
					t.Fatalf("provider error was accepted or exposed sensitive values: %v", err)
				}
			}
			if calls.Load() != 2 {
				t.Fatal("token issuance errors must not be cached or automatically retried")
			}
		})
	}
}

func TestInstallationGitCredentialsConcurrentRefresh(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gitCredentialResponse(w, "shared-token", "write", time.Now().Add(time.Hour))
	})
	var group sync.WaitGroup
	for range 24 {
		group.Go(func() {
			result, err := client.InstallationGitCredentials(t.Context())
			if err != nil || result.Token != "shared-token" {
				t.Errorf("concurrent credential issuance failed: %v", err)
			}
		})
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent callers minted %d tokens", calls.Load())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := client.InstallationGitCredentials(ctx)
	if !errors.Is(err, context.Canceled) || result != (InstallationCredentials{}) {
		t.Fatalf("canceled caller received cached credentials: %v", err)
	}
}

func TestInstallationGitCredentialsRefreshCancellation(t *testing.T) {
	for _, cancelRefresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_refresh=%t", cancelRefresh), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				gitCredentialResponse(w, "available", "read", time.Now().Add(time.Hour))
			})
			ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			refreshCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			refreshed := make(chan error, 1)
			go func() {
				_, err := client.InstallationGitCredentials(refreshCtx)
				refreshed <- err
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("refresh did not start")
			}
			if cancelRefresh {
				cancel()
				if err := <-refreshed; !errors.Is(err, context.Canceled) {
					t.Fatalf("refresh cancellation was lost: %v", err)
				}
			} else {
				waitCtx, cancelWait := context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancelWait()
				result, err := client.InstallationGitCredentials(waitCtx)
				if !errors.Is(err, context.DeadlineExceeded) || result != (InstallationCredentials{}) {
					t.Fatalf("waiting caller was not canceled: %v", err)
				}
				close(release)
				if err := <-refreshed; err != nil {
					t.Fatalf("waiter cancellation interrupted refresh: %v", err)
				}
			}
			result, err := client.InstallationGitCredentials(ctx)
			wantCalls := int32(1)
			if cancelRefresh {
				wantCalls = 2
			}
			if err != nil || result.Token != "available" || calls.Load() != wantCalls {
				t.Fatalf("refresh did not recover after cancellation: %v", err)
			}
		})
	}
}
