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

func (s *Server) mcpResourceOrigin(r *http.Request) string {
	for _, origin := range s.publicOrigins {
		if origin.matchesHost(r.Host) {
			return origin.url
		}
	}
	return s.issuerURL(r)
}

func (s *Server) mcpResourceURL(r *http.Request) string {
	origin := s.mcpResourceOrigin(r)
	if origin == "" {
		return ""
	}
	return origin + apimcp.Path
}

func (s *Server) mcpResourceURLs() []string {
	resources := make([]string, 0, len(s.publicOrigins))
	for _, origin := range s.publicOrigins {
		resources = append(resources, origin.url+apimcp.Path)
	}
	return resources
}

func (s *Server) mcpProtectedResourceMetadataURL(r *http.Request) string {
	return s.mcpResourceOrigin(r) + mcpProtectedResourceMetadataPath
}

func (s *Server) mcpProtectedResourceMetadataRoute(w http.ResponseWriter, r *http.Request) {
	issuer := s.issuerURL(r)
	resource := s.mcpResourceURL(r)
	if issuer == "" || resource == "" {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 resource,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "Omnara",
	})
}
