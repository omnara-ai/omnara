package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCreateOrganizationStorageErrorIsDomainSpecificAndOpaque(t *testing.T) {
	server := strictOpenAPIServer{server: &Server{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "idempotency conflict",
			err:         storeerr.ErrIdempotencyConflict,
			wantStatus:  http.StatusConflict,
			wantMessage: "idempotency key conflict: organization request conflicts with an earlier attempt",
		},
		{
			name:        "capacity",
			err:         storeerr.ErrUnauthorized,
			wantStatus:  http.StatusForbidden,
			wantMessage: "forbidden",
		},
		{
			name:        "unknown database failure",
			err:         errors.New("internal table and connection details"),
			wantStatus:  http.StatusServiceUnavailable,
			wantMessage: "service unavailable: organization creation temporarily unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := server.createOrganizationStorageError("test organization storage", test.err)
			var responseError apierror.ResponseError
			if !errors.As(err, &responseError) {
				t.Fatalf("error = %T %v, want api response error", err, err)
			}
			if responseError.Status != test.wantStatus || responseError.Message != test.wantMessage {
				t.Fatalf("response error = %+v, want status=%d message=%q", responseError, test.wantStatus, test.wantMessage)
			}
		})
	}
}
