package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestRoutesUseClientRateLimits(t *testing.T) {
	for _, config := range []struct {
		name   string
		limits authn.ClientRateLimits
	}{
		{name: "defaults"},
		{name: "partial", limits: authn.ClientRateLimits{Signup: 301}},
		{name: "custom", limits: authn.ClientRateLimits{
			Signup: 301, Login: 302, PasswordReset: 303, Token: 304,
			OAuthLogin: 305, OAuthLoginConnector: 306, DeviceAuthorization: 307, DevicePoll: 308,
		}},
	} {
		t.Run(config.name, func(t *testing.T) {
			limits := config.limits.WithDefaults()
			for _, tt := range []struct {
				path   string
				body   string
				limits []int
				window time.Duration
			}{
				{"/api/auth/signup", `{"email":"person@example.com"}`,
					[]int{5, 5, limits.Signup}, time.Hour},
				{"/api/auth/email/verify/request", `{"email":"person@example.com"}`,
					[]int{5, 5, limits.Signup}, time.Hour},
				{"/api/auth/login", `{"email":"person@example.com","password":"test"}`,
					[]int{10, 25, limits.Login}, 15 * time.Minute},
				{"/api/auth/password/reset/request", `{"email":"person@example.com"}`,
					[]int{5, 5, limits.PasswordReset}, time.Hour},
				{"/api/auth/password/reset", `{"token":"test","password":"test"}`,
					[]int{10, 10, limits.Token}, 15 * time.Minute},
				{"/api/auth/email/verify", `{"token":"test","password":"test"}`,
					[]int{10, 10, limits.Token}, 15 * time.Minute},
				{"/api/auth/connectors/example/login", "",
					[]int{limits.OAuthLoginConnector, limits.OAuthLogin}, 15 * time.Minute},
				{"/api/auth/device/code", "client_id=" + OmnaraCLIClientID,
					[]int{limits.DeviceAuthorization, limits.DeviceAuthorization, limits.DeviceAuthorization}, 15 * time.Minute},
				{"/api/auth/device/token",
					"client_id=https://client.example.com/client.json&grant_type=authorization_code" +
						"&code=test&redirect_uri=https://client.example.com/callback&code_verifier=test",
					[]int{10, 10, limits.Token}, 15 * time.Minute},
				{"/api/auth/device/token",
					"client_id=https://client.example.com/client.json&grant_type=refresh_token&refresh_token=test",
					[]int{10, 10, limits.Token}, 15 * time.Minute},
				{"/api/auth/device/token",
					"client_id=" + OmnaraCLIClientID + "&device_code=test&grant_type=" + OAuthDeviceGrantType,
					[]int{240, 240, limits.DevicePoll}, 15 * time.Minute},
			} {
				t.Run(tt.path, func(t *testing.T) {
					limiter := &recordingClientLimiter{t: t, window: tt.window}
					handler := New(Config{
						ClientRateLimits: config.limits, Limiter: limiter,
						Store: rateLimitConnectorStore{}, OAuthStates: struct{ OAuthStateStore }{},
						Email: struct{ EmailSender }{}, SignupEnabled: true, ResetEnabled: true,
						PublicURL: "https://example.com",
					})
					mux := http.NewServeMux()
					handler.RegisterRoutes(mux)
					method, contentType := http.MethodPost, "application/json"
					if tt.body == "" {
						method = http.MethodGet
					} else if strings.HasPrefix(tt.body, "client_id=") {
						contentType = "application/x-www-form-urlencoded"
					}
					req := httptest.NewRequest(method, "https://example.com"+tt.path, strings.NewReader(tt.body))
					req.Header.Set("Content-Type", contentType)
					req.Header.Set("Origin", "https://example.com")
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, req)
					wantStatus := http.StatusTooManyRequests
					if strings.Contains(tt.body, "grant_type="+OAuthDeviceGrantType) {
						wantStatus = http.StatusBadRequest
					}
					if response.Code != wantStatus || !reflect.DeepEqual(limiter.limits, tt.limits) {
						t.Fatalf("status=%d body=%s limits=%v; want status=%d limits=%v",
							response.Code, response.Body, limiter.limits, wantStatus, tt.limits)
					}
				})
			}
		})
	}
}

type recordingClientLimiter struct {
	t      *testing.T
	window time.Duration
	limits []int
}

func (l *recordingClientLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) error {
	l.t.Helper()
	if window != l.window {
		l.t.Fatalf("window=%s, want %s", window, l.window)
	}
	l.limits = append(l.limits, limit)
	if strings.Contains(key, ":client:") {
		return ErrRateLimited
	}
	return nil
}

type rateLimitConnectorStore struct{ Store }

func (rateLimitConnectorStore) GetEnabledAuthConnectorBySlug(
	_ context.Context, slug string,
) (identitystore.AuthConnectorRecord, error) {
	return identitystore.AuthConnectorRecord{Slug: slug}, nil
}
