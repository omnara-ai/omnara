package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
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
	var timeout net.Error
	switch {
	case err == nil || errors.As(err, &tooLarge):
		logpkg.Error(r.Context(), errors.New("read integration callback body: request body too large"))
		apierror.Write(w, openapi.ErrorCodeRequestTooLarge)
		return nil, false
	case errors.As(err, &timeout) && timeout.Timeout():
		logpkg.Error(r.Context(), fmt.Errorf("read integration callback body: %w", os.ErrDeadlineExceeded))
	default:
		logpkg.Error(r.Context(), fmt.Errorf("read integration callback body: %T", err))
	}
	apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid request body")
	return nil, false
}

func writeIntegrationProviderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storeerr.ErrNotFound):
		apierror.Write(w, openapi.ErrorCodeNotFound)
	case errors.Is(err, storeerr.ErrUnauthorized):
		apierror.Write(w, openapi.ErrorCodeForbidden)
	case errors.Is(err, storeerr.ErrConflict), errors.Is(err, storeerr.ErrIdempotencyConflict):
		apierror.Write(w, openapi.ErrorCodeConflict)
	default:
		logIntegrationError(r.Context(), "integration callback", err)
		apierror.Write(w, openapi.ErrorCodeInternalError)
	}
}
