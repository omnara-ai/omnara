package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func identityResponse(path string) string {
	switch path {
	case "/app":
		return `{"id":123,"slug":"helper","name":"Helper Bot","owner":{"id":888}}`
	case "/app/installations/456":
		return `{"id":456,"app_id":123}`
	case "/users/helper[bot]":
		return `{"id":999,"login":"helper[bot]","type":"Bot"}`
	default:
		return ""
	}
}

func TestCheckAppIdentity(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Github-Api-Version") != APIVersion || r.Header.Get("Accept") != jsonMediaType {
			t.Error("missing provider headers")
		}
		if r.URL.Path == "/installation/token" {
			if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer installation-token" ||
				requests.Load() != 5 {
				t.Error("cleanup must revoke the temporary token once, after bot lookup")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/users/helper[bot]" {
			if r.Header.Get("Authorization") != "Bearer installation-token" ||
				r.URL.EscapedPath() != "/users/helper%5Bbot%5D" {
				t.Error("bot lookup must authenticate with the temporary token and escape the login")
			}
		} else if len(strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")) != 3 {
			t.Error("App and installation endpoints require App JWT authentication")
		}
		if r.URL.Path == "/app/installations/456/access_tokens" {
			var input map[string]any
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("invalid token request")
			}
			encoded, _ := json.Marshal(input)
			if string(encoded) != `{"permissions":{"metadata":"read"}}` {
				t.Errorf("setup token must request only metadata read: %s", encoded)
			}
			tokenResponse(w)
			return
		}
		if r.Method != http.MethodGet || identityResponse(r.URL.Path) == "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, identityResponse(r.URL.Path))
	})
	var hooks int
	client.beforeRequest = func(ctx context.Context) error {
		hooks++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > OperationTimeout {
			t.Error("identity operation must be bounded")
		}
		return nil
	}
	identity, err := client.CheckAppIdentity(t.Context())
	if err != nil || identity != (AppIdentity{
		AppID: 123, InstallationID: 456, AppSlug: "helper", BotUserID: 999, BotLogin: "helper[bot]",
		DisplayName: "Helper Bot",
	}) {
		t.Fatalf("identity=%+v err=%v", identity, err)
	}
	if requests.Load() != 5 || hooks != 5 || client.tokens != ([2]cachedToken{}) {
		t.Fatal("all five attempts must be fenced; setup token must not enter the PR tool cache")
	}
}

func TestCheckAppIdentityDisplayNameFallback(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app":
			fmt.Fprint(w, `{"id":123,"slug":"helper"}`)
		case "/app/installations/456/access_tokens":
			tokenResponse(w)
		case "/installation/token":
			w.WriteHeader(http.StatusNoContent)
		default:
			fmt.Fprint(w, identityResponse(r.URL.Path))
		}
	})
	identity, err := client.CheckAppIdentity(t.Context())
	if err != nil || identity.DisplayName != "helper[bot]" {
		t.Fatalf("missing App name must preserve discovery: %+v %v", identity, err)
	}
}

func TestCheckAppIdentityRejectsUnverifiedFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, body string
		code             ErrorCode
	}{
		{"wrong App", "/app", `{"id":321,"slug":"helper"}`, ScopeMismatch},
		{"missing slug", "/app", `{"id":123}`, ScopeMismatch},
		{"path in slug", "/app", `{"id":123,"slug":"../other"}`, ScopeMismatch},
		{"wrong installation", "/app/installations/456", `{"id":654,"app_id":123}`, ScopeMismatch},
		{"wrong installation App", "/app/installations/456", `{"id":456,"app_id":321}`, ScopeMismatch},
		{"missing bot ID", "/users/helper[bot]", `{"login":"helper[bot]","type":"Bot"}`, ScopeMismatch},
		{"human account", "/users/helper[bot]", `{"id":999,"login":"helper[bot]","type":"User"}`, ScopeMismatch},
		{"different bot", "/users/helper[bot]", `{"id":999,"login":"other[bot]","type":"Bot"}`, ScopeMismatch},
		{"expired token", "/app/installations/456/access_tokens",
			`{"token":"private-token","expires_at":"2000-01-01T00:00:00Z"}`, InvalidResponse},
		{"malformed expiry", "/app/installations/456/access_tokens",
			`{"token":"private-token","expires_at":"invalid"}`, InvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reached atomic.Bool
			var revoked atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/installation/token" {
					revoked.Add(1)
					if r.Method != http.MethodDelete {
						t.Error("cleanup must use DELETE")
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if reached.Load() {
					t.Error("continued after an unverified identity")
				}
				if r.URL.Path == tc.path {
					reached.Store(true)
					fmt.Fprint(w, tc.body)
				} else if r.URL.Path == "/app/installations/456/access_tokens" {
					tokenResponse(w)
				} else {
					fmt.Fprint(w, identityResponse(r.URL.Path))
				}
			})
			_, err := client.CheckAppIdentity(t.Context())
			var apiErr *APIError
			if !reached.Load() || !errors.As(err, &apiErr) || apiErr.Code != tc.code {
				t.Fatalf("unexpected identity failure: %v", err)
			}
			var want int32
			if strings.HasPrefix(tc.path, "/users/") || strings.HasSuffix(tc.path, "/access_tokens") {
				want = 1
			}
			if revoked.Load() != want {
				t.Fatalf("cleanup attempts=%d want=%d", revoked.Load(), want)
			}
		})
	}
}

