package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func readIntegrationCallbackBody(
	w http.ResponseWriter,
	r *http.Request,
	maxBodyBytes int64,
) ([]byte, bool) {
	deadline := time.Now().Add(integrationIntakeTimeout)
	if requestDeadline, ok := r.Context().Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Write(w, openapi.ErrorCodeRequestTooLarge)
		} else {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid request body")
		}
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
	ownerID uuid.UUID, appID, workspaceID string,
) (integrationstore.ProjectIntegrationRecord, bool) {
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return integrationstore.ProjectIntegrationRecord{}, false
	}
	if appID == "" || workspaceID == "" {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack callback identity")
		return integrationstore.ProjectIntegrationRecord{}, false
	}
	install, err := s.store.Integrations().GetProjectIntegrationByID(r.Context(), ownerID)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return integrationstore.ProjectIntegrationRecord{}, false
	}
	if install.State != integrationstore.ProjectIntegrationStateActive ||
		install.Provider != integrationstore.IntegrationProviderSlack ||
		install.ProviderTenantID != workspaceID || install.ProviderAccountRef != appID {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack callback owner")
		return integrationstore.ProjectIntegrationRecord{}, false
	}
	credentials, err := s.integrationSlackCredentials(r.Context(), install)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return integrationstore.ProjectIntegrationRecord{}, false
	}
	if !slack.ValidSignature(r.Header, raw, credentials.SigningSecret, time.Now().UTC()) {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid signature")
		return integrationstore.ProjectIntegrationRecord{}, false
	}
	return install, true
}

func (s *Server) slackCallbackOwner(ctx context.Context, envelope slack.ActionsEnvelope) (uuid.UUID, error) {
	for _, action := range envelope.Actions {
		if value, ok := strings.CutPrefix(action.ActionID, slack.ProfileChoiceActionPrefix); ok {
			id, err := publicid.Decode(publicid.KindIntegrationProfileChoice, value)
			if err != nil {
				return uuid.Nil, storeerr.ErrNotFound
			}
			return s.store.Integrations().GetIntegrationProfileChoiceIntegrationID(ctx, id)
		}
	}
	interactionID, err := slack.PromptCallbackInteractionID(envelope)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	id, err := publicid.Decode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return s.store.Execution().GetInteractionCallbackIntegrationID(ctx, id)
}
