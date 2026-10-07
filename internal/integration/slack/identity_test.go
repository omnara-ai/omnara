package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, response string
		status         int
		allowed        bool
	}{
		{"bot", `{"ok":true,"team_id":"T123","user_id":"U123","bot_id":"B123"}`, 200, true},
		{"workspace", `{"ok":true,"team_id":"T456","user_id":"U123","bot_id":"B123"}`, 200, false},
		{"bot user", `{"ok":true,"team_id":"T123","user_id":"U456","bot_id":"B456"}`, 200, false},
		{"user token", `{"ok":true,"team_id":"T123","user_id":"U123"}`, 200, false},
		{"revoked", `{"ok":false,"error":"token_revoked"}`, 200, false},
		{"incomplete", `{"ok":true}`, 200, false},
		{"rate limited", `{}`, 429, false},
		{"unavailable", `{}`, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/auth.test", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "Bearer rotated-token", r.Header.Get("Authorization"))
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			err := CheckIdentity(t.Context(), OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				"rotated-token", Identity{WorkspaceID: "T123", BotUserID: "U123"})
			if tc.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if tc.status == 429 || tc.status == 503 {
					var apiErr *APIError
					require.ErrorAs(t, err, &apiErr)
					require.Equal(t, 2*time.Minute, apiErr.RetryDelay())
				}
			}
		})
	}
}

func TestCheckIdentityHonorsRequestAuthority(t *testing.T) {
	t.Parallel()
	denied := errors.New("authority revoked")
	client := WithRequestCheck(nil, func(context.Context) error { return denied })
	err := CheckIdentity(t.Context(), OAuthConfig{HTTPClient: client}, "token",
		Identity{WorkspaceID: "T123", BotUserID: "U123"})
	require.ErrorIs(t, err, denied)
}