func TestCheckAppIdentityTokenFailureAndPreSendFence(t *testing.T) {
	t.Parallel()
	for _, fence := range []bool{false, true} {
		var tokenRequests atomic.Int32
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/app/installations/456/access_tokens" {
				tokenRequests.Add(1)
				http.Error(w, "private provider body", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, identityResponse(r.URL.Path))
		})
		blocked := errors.New("credential revision changed")
		var attempts int
		client.beforeRequest = func(context.Context) error {
			attempts++
			if fence && attempts == 3 {
				return blocked
			}
			return nil
		}
		_, err := client.CheckAppIdentity(t.Context())
		if fence {
			if !errors.Is(err, blocked) || tokenRequests.Load() != 0 {
				t.Fatalf("trusted pre-send failure must prevent token request: %v", err)
			}
		} else {
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != TransientFailure || tokenRequests.Load() != 1 ||
				strings.Contains(err.Error(), "private") {
				t.Fatalf("token POST must not blindly retry or leak provider body: %v", err)
			}
		}
	}
}

func TestCheckAppIdentityCleanupFailurePreservesResult(t *testing.T) {
	t.Parallel()
	for _, lookupFails := range []bool{false, true} {
		for _, cleanupStatus := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("lookup_failure_%t/cleanup_%d", lookupFails, cleanupStatus), func(t *testing.T) {
				t.Parallel()
				var revoked atomic.Int32
				client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/app/installations/456/access_tokens":
						tokenResponse(w)
					case "/installation/token":
						revoked.Add(1)
						if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer installation-token" {
							t.Error("cleanup must authenticate with its temporary installation token")
						}
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(cleanupStatus)
						fmt.Fprint(w, strings.Repeat("private-cleanup-body installation-token ", ErrorMaxBytes))
					default:
						if lookupFails && r.URL.Path == "/users/helper[bot]" {
							http.Error(w, "private-lookup-body installation-token", http.StatusNotFound)
							return
						}
						fmt.Fprint(w, identityResponse(r.URL.Path))
					}
				})
				identity, err := client.CheckAppIdentity(t.Context())
				if revoked.Load() != 1 {
					t.Fatalf("cleanup must make one bounded attempt, got %d", revoked.Load())
				}
				if lookupFails {
					var apiErr *APIError
					if !errors.As(err, &apiErr) || apiErr.Code != PermanentFailure || apiErr.StatusCode != http.StatusNotFound {
						t.Fatalf("cleanup replaced the original lookup failure: %v", err)
					}
				} else if err != nil || identity.BotUserID != 999 {
					t.Fatalf("cleanup replaced successful discovery: %+v %v", identity, err)
				}
				result, _ := json.Marshal(identity)
				returned := string(result) + fmt.Sprint(err)
				if strings.Contains(returned, "installation-token") || strings.Contains(returned, "private-") {
					t.Error("identity results must not expose credentials or raw failure bodies")
				}
			})
		}
	}
}

func TestCheckAppIdentityCleanupHonorsOperationContext(t *testing.T) {
	t.Parallel()
	for _, cancelAt := range []int{4, 5} {
		t.Run(fmt.Sprintf("cancel_before_attempt_%d", cancelAt), func(t *testing.T) {
			t.Parallel()
			var revoked atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/installation/token" {
					revoked.Add(1)
					w.WriteHeader(http.StatusNoContent)
				} else if r.URL.Path == "/app/installations/456/access_tokens" {
					tokenResponse(w)
				} else {
					fmt.Fprint(w, identityResponse(r.URL.Path))
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls int
			var deadline time.Time
			client.beforeRequest = func(attempt context.Context) error {
				calls++
				current, ok := attempt.Deadline()
				if calls == 1 {
					deadline = current
				}
				if !ok || !current.Equal(deadline) {
					t.Error("cleanup must share the original bounded operation context")
				}
				if calls == cancelAt {
					cancel()
				}
				return attempt.Err()
			}
			identity, err := client.CheckAppIdentity(ctx)
			if calls != cancelAt || revoked.Load() != 0 {
				t.Fatal("cleanup bypassed cancellation or the pre-send hook")
			}
			if cancelAt == 4 {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lookup cancellation was lost: %v", err)
				}
			} else if err != nil || identity.BotUserID != 999 {
				t.Fatalf("cleanup cancellation replaced verified facts: %+v %v", identity, err)
			}
		})
	}
}
