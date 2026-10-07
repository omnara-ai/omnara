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
