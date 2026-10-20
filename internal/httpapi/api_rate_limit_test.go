package httpapi

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

type countingRateLimiter struct {
	counts map[string]int
	window time.Duration
	err    error
}

func (l *countingRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) error {
	l.window = window
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.err != nil {
		return l.err
	}
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[key]++
	if l.counts[key] > limit {
		return httpauth.ErrRateLimited
	}
	return nil
}

func serveAs(
	handler http.Handler,
	principal identitystore.PrincipalRecord,
	method, path string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestLimitAPIRequestsSharesBudgetsPerAccountAndBucket(t *testing.T) {
	limiter := &countingRateLimiter{}
	server := &Server{apiRateLimit: &apiRateLimit{limiter: limiter, readsPerHour: 20, writesPerHour: 10, burst: 2}}
	handler := server.limitAPIRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	userID, orgID := uuid.New(), uuid.New()
	pat := identitystore.NewPersonalAccessTokenPrincipal(userID, uuid.New())
	oauth := identitystore.NewOAuthAccessTokenPrincipal(userID, uuid.New())
	firstOrgKey := identitystore.NewOrgAPIKeyPrincipal(orgID, uuid.New())
	secondOrgKey := identitystore.NewOrgAPIKeyPrincipal(orgID, uuid.New())
	path := openAPIBasePath + "/me"

	for _, principal := range []identitystore.PrincipalRecord{pat, oauth, firstOrgKey, secondOrgKey} {
		if code := serveAs(handler, principal, http.MethodGet, path).Code; code != http.StatusNotFound {
			t.Fatalf("read within budget status = %d, want 404", code)
		}
	}
	for _, principal := range []identitystore.PrincipalRecord{pat, firstOrgKey} {
		limited := serveAs(handler, principal, http.MethodGet, path)
		if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "180" ||
			!strings.Contains(limited.Body.String(), "retry after 180s") {
			t.Fatalf(
				"over-budget read = %d with Retry-After %q and body %s, want 429 with 180",
				limited.Code,
				limited.Header().Get("Retry-After"),
				limited.Body.String(),
			)
		}
	}
	if code := serveAs(handler, oauth, http.MethodPost, path).Code; code != http.StatusNotFound {
		t.Fatalf("write after reads ran out status = %d, want 404", code)
	}
	browser := identitystore.NewBrowserSessionPrincipal(userID, uuid.New())
	uncharged := []struct {
		principal identitystore.PrincipalRecord
		path      string
	}{{pat, apimcp.Path}, {browser, "/api/auth/security/revoke-all"}}
	for _, request := range uncharged {
		if code := serveAs(handler, request.principal, http.MethodPost, request.path).Code; code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want it passed through uncharged", request.path, code)
		}
	}
	for _, browserPath := range []string{path, integrationOAuthCallbackPath} {
		if code := serveAs(handler, browser, http.MethodGet, browserPath).Code; code != http.StatusTooManyRequests {
			t.Fatalf("browser read of %s after the user's reads ran out status = %d, want 429", browserPath, code)
		}
	}
	machineID := uuid.New()
	daemon := identitystore.NewMachineDaemonPrincipal(orgID, machineID, uuid.New())
	if code := serveAs(handler, daemon, http.MethodGet, path).Code; code != http.StatusNotFound {
		t.Fatalf("daemon read after the org's reads ran out status = %d, want 404", code)
	}

	want := map[string]int{
		"api:20:2:user:" + userID.String() + ":read":       5,
		"api:10:2:user:" + userID.String() + ":write":      1,
		"api:20:2:org:" + orgID.String() + ":read":         3,
		"api:20:2:machine:" + machineID.String() + ":read": 1,
	}
	if !maps.Equal(limiter.counts, want) || limiter.window != 6*time.Minute {
		t.Fatalf("charged buckets = %v with last window %s, want %v with 6m", limiter.counts, limiter.window, want)
	}
}

func TestLimitAPIRequestsLogsOnlyLimitsAndErrors(t *testing.T) {
	cases := []struct {
		name       string
		limit      *apiRateLimit
		wantStatus int
		wantResult any
		wantError  any
	}{
		{name: "off", wantStatus: http.StatusNoContent},
		{
			name:       "unlimited bucket",
			limit:      &apiRateLimit{limiter: &countingRateLimiter{err: httpauth.ErrRateLimited}, writesPerHour: 1, burst: 1},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "allowed",
			limit:      &apiRateLimit{limiter: &countingRateLimiter{}, readsPerHour: 1, burst: 1},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "limited",
			limit:      &apiRateLimit{limiter: &countingRateLimiter{err: httpauth.ErrRateLimited}, readsPerHour: 1, burst: 1},
			wantStatus: http.StatusTooManyRequests,
			wantResult: "limited",
		},
		{
			name: "limiter error",
			limit: &apiRateLimit{
				limiter:      &countingRateLimiter{err: errors.New("redis unavailable")},
				readsPerHour: 1,
				burst:        1,
			},
			wantStatus: http.StatusNoContent,
			wantResult: "error",
			wantError:  "redis unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf, log := newRequestEventCapture()
			server := &Server{apiRateLimit: tc.limit}
			handler := requestEvents(log, nil, nil)(server.limitAPIRequests(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
			)))
			principal := identitystore.NewPersonalAccessTokenPrincipal(uuid.New(), uuid.New())

			if code := serveAs(handler, principal, http.MethodGet, openAPIBasePath+"/me").Code; code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", code, tc.wantStatus)
			}
			event := decodeRequestEvent(t, buf)
			var wantAccount any
			if tc.wantResult != nil {
				wantAccount = "user:" + principal.ID.String()
			}
			got := []any{event["rate_limit.result"], event["rate_limit.account"], event["rate_limit.error"]}
			if want := []any{tc.wantResult, wantAccount, tc.wantError}; !slices.Equal(got, want) {
				t.Fatalf("rate limit result, account, error = %v, want %v", got, want)
			}
		})
	}
}

func TestLimitAPIRequestsIgnoresClientDisconnect(t *testing.T) {
	buf, log := newRequestEventCapture()
	server := &Server{apiRateLimit: &apiRateLimit{limiter: &countingRateLimiter{}, readsPerHour: 1, burst: 1}}
	handler := requestEvents(log, nil, nil)(server.limitAPIRequests(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	)))
	principal := identitystore.NewPersonalAccessTokenPrincipal(uuid.New(), uuid.New())
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), principalContextKey{}, principal))
	cancel()

	request := httptest.NewRequestWithContext(ctx, http.MethodGet, openAPIBasePath+"/me", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if result := decodeRequestEvent(t, buf)["rate_limit.result"]; result != nil {
		t.Fatalf("rate_limit.result = %v after a client disconnect, want none", result)
	}
}
