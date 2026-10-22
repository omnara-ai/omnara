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
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err == nil && int64(len(raw)) <= maxBodyBytes {
		_ = controller.SetReadDeadline(time.Time{})
		return raw, true
	}
	_ = controller.SetReadDeadline(time.Now())
	w.Header().Set("Connection", "close")
	var tooLarge *http.MaxBytesError
	if err == nil || errors.As(err, &tooLarge) {
		apierror.Write(w, openapi.ErrorCodeRequestTooLarge)
	} else {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid request body")
	}
	return nil, false
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
