package executionstore

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/omnara-ai/omnara/internal/agentconfig"
)

// mcpServerConfigHash is the MCP connection identity: it hashes only the MCP
// projection of the agent config (server key, endpoint, tool surface, and
// permissions). It deliberately excludes the whole-config definition hash so
// config changes that leave a server's MCP projection unchanged (model switch,
// instruction edits) do not change connection identity or force a
// reinitialize/generation bump.
func mcpServerConfigHash(server agentconfig.RuntimeMCPServer) (string, error) {
	tools := make(map[string]map[string]any, len(server.Tools))
	for remoteName, tool := range server.Tools {
		tools[remoteName] = map[string]any{
			"enabled":    tool.Enabled,
			"permission": tool.Permission,
		}
	}
	body := map[string]any{
		"server_key":      server.ServerKey,
		"url":             server.URL,
		"auth":            server.Auth,
		"default_enabled": server.DefaultEnabled,
		"permission":      server.Permission,
		"tools":           tools,
	}
	raw, err := marshalJSON(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(normalizedJSON(raw))
	return hex.EncodeToString(sum[:]), nil
}
