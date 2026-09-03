package mcp

import (
	"strings"
	"testing"
	"unicode/utf8"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
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
