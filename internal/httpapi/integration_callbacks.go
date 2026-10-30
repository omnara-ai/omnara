package httpapi

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func readIntegrationCallbackBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	deadline := time.Now().Add(integrationIntakeTimeout)
	if requestDeadline, ok := r.Context().Deadline(); ok && requestDeadline.Before(deadline) {
		deadline = requestDeadline
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(deadline)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		closeHTTP1Connection(w, r)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Write(w, openapi.ErrorCodeRequestTooLarge)
		} else {
			apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid request body")
		}
		return nil, false
	}
	_ = controller.SetReadDeadline(time.Time{})
	return raw, true
}

func writeIntegrationProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storeerr.ErrNotFound):
		apierror.Write(w, openapi.ErrorCodeNotFound)
	case errors.Is(err, storeerr.ErrUnauthorized):
		apierror.Write(w, openapi.ErrorCodeForbidden)
	case errors.Is(err, storeerr.ErrConflict), errors.Is(err, storeerr.ErrIdempotencyConflict):
		apierror.Write(w, openapi.ErrorCodeConflict)
	default:
		apierror.Write(w, openapi.ErrorCodeInternalError)
	}
}
