package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
)

const (
	mcpServerCardPath      = apimcp.Path + "/server-card"
	mcpServerCardMediaType = "application/mcp-server-card+json"
)

func (s *Server) mcpServerCard(r *http.Request) (apimcp.ServerCard, bool) {
	endpoint := s.mcpResourceURL(r)
	if endpoint == "" {
		return apimcp.ServerCard{}, false
	}
	return apimcp.NewServerCard(s.apiMCPVersion, endpoint), true
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
