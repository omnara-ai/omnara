package secretstore

import "testing"

func TestPublishableURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://mcp.example/mcp":             true,
		"http://localhost:8080/mcp":           true,
		"https://[::1]:8443/mcp":              true,
		"https://user:secret@mcp.example/mcp": false,
		"user:secret@mcp.example/mcp":         false,
		"//mcp.example/mcp":                   false,
		"https://:443/mcp":                    false,
		"ftp://mcp.example/mcp":               false,
		"":                                    false,
	} {
		if got := publishableURL(raw); got != want {
			t.Errorf("publishableURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
