package toolcatalog

import "testing"

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
