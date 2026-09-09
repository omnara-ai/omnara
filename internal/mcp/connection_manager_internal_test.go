package mcp

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestSanitizeInitializationError(t *testing.T) {
	if got := sanitizeInitializationError(" \xffbad\x00 \n"); got != "\uFFFDbad" {
		t.Fatalf("sanitized error = %q", got)
	}
	got := sanitizeInitializationError(strings.Repeat("é", maxInitializationErrorRunes+1))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != maxInitializationErrorRunes {
		t.Fatalf("sanitized long error has %d runes and valid UTF-8 %t", utf8.RuneCountInString(got), utf8.ValidString(got))
	}
}

func TestValidateDiscoveredTools(t *testing.T) {
	disabledTool := map[string]agentconfig.RuntimeMCPTool{
		strings.Repeat("a", 60): {RemoteName: strings.Repeat("a", 60), Enabled: boolPtr(false)},
		"search docs":           {RemoteName: "search docs", Enabled: boolPtr(false)},
	}
	tests := []struct {
		name   string
		tools  []*sdkmcp.Tool
		server agentconfig.RuntimeMCPServer
		want   string
	}{
		{
			name: "valid",
			tools: []*sdkmcp.Tool{
				{Name: "search"},
				{Name: "fetch-result"},
			},
		},
		{
			name:  "AWS tool name",
			tools: []*sdkmcp.Tool{{Name: "aws___call_aws"}},
		},
		{
			name:  "null tool",
			tools: []*sdkmcp.Tool{nil},
			want:  "tool at index 0 is null",
		},
		{
			name:  "unsupported name",
			tools: []*sdkmcp.Tool{{Name: "search docs"}},
			want:  `tool name "search docs" cannot be exposed to the model: mcp tool name "search docs" must match`,
		},
		{
			name:  "prefixed name too long",
			tools: []*sdkmcp.Tool{{Name: strings.Repeat("a", 60)}},
			want: `tool name "` + strings.Repeat("a", 60) + `" cannot be exposed to the model: tool "` +
				strings.Repeat("a", 60) + `" becomes "mcp__docs__` + strings.Repeat("a", 60) +
				`" (71 characters) once the server name is prefixed, but the model only accepts tool names of 64 characters or fewer; ` +
				`the tool name itself is too long to expose under any server name`,
		},
		{
			name: "duplicate name",
			tools: []*sdkmcp.Tool{
				{Name: "search"},
				{Name: "search"},
			},
			want: `duplicate tool name "search"`,
		},
		{
			name: "unexposable names skipped when disabled by override",
			tools: []*sdkmcp.Tool{
				{Name: "search"},
				{Name: strings.Repeat("a", 60)},
				{Name: "search docs"},
			},
			server: agentconfig.RuntimeMCPServer{ServerKey: "docs", DefaultEnabled: true, Tools: disabledTool},
		},
		{
			name: "unexposable names skipped when server disables tools by default",
			tools: []*sdkmcp.Tool{
				{Name: "search"},
				{Name: strings.Repeat("a", 60)},
			},
			server: agentconfig.RuntimeMCPServer{
				ServerKey: "docs",
				Tools:     map[string]agentconfig.RuntimeMCPTool{"search": {RemoteName: "search", Enabled: boolPtr(true)}},
			},
		},
		{
			name: "duplicate disabled names still rejected",
			tools: []*sdkmcp.Tool{
				{Name: "search docs"},
				{Name: "search docs"},
			},
			server: agentconfig.RuntimeMCPServer{ServerKey: "docs", DefaultEnabled: true, Tools: disabledTool},
			want:   `duplicate tool name "search docs"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := test.server
			if server.ServerKey == "" {
				server = agentconfig.RuntimeMCPServer{ServerKey: "docs", DefaultEnabled: true}
			}
			err := validateDiscoveredTools(server, test.tools)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validate discovered tools: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func TestEnsureConnectionLeavesConnectionsAloneWhenNothingIsDue(t *testing.T) {
	catalogID := uuid.New()
	cases := map[string]struct {
		trigger ConnectionTrigger
		conn    executionstore.MCPConnectionRecord
	}{
		"resume does not revive failed": {
			trigger: TriggerTurnResume,
			conn:    executionstore.MCPConnectionRecord{State: executionstore.MCPConnectionStateFailed},
		},
		"resume does not refresh ready": {
			trigger: TriggerTurnResume,
			conn: executionstore.MCPConnectionRecord{
				State:     executionstore.MCPConnectionStateReady,
				CatalogID: &catalogID,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := Manager{}.EnsureConnection(
				context.Background(),
				uuid.New(),
				uuid.New(),
				uuid.New(),
				tc.conn,
				agentconfig.RuntimeMCPServer{ServerKey: "docs"},
				tc.trigger,
			)
			if err != nil || result.Changed {
				t.Fatalf("result = %+v err = %v, want unchanged", result, err)
			}
			if result.Ready != (tc.conn.State == executionstore.MCPConnectionStateReady) {
				t.Fatalf("ready = %t for state %q", result.Ready, tc.conn.State)
			}
		})
	}
}
