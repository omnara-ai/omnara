package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRequestSenderPermission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, permission, role string
		userID                 int64
		status                 int
		want, wantError        bool
	}{
		{"writer", "write", "write", 71, 200, true, false},
		{"maintainer", "write", "maintain", 71, 200, true, false},
		{"admin", "admin", "admin", 71, 200, true, false},
		{"reader", "read", "read", 71, 200, false, false},
		{"triage", "read", "triage", 71, 200, false, false},
		{"unrecognized", "future", "future", 71, 200, false, false},
		{"renamed user reused login", "write", "write", 72, 200, false, false},
		{"no collaborator", "", "", 0, 404, false, false},
		{"unavailable permission", "", "", 0, 503, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, repoPath(testRepository())+"/collaborators/human/permission", r.URL.Path)
				w.WriteHeader(tc.status)
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"permission": tc.permission, "role_name": tc.role, "user": User{ID: tc.userID, Login: "human"},
				}))
			}))
			defer server.Close()
			client, err := NewClient(Config{
				Credentials: testCredentials(t), InstallationID: 456, HTTPClient: server.Client(), APIURL: server.URL,
			})
			require.NoError(t, err)
			allowed, err := client.CanDirectPullRequest(t.Context(), testScope(), User{ID: 71, Login: "human"})
			require.Equal(t, tc.want, allowed)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
