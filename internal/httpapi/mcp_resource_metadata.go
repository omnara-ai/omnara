package httpapi

import (
	"net/http"

	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
)

const mcpProtectedResourceMetadataPath = "/.well-known/oauth-protected-resource" + apimcp.Path

func (s *Server) issuerURL(r *http.Request) string {
	if s.publicURL != "" {
		return s.publicURL
	}
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) mcpResourceURL(r *http.Request) string {
	issuer := s.issuerURL(r)
	if issuer == "" {
		return ""
	}
	return issuer + apimcp.Path
}

func (s *Server) mcpProtectedResourceMetadataURL(r *http.Request) string {
	return s.issuerURL(r) + mcpProtectedResourceMetadataPath
}

func (s *Server) mcpProtectedResourceMetadataRoute(w http.ResponseWriter, r *http.Request) {
	issuer := s.issuerURL(r)
	if issuer == "" {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 issuer + apimcp.Path,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Omnara",
	})
}
