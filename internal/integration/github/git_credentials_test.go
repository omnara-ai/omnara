package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
			var git, scoped atomic.Int32
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
				var input struct {
					RepositoryIDs []int64           `json:"repository_ids"`
					Permissions   map[string]string `json:"permissions"`
				}
				if json.Unmarshal(body, &input) != nil {
					t.Error("invalid token request body")
					return
				}
				if input.RepositoryIDs == nil {
					gitCredentialResponse(w, fmt.Sprintf("git-%d", git.Add(1)), permission, expires)
					return
				}
				if len(input.RepositoryIDs) != 1 || input.RepositoryIDs[0] != 789 || len(input.Permissions) != 2 ||
					input.Permissions["metadata"] != "read" {
					t.Error("PR token restrictions changed")
				}
				scoped.Add(1)
				fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`,
					"scoped-"+input.Permissions["pull_requests"], expires.Format(time.RFC3339))
			})
			client.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			first, err := client.InstallationGitCredentials(t.Context(), true, nil)
			if err != nil || first.Token != "git-1" || !first.ExpiresAt.Equal(now.Add(time.Hour)) {
				t.Fatalf("initial credential issuance failed: %v", err)
			}
			for _, write := range []bool{false, true} {
				if _, err := client.installationToken(t.Context(), 789, write); err != nil {
					t.Fatal(err)
				}
			}
			clock.Store(now.Add(44*time.Minute + 59*time.Second).Unix())
			cached, err := client.InstallationGitCredentials(t.Context(), true, nil)
			if err != nil || cached != first || git.Load() != 1 {
				t.Fatalf("credential cache miss before refresh margin: %v", err)
			}
			clock.Store(now.Add(45 * time.Minute).Unix())
			refreshed, err := client.InstallationGitCredentials(t.Context(), true, nil)
			if err != nil || refreshed.Token != "git-2" || git.Load() != 2 {
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

func TestInstallationGitCredentialsReadOnlyUnlessWriteGranted(t *testing.T) {
	var readOnly, writable atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var input map[string]map[string]string
		var contents string
		if json.Unmarshal(body, &input) == nil && len(input) == 1 && len(input["permissions"]) == 2 &&
			input["permissions"]["metadata"] == "read" {
			contents = input["permissions"]["contents"]
		}
		switch contents {
		case "read":
			gitCredentialResponse(w, fmt.Sprintf("read-%d", readOnly.Add(1)), "read", time.Now().Add(time.Hour))
		case "write":
			gitCredentialResponse(w, fmt.Sprintf("write-%d", writable.Add(1)), "write", time.Now().Add(time.Hour))
		default:
			t.Errorf("Git credentials requested %s", body)
		}
	})
	for range 2 {
		read, err := client.InstallationGitCredentials(t.Context(), false, nil)
		if err != nil || read.Token != "read-1" {
			t.Fatalf("read-only credentials were not issued: %v", err)
		}
		write, err := client.InstallationGitCredentials(t.Context(), true, nil)
		if err != nil || write.Token != "write-1" {
			t.Fatalf("write credentials were not issued: %v", err)
		}
	}
	if readOnly.Load() != 1 || writable.Load() != 1 {
		t.Fatal("read-only and write credentials must be cached separately")
	}
}

func TestInstallationGitCredentialsRepositoriesAreScopedAndCachedSeparately(t *testing.T) {
	var minted atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var input struct {
			Permissions   map[string]string `json:"permissions"`
			RepositoryIDs []int64           `json:"repository_ids"`
		}
		if json.Unmarshal(body, &input) != nil {
			t.Error("invalid token request body")
			return
		}
		token := strconv.Itoa(int(minted.Add(1)))
		for _, id := range input.RepositoryIDs {
			token += "-" + strconv.FormatInt(id, 10)
		}
		gitCredentialResponse(w, token, input.Permissions["contents"], time.Now().Add(time.Hour))
	})
	for range 2 {
		all, err := client.InstallationGitCredentials(t.Context(), false, nil)
		if err != nil || all.Token != "1" {
			t.Fatalf("unscoped credentials = %q, %v", all.Token, err)
		}
		scoped, err := client.InstallationGitCredentials(t.Context(), false, []int64{11, 12})
		if err != nil || scoped.Token != "2-11-12" {
			t.Fatalf("repository credentials = %q, %v", scoped.Token, err)
		}
	}
	if minted.Load() != 2 {
		t.Fatalf("minted %d credentials, want one per repository scope", minted.Load())
	}
}

func TestInstallationRepositoriesPaginatesUpToLimit(t *testing.T) {
	for _, test := range []struct {
		lastPage, want int
	}{{2, 2}, {1000, maxRepositoryPages}} {
		t.Run(strconv.Itoa(test.lastPage), func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/app/installations/456/access_tokens" {
					gitCredentialResponse(w, "git-token", "read", time.Now().Add(time.Hour))
					return
				}
				if r.URL.Path != "/installation/repositories" || r.Header.Get("Authorization") != "Bearer git-token" ||
					r.URL.Query().Get("per_page") != "100" {
					t.Errorf("unexpected repository listing %s", r.URL)
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if page < test.lastPage {
					w.Header().Set("Link", fmt.Sprintf(
						`<http://%s/installation/repositories?per_page=100&page=%d>; rel="next"`, r.Host, page+1))
				}
				fmt.Fprintf(w, `{"total_count":%d,"repositories":[{"id":%d,"name":"repo-%d"}]}`,
					test.lastPage, page, page)
			})
			repositories, err := client.InstallationRepositories(t.Context())
			if err != nil || len(repositories) != test.want || repositories[0] != (Repository{ID: 1, Name: "repo-1"}) ||
				repositories[test.want-1].ID != int64(test.want) {
				t.Fatalf("repositories = %v, %v", repositories, err)
			}
		})
	}
}

