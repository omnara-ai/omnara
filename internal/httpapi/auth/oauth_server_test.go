package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type oauthTestLimiter struct {
	err error
}

func (l oauthTestLimiter) Allow(context.Context, string, int, time.Duration) error {
	return l.err
}

func TestOAuthRateLimitErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tokenEndpoint bool
		limiter       RateLimiter
		wantStatus    int
		wantCode      string
	}{
		{
			name:          "device authorization rate limit",
			tokenEndpoint: false,
			limiter:       oauthTestLimiter{err: errRateLimited},
			wantStatus:    http.StatusTooManyRequests,
			wantCode:      "temporarily_unavailable",
		},
		{
			name:          "token polling rate limit",
			tokenEndpoint: true,
			limiter:       oauthTestLimiter{err: errRateLimited},
			wantStatus:    http.StatusBadRequest,
			wantCode:      "slow_down",
		},
		{
			name:          "limiter unavailable",
			tokenEndpoint: true,
			limiter:       oauthTestLimiter{err: errors.New("redis unavailable")},
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      "temporarily_unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &Handler{limiter: tc.limiter}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/auth/device/token", nil)

			if handler.requireOAuthRateLimits(
				recorder,
				request,
				"device_token",
				"device-code",
				1,
				1,
				1,
				time.Minute,
				tc.tokenEndpoint,
			) {
				t.Fatal("rate-limited request was allowed")
			}
			var response oauthErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode OAuth error: %v", err)
			}
			if recorder.Code != tc.wantStatus || response.Error != tc.wantCode {
				t.Fatalf("OAuth rate limit response status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}
