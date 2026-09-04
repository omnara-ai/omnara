package toolcatalog

import (
	"errors"
	"strings"
	"testing"
)

func TestMCPRuntimeToolNamesUseExplicitNamespace(t *testing.T) {
	name := MCPRuntimeToolName("docs", "search")
	if name != "mcp__docs__search" {
		t.Fatalf("runtime name = %q, want mcp__docs__search", name)
	}
	serverKey, remoteName, ok := SplitMCPRuntimeToolName(name)
	if !ok || serverKey != "docs" || remoteName != "search" {
		t.Fatalf("split runtime name = (%q, %q, %t)", serverKey, remoteName, ok)
	}
	for _, invalid := range []string{
		"docs__search",
		"mcp__docs",
	} {
		if IsMCPRuntimeToolName(invalid) {
			t.Fatalf("%q unexpectedly recognized as an MCP runtime tool name", invalid)
		}
	}
	awsName := MCPRuntimeToolName("aws", "aws___call_aws")
	serverKey, remoteName, ok = SplitMCPRuntimeToolName(awsName)
	if !ok || serverKey != "aws" || remoteName != "aws___call_aws" {
		t.Fatalf("split AWS runtime name = (%q, %q, %t)", serverKey, remoteName, ok)
	}
}

func TestValidateMCPRuntimeToolNameExplainsPrefixedLength(t *testing.T) {
	serverKey := "customer-user-read"
	remoteName := "provider__search-call-recordings-by-metadata"
	err := ValidateMCPRuntimeToolName(serverKey, remoteName)
	var tooLong *MCPRuntimeToolNameTooLongError
	if !errors.As(err, &tooLong) {
		t.Fatalf("error = %v, want MCPRuntimeToolNameTooLongError", err)
	}
	if tooLong.RuntimeName() != "mcp__customer-user-read__provider__search-call-recordings-by-metadata" {
		t.Fatalf("runtime name = %q", tooLong.RuntimeName())
	}
	if tooLong.MaxServerKeyLength() != 13 {
		t.Fatalf("max server key length = %d, want 13", tooLong.MaxServerKeyLength())
	}
	for _, want := range []string{
		"(69 characters)",
		"64 characters or fewer",
		`shorten the server name "customer-user-read" to 13 characters or fewer`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err.Error(), want)
		}
	}
	if err := ValidateMCPRuntimeToolName("cust-read", remoteName); err != nil {
		t.Fatalf("shortened server key unexpectedly rejected: %v", err)
	}
	if err := ValidateMCPRuntimeToolName("a", strings.Repeat("b", 64)); err == nil ||
		!strings.Contains(err.Error(), "too long to expose under any server name") {
		t.Fatalf("error = %v, want unexposable tool name message", err)
	}
}
