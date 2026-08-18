package httpapi

import (
	"net/http"

	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

const openAPIYAMLPath = "/api/openapi.yaml"
const omnaradInstallPath = "/install/omnarad.sh"
const webConfigPath = "/api/web-config"

type manualRouteAccess string

const (
	manualRouteAccessAuthRequired          manualRouteAccess = "auth-required"
	manualRouteAccessProviderSigned        manualRouteAccess = "provider-signed"
	manualRouteAccessProviderUnsignedProbe manualRouteAccess = "provider-unsigned-probe"
	manualRouteAccessOAuthState            manualRouteAccess = "oauth-state"
	manualRouteAccessStatic                manualRouteAccess = "static"
)

type manualRouteContract struct {
	Method  string
	Pattern string
	Access  manualRouteAccess
}

var serverManualRouteContracts = []manualRouteContract{
	{Method: http.MethodGet, Pattern: mcpOAuthCallbackPath, Access: manualRouteAccessOAuthState},
	{Method: http.MethodGet, Pattern: integrationOAuthCallbackPath, Access: manualRouteAccessAuthRequired},
	{Method: http.MethodGet, Pattern: mcpOAuthClientMetadataPath, Access: manualRouteAccessStatic},
	{Method: http.MethodPost, Pattern: integrationEventsPath, Access: manualRouteAccessProviderUnsignedProbe},
	{Method: http.MethodPost, Pattern: integrationActionsPath, Access: manualRouteAccessProviderSigned},
	{Method: http.MethodGet, Pattern: openAPIYAMLPath, Access: manualRouteAccessStatic},
	{Method: http.MethodGet, Pattern: omnaradInstallPath, Access: manualRouteAccessStatic},
	{Method: http.MethodGet, Pattern: webConfigPath, Access: manualRouteAccessStatic},
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	if s.authRoutes != nil {
		s.authRoutes.RegisterRoutes(mux)
	}
	mux.HandleFunc("GET /api/mcp-oauth/callback", s.mcpOAuthCallbackRoute)
	mux.HandleFunc("GET /api/integrations/oauth/callback", s.integrationOAuthCallbackRoute)
	mux.HandleFunc("GET /.well-known/oauth-client.json", s.mcpOAuthClientMetadataRoute)
	mux.HandleFunc("POST /api/integrations/slack/events", s.integrationEventsRoute)
	mux.HandleFunc("POST /api/integrations/slack/actions", s.integrationActionsRoute)
	mux.HandleFunc("GET /api/openapi.yaml", s.openapiYAMLRoute)
	mux.HandleFunc("GET /install/omnarad.sh", s.omnaradInstallRoute)
	mux.HandleFunc("GET /api/web-config", s.webConfigRoute)
	openapi.HandlerWithOptions(s.strictOpenAPIHandler(), openapi.StdHTTPServerOptions{
		BaseRouter: mux,
		Middlewares: []openapi.MiddlewareFunc{
			s.rejectEncodedSlashPathValues,
		},
		ErrorHandlerFunc: openAPIErrorHandler,
	})
	s.registerRootRoutes(mux)
}

func (s *Server) openapiYAMLRoute(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapispec.YAML)
}

type webConfigResponse struct {
	BillingURL string `json:"billing_url,omitempty"`
}

func (s *Server) webConfigRoute(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, webConfigResponse{BillingURL: s.billingURL})
}

func openAPIErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	logpkg.Error(r.Context(), err)
	apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	apierror.Write(w, openapi.ErrorCodeNotFound)
}
