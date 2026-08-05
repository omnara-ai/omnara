package mcp

import (
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestValidateDiscoveredTools(t *testing.T) {
	tests := []struct {
		name  string
		tools []*sdkmcp.Tool
		want  string
	}{
		{
			name: "valid",
			tools: []*sdkmcp.Tool{
				{Name: "search"},
				{Name: "fetch-result"},
			},
		},
		{
			name:  "null tool",
			tools: []*sdkmcp.Tool{nil},
			want:  "tool at index 0 is null",
		},
		{
			name:  "unsupported name",
			tools: []*sdkmcp.Tool{{Name: "search docs"}},
			want:  `tool name "search docs" cannot be exposed to the model`,
		},
		{
			name: "duplicate name",
			tools: []*sdkmcp.Tool{
				{Name: "search"},
				{Name: "search"},
			},
			want: `duplicate tool name "search"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDiscoveredTools("docs", test.tools)
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
