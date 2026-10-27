package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/omnara-ai/omnara/internal/mcp"
)

func TestAuthorizationChallengeFromToolCallFailure(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		challenge string
		body      string
		want      mcp.AuthorizationChallenge
		wantOK    bool
	}{
		{
			name:   "insufficient scope",
			status: http.StatusForbidden,
			challenge: `Bearer error="insufficient_scope", scope="calendar.read calendar.events", ` +
				`resource_metadata="https://mcp.example/.well-known/oauth-protected-resource", ` +
				`error_description="needs write"`,
			want: mcp.AuthorizationChallenge{
				Status:              http.StatusForbidden,
				Error:               "insufficient_scope",
				ErrorDescription:    "needs write",
				Scopes:              []string{"calendar.read", "calendar.events"},
				ResourceMetadataURL: "https://mcp.example/.well-known/oauth-protected-resource",
			},
			wantOK: true,
		},
		{
			name:      "unauthorized without token",
			status:    http.StatusUnauthorized,
			challenge: `Bearer resource_metadata="https://mcp.example/.well-known/oauth-protected-resource"`,
			body:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"unauthorized"}}`,
			want: mcp.AuthorizationChallenge{
				Status:              http.StatusUnauthorized,
				ResourceMetadataURL: "https://mcp.example/.well-known/oauth-protected-resource",
			},
			wantOK: true,
		},
		{
			name:      "forbidden without scope challenge",
			status:    http.StatusForbidden,
			challenge: `Bearer error="invalid_token"`,
		},
		{
			name:   "unauthorized without challenge",
			status: http.StatusUnauthorized,
		},
		{
			name:      "non bearer challenge",
			status:    http.StatusUnauthorized,
			challenge: `Basic realm="mcp"`,
		},
		{
			name:      "server error",
			status:    http.StatusInternalServerError,
			challenge: `Bearer error="insufficient_scope"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.challenge != "" {
					w.Header().Set("WWW-Authenticate", tt.challenge)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(ts.Close)

			_, err := newClient(t).CallTool(
				context.Background(),
				mcp.Conn{EndpointURL: ts.URL},
				1,
				mcp.ToolCall{Name: "create_event", Arguments: json.RawMessage(`{}`)},
			)
			if err == nil {
				t.Fatal("CallTool() error = nil, want HTTP failure")
			}
			got, ok := mcp.AuthorizationChallengeFromError(err)
			if ok != tt.wantOK {
				t.Fatalf("AuthorizationChallengeFromError() ok = %v, want %v (err %v)", ok, tt.wantOK, err)
			}
			if !ok {
				return
			}
			if got.Status != tt.want.Status ||
				got.Error != tt.want.Error ||
				got.ErrorDescription != tt.want.ErrorDescription ||
				got.ResourceMetadataURL != tt.want.ResourceMetadataURL ||
				!slices.Equal(got.Scopes, tt.want.Scopes) {
				t.Fatalf("AuthorizationChallengeFromError() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
