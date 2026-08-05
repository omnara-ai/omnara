package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

func TestWriteOpenAPIRequestErrorClassifiesOversizedBody(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    openapi.ErrorCode
		wantMessage string
	}{
		{
			name:        "oversized body",
			err:         &http.MaxBytesError{Limit: 1048576},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    openapi.ErrorCodeRequestTooLarge,
			wantMessage: "request too large",
		},
		{
			name:        "wrapped oversized body",
			err:         fmt.Errorf("decode request body: %w", &http.MaxBytesError{Limit: 1048576}),
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    openapi.ErrorCodeRequestTooLarge,
			wantMessage: "request too large",
		},
		{
			name:        "malformed body",
			err:         fmt.Errorf("field is required"),
			wantStatus:  http.StatusBadRequest,
			wantCode:    openapi.ErrorCodeValidationFailed,
			wantMessage: "validation failed: field is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
			writeOpenAPIRequestError(recorder, req, tt.err)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			var response openapi.Error
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != tt.wantCode || response.Error != tt.wantMessage {
				t.Fatalf("response = %+v, want code=%q message=%q", response, tt.wantCode, tt.wantMessage)
			}
		})
	}
}
