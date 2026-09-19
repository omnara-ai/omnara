package httpapi

import (
	"io"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func readIntegrationCallbackBody(
	w http.ResponseWriter,
	r *http.Request,
	maxBodyBytes int64,
) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid request body")
		return nil, false
	}
	if int64(len(raw)) > maxBodyBytes {
		apierror.Write(w, openapi.ErrorCodeRequestTooLarge)
		return nil, false
	}
	return raw, true
}

func (s *Server) verifySignedSlackCallback(
	w http.ResponseWriter,
	r *http.Request,
	raw []byte,
	appID, workspaceID string,
) (integrationstore.IntegrationConnectionRecord, bool) {
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return integrationstore.IntegrationConnectionRecord{}, false
	}
	if appID == "" || workspaceID == "" {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack callback identity")
		return integrationstore.IntegrationConnectionRecord{}, false
	}
	install, err := s.store.Integrations().GetIntegrationConnectionByProviderAccount(
		r.Context(),
		integrationstore.IntegrationProviderSlack,
		workspaceID,
		appID,
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid signature")
			return integrationstore.IntegrationConnectionRecord{}, false
		}
		writeIntegrationProviderError(w, err)
		return integrationstore.IntegrationConnectionRecord{}, false
	}
	credentials, err := s.integrationSlackCredentials(r.Context(), install)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return integrationstore.IntegrationConnectionRecord{}, false
	}
	if !slack.ValidSignature(r.Header, raw, credentials.SigningSecret, time.Now().UTC()) {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid signature")
		return integrationstore.IntegrationConnectionRecord{}, false
	}
	return install, true
}