func TestInstallationRepositoriesRejectsInvalidPages(t *testing.T) {
	for _, page := range []string{
		`{}`, `{"repositories":[{"id":0,"name":"app"}]}`, `{"repositories":[{"id":1,"name":".."}]}`,
	} {
		t.Run(page, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/app/installations/456/access_tokens" {
					gitCredentialResponse(w, "git-token", "read", time.Now().Add(time.Hour))
					return
				}
				fmt.Fprint(w, page)
			})
			var apiErr *APIError
			if _, err := client.InstallationRepositories(t.Context()); !errors.As(err, &apiErr) ||
				apiErr.Code != InvalidResponse {
				t.Fatalf("invalid repository page accepted: %v", err)
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
			result, err := client.InstallationGitCredentials(t.Context(), true, nil)
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
			result, err = client.InstallationGitCredentials(t.Context(), true, nil)
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
			result, err := client.InstallationGitCredentials(t.Context(), true, nil)
			if !errors.Is(err, ErrContentsPermissionRequired) || result != (InstallationCredentials{}) ||
				len(client.gitCredentials) != 0 {
				t.Fatalf("cleanup changed the permission error or retained the rejected token: %v", err)
			}
			if minted.Load() != 1 || revoked.Load() != 1 {
				t.Fatal("rejected token must receive exactly one best-effort revocation attempt")
			}
			for range 2 {
				result, err = client.InstallationGitCredentials(t.Context(), true, nil)
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
				result, err := client.InstallationGitCredentials(t.Context(), true, nil)
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
			result, err := client.InstallationGitCredentials(t.Context(), true, nil)
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
	result, err := client.InstallationGitCredentials(ctx, true, nil)
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
				_, _ = io.Copy(io.Discard, r.Body)
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
				_, err := client.InstallationGitCredentials(refreshCtx, true, nil)
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
				result, err := client.InstallationGitCredentials(waitCtx, true, nil)
				if !errors.Is(err, context.DeadlineExceeded) || result != (InstallationCredentials{}) {
					t.Fatalf("waiting caller was not canceled: %v", err)
				}
				close(release)
				if err := <-refreshed; err != nil {
					t.Fatalf("waiter cancellation interrupted refresh: %v", err)
				}
			}
			result, err := client.InstallationGitCredentials(ctx, true, nil)
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
