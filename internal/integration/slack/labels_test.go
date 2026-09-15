package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupUserDisplayNamePreservesOAuthNameSelection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		user slackUserInfo
		want string
	}{
		{"profile display", slackUserInfo{Name: "ada", RealName: "Ada", Profile: &UserProfile{
			DisplayName: " Ada Lovelace ", RealName: "Other name",
		}}, "Ada Lovelace"},
		{"profile fallback", slackUserInfo{Name: "ada", Profile: &UserProfile{RealName: "Ada"}}, "Ada"},
		{"user fallback", slackUserInfo{Name: "ada", RealName: " Ada "}, "Ada"},
		{"username fallback", slackUserInfo{Name: " ada "}, "ada"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/users.info" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("user lookup did not use the expected endpoint and credential")
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if err := r.ParseForm(); err != nil || r.Form.Get("user") != "U123" {
					t.Error("user lookup did not preserve the scoped user ID")
					http.Error(w, "unexpected user", http.StatusBadRequest)
					return
				}
				writeSlackTestJSON(w, userInfoResponse{OK: true, User: test.user})
			}))
			defer server.Close()
			name, result, err := LookupUserDisplayName(context.Background(),
				OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, "test-token", " U123 ")
			require.NoError(t, err)
			require.Equal(t, APIResult{}, result)
			require.Equal(t, test.want, name)
		})
	}
}
