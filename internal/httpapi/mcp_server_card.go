package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

// MCP Server Card extension (SEP-2127):
// https://github.com/modelcontextprotocol/experimental-ext-server-card
const (
	// <streamable-http-url>/server-card is the spec's reserved default location.
	mcpServerCardPath      = apimcp.Path + "/server-card"
	mcpServerCardMediaType = "application/mcp-server-card+json"
	mcpServerCardSchema    = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"

	// Name and repository match server.json at the repository root, which is
	// what the MCP Registry publishes; the rest comes from apimcp's identity.
	mcpServerCardName    = "com.omnara/omnara"
	mcpServerCardRepoURL = "https://github.com/omnara-ai/omnara"
)

type mcpServerCard struct {
	Schema      string                `json:"$schema"`
	Name        string                `json:"name"`
	Version     string                `json:"version"`
	Description string                `json:"description"`
	Title       string                `json:"title,omitempty"`
	WebsiteURL  string                `json:"websiteUrl,omitempty"`
	Repository  *mcpServerCardRepo    `json:"repository,omitempty"`
	Icons       []mcpServerCardIcon   `json:"icons,omitempty"`
	Remotes     []mcpServerCardRemote `json:"remotes,omitempty"`
}

type mcpServerCardRepo struct {
	URL    string `json:"url"`
	Source string `json:"source"`
}

type mcpServerCardIcon struct {
	Src      string   `json:"src"`
	MimeType string   `json:"mimeType,omitempty"`
	Sizes    []string `json:"sizes,omitempty"`
}

type mcpServerCardRemote struct {
	Type                      string   `json:"type"`
	URL                       string   `json:"url"`
	SupportedProtocolVersions []string `json:"supportedProtocolVersions,omitempty"`
}

func (s *Server) mcpServerCard(r *http.Request) (mcpServerCard, bool) {
	resource := s.mcpResourceURL(r)
	if resource == "" {
		return mcpServerCard{}, false
	}
	version := "0.0.0"
	if spec, err := openapi.GetSpec(); err == nil && spec.Info != nil && spec.Info.Version != "" {
		version = spec.Info.Version
	}
	return mcpServerCard{
		Schema:      mcpServerCardSchema,
		Name:        mcpServerCardName,
		Version:     version,
		Description: apimcp.ServerDescription,
		Title:       apimcp.ServerTitle,
		WebsiteURL:  apimcp.ServerWebsiteURL,
		Repository:  &mcpServerCardRepo{URL: mcpServerCardRepoURL, Source: "github"},
		Icons: []mcpServerCardIcon{{
			Src:      apimcp.ServerIconURL,
			MimeType: apimcp.ServerIconMIME,
			Sizes:    []string{apimcp.ServerIconSize},
		}},
		Remotes: []mcpServerCardRemote{{
			Type:                      "streamable-http",
			URL:                       resource,
			SupportedProtocolVersions: mcp.SupportedProtocolVersions(),
		}},
	}, true
}

func (s *Server) mcpServerCardRoute(w http.ResponseWriter, r *http.Request) {
	card, ok := s.mcpServerCard(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	body, err := json.Marshal(card)
	if err != nil {
		http.Error(w, "encode server card", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	h := w.Header()
	setMCPServerCardCORSHeaders(h)
	h.Set("Cache-Control", "public, max-age=3600")
	h.Set("ETag", etag)
	h.Add("Vary", "Host")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", mcpServerCardMediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// mcpServerCardPreflightRoute answers CORS preflights. Browsers preflight
// conditional GETs because If-None-Match is not a CORS-safelisted header.
func mcpServerCardPreflightRoute(w http.ResponseWriter, _ *http.Request) {
	setMCPServerCardCORSHeaders(w.Header())
	w.Header().Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

func setMCPServerCardCORSHeaders(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET")
	h.Set("Access-Control-Allow-Headers", "Content-Type, If-None-Match")
	h.Set("Access-Control-Expose-Headers", "ETag")
}
