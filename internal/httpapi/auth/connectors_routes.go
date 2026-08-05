package auth

import (
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"net/http"
	"net/url"
)

type authConnectorResponse struct {
	Slug        string `json:"slug"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	LoginURL    string `json:"login_url"`
}

const (
	authConnectorsPath            = "/api/auth/connectors"
	authConnectorLoginPathPattern = authConnectorsPath + "/{connector}/login"
	authConnectorCallbackPattern  = authConnectorsPath + "/{connector}/callback"
)

func (h *Handler) listConnectorsRoute(w http.ResponseWriter, r *http.Request) {
	connectors, err := h.store.ListEnabledAuthConnectorSummaries(r.Context())
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	out := make([]authConnectorResponse, 0, len(connectors))
	for _, connector := range connectors {
		out = append(out, authConnectorResponse{
			Slug:        connector.Slug,
			Kind:        connector.Kind,
			DisplayName: connector.DisplayName,
			LoginURL:    connectorLoginURL(connector),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectors": out})
}

func connectorLoginURL(connector identitystore.AuthConnectorSummaryRecord) string {
	slug := url.PathEscape(connector.Slug)
	switch connector.Kind {
	case identitystore.AuthConnectorKindGitHub, identitystore.AuthConnectorKindOIDC:
		return authConnectorsPath + "/" + slug + "/login"
	default:
		return ""
	}
}
