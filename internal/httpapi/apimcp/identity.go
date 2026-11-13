package apimcp

import (
	_ "embed"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	registryName      = "com.omnara/omnara"
	serverTitle       = "Omnara"
	serverDescription = "Run durable AI agents with Omnara, the open-source managed agent platform."
	serverWebsiteURL  = "https://www.omnara.com"
	serverRepoURL     = "https://github.com/omnara-ai/omnara"

	serverCardSchema = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"
)

//go:embed instructions.md
var instructionsFile string

var serverInstructions = strings.TrimSpace(instructionsFile)

func serverIcons() []mcp.Icon {
	return []mcp.Icon{{
		Source:   "https://www.omnara.com/apple-touch-icon.png",
		MIMEType: "image/png",
		Sizes:    []string{"180x180"},
	}}
}

func serverImplementation(version string) *mcp.Implementation {
	return &mcp.Implementation{
		Name:        serverName,
		Title:       serverTitle,
		Description: serverDescription,
		Version:     version,
		WebsiteURL:  serverWebsiteURL,
		Icons:       serverIcons(),
	}
}

type ServerCard struct {
	Schema      string                `json:"$schema"`
	Name        string                `json:"name"`
	Version     string                `json:"version"`
	Description string                `json:"description"`
	Title       string                `json:"title,omitempty"`
	WebsiteURL  string                `json:"websiteUrl,omitempty"`
	Repository  *ServerCardRepository `json:"repository,omitempty"`
	Icons       []mcp.Icon            `json:"icons,omitempty"`
	Remotes     []ServerCardRemote    `json:"remotes,omitempty"`
}

type ServerCardRepository struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type ServerCardRemote struct {
	Type                      string   `json:"type"`
	URL                       string   `json:"url"`
	SupportedProtocolVersions []string `json:"supportedProtocolVersions,omitempty"`
}

func NewServerCard(version, endpoint string) ServerCard {
	return ServerCard{
		Schema:      serverCardSchema,
		Name:        registryName,
		Version:     version,
		Description: serverDescription,
		Title:       serverTitle,
		WebsiteURL:  serverWebsiteURL,
		Repository:  &ServerCardRepository{URL: serverRepoURL, Source: "github"},
		Icons:       serverIcons(),
		Remotes: []ServerCardRemote{{
			Type:                      "streamable-http",
			URL:                       endpoint,
			SupportedProtocolVersions: mcp.SupportedProtocolVersions(),
		}},
	}
}
