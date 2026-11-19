package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelbatch"
)

// WithOpenRouterBatchWebhookSecret requires OpenRouter batch callbacks to carry
// a valid signature made with the workspace's webhook signing secret.
func WithOpenRouterBatchWebhookSecret(secret string) Option {
	return func(s *Server) {
		s.openRouterBatchWebhookSecret = secret
	}
}

// openRouterBatchCallbackRoute receives OpenRouter's "batch reached a final
// status" event. It only marks the parked model call resumable; a worker then
// takes its runtime lock back and collects the result, so the event body is
// never trusted for the outcome.
func (s *Server) openRouterBatchCallbackRoute(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), integrationIntakeTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	raw, ok := readIntegrationCallbackBody(w, r)
	if !ok {
		return
	}
	if s.openRouterBatchWebhookSecret != "" {
		if err := modelbatch.VerifyOpenRouterSignature(
			s.openRouterBatchWebhookSecret,
			r.Header.Get(modelbatch.SignatureHeader),
			raw,
			time.Now().UTC(),
		); err != nil {
			apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid webhook signature")
			return
		}
	}
	if s.store == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return
	}
	requestID, err := uuid.Parse(r.PathValue("batch_request_id"))
	token := r.PathValue("token")
	if err != nil || token == "" {
		// Unknown callbacks are acknowledged so the sender does not retry them.
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	woken, err := s.store.Execution().WakeModelBatchRequest(ctx, requestID, modelbatch.HashCallbackToken(token))
	if err != nil {
		// A non-2xx response makes OpenRouter deliver the event again.
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "could not record batch callback")
		return
	}
	if !woken {
		// Already collected by the fallback poll, canceled, or not ours.
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "accepted"})
}
