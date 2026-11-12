package apimcp

import (
	_ "embed"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server identity advertised in the MCP handshake (Implementation and
// instructions) and in the Server Card. Keep in step with server.json at the
// repository root, which is what the MCP Registry publishes.
const (
	ServerTitle       = "Omnara"
	ServerDescription = "Run durable AI agents with Omnara, the open-source managed agent platform."
	ServerWebsiteURL  = "https://www.omnara.com"
	ServerIconURL     = "https://www.omnara.com/apple-touch-icon.png"
	ServerIconMIME    = "image/png"
	ServerIconSize    = "180x180"
)

//go:embed instructions.md
var serverInstructions string

// ServerInstructions tells connected agents how the tools fit together.
var ServerInstructions = strings.TrimSpace(serverInstructions)

func serverImplementation(version string) *mcp.Implementation {
	return &mcp.Implementation{
		Name:        serverName,
		Title:       ServerTitle,
		Description: ServerDescription,
		Version:     version,
		WebsiteURL:  ServerWebsiteURL,
		Icons: []mcp.Icon{{
			Source:   ServerIconURL,
			MIMEType: ServerIconMIME,
			Sizes:    []string{ServerIconSize},
		}},
	}
}
