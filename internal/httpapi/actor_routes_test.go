package httpapi

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestRequestInputSource(t *testing.T) {
	userID, orgID, credentialID := uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name      string
		principal identitystore.PrincipalRecord
		mcp       bool
		want      executionstore.AgentInputSource
	}{
		{
			name:      "browser session",
			principal: identitystore.NewBrowserSessionPrincipal(userID, credentialID),
			want:      executionstore.AgentInputSourceWeb,
		},
		{
			name:      "personal access token",
			principal: identitystore.NewPersonalAccessTokenPrincipal(userID, credentialID),
			want:      executionstore.AgentInputSourceAPI,
		},
		{
			name:      "org api key",
			principal: identitystore.NewOrgAPIKeyPrincipal(orgID, credentialID),
			want:      executionstore.AgentInputSourceAPI,
		},
		{
			name:      "oauth access token through mcp",
			principal: identitystore.NewOAuthAccessTokenPrincipal(userID, credentialID),
			mcp:       true,
			want:      executionstore.AgentInputSourceMCP,
		},
		{
			name:      "personal access token through mcp",
			principal: identitystore.NewPersonalAccessTokenPrincipal(userID, credentialID),
			mcp:       true,
			want:      executionstore.AgentInputSourceMCP,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), principalContextKey{}, tt.principal)
			if tt.mcp {
				ctx = apimcp.ContextWithToolCall(ctx, apimcp.ToolCall{Tool: "agents_input", OperationID: "createAgentInput"})
			}
			if got := requestInputSource(ctx); got != tt.want {
				t.Fatalf("requestInputSource() = %q, want %q", got, tt.want)
			}
		})
	}
}
