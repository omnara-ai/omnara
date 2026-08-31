package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestEverySentinelCodeHasDefinition(t *testing.T) {
	for _, mapping := range sentinelCodes {
		if _, ok := definitions[mapping.code]; !ok {
			t.Errorf("sentinel %q maps to undefined code %q", mapping.sentinel, mapping.code)
		}
	}
}

func TestFromError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   openapi.ErrorCode
	}{
		{
			"sentinel",
			storeerr.ErrStateTransitionConflict,
			http.StatusConflict,
			openapi.ErrorCodeStateTransitionConflict,
		},
		{
			"wrapped sentinel",
			fmt.Errorf("advance agent: %w", storeerr.ErrAgentNotAdvanceable),
			http.StatusBadRequest,
			openapi.ErrorCodeInvalidRequest,
		},
		{
			"tagged invalid request",
			storeerr.InvalidRequest(errors.New("name is required")),
			http.StatusBadRequest,
			openapi.ErrorCodeInvalidRequest,
		},
		{
			"tagged device auth flow",
			storeerr.Tag(storeerr.ErrInvalidDeviceAuthFlow, errors.New("name cannot include control characters")),
			http.StatusBadRequest,
			openapi.ErrorCodeInvalidRequest,
		},
		{"not found sentinel", storeerr.ErrNotFound, http.StatusNotFound, openapi.ErrorCodeNotFound},
		{
			"wrapped pgx no rows",
			fmt.Errorf("load agent: %w", pgx.ErrNoRows),
			http.StatusNotFound,
			openapi.ErrorCodeNotFound,
		},
		{
			"idempotency conflict",
			storeerr.ErrIdempotencyConflict,
			http.StatusConflict,
			openapi.ErrorCodeIdempotencyKeyConflict,
		},
		{
			"unauthorized maps to forbidden",
			storeerr.ErrUnauthorized,
			http.StatusForbidden,
			openapi.ErrorCodeForbidden,
		},
		{
			"daemon runtime unregistered",
			storeerr.ErrDaemonRuntimeUnregistered,
			http.StatusGone,
			openapi.ErrorCodeDaemonRuntimeUnregistered,
		},
		{
			"managed work admission denied",
			storeerr.ErrManagedWorkAdmissionDenied,
			http.StatusConflict,
			openapi.ErrorCodeManagedWorkAdmissionDenied,
		},
		{
			"unclassified",
			errors.New("pq: connection reset"),
			http.StatusInternalServerError,
			openapi.ErrorCodeInternalError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromError(tt.err)
			if got.Status != tt.wantStatus || got.Code != tt.wantCode {
				t.Fatalf(
					"FromError(%v) = (%d, %q), want (%d, %q)",
					tt.err, got.Status, got.Code, tt.wantStatus, tt.wantCode,
				)
			}
			if !errors.Is(got, tt.err) {
				t.Fatalf("FromError(%v) does not preserve its cause", tt.err)
			}
		})
	}
}

func TestFromErrorHidesUnrecognizedMessages(t *testing.T) {
	got := FromError(errors.New("pq: duplicate key value violates unique constraint"))
	if got.Status != http.StatusInternalServerError || got.Message != "internal server error" {
		t.Fatalf("FromError unrecognized = (%d, %q), want opaque 500", got.Status, got.Message)
	}
}

func TestFromErrorHidesOpaqueSentinelDetail(t *testing.T) {
	err := fmt.Errorf(
		"provision machine: provider says quota exceeded: %w",
		storeerr.ErrMachineProviderUnavailable,
	)
	got := FromError(err)
	if got.Status != http.StatusServiceUnavailable || got.Code != openapi.ErrorCodeServiceUnavailable {
		t.Fatalf("FromError(%v) = %+v, want service unavailable", err, got)
	}
	if got.Message != "service unavailable" {
		t.Fatalf("FromError(%v).Message = %q, want opaque base message", err, got.Message)
	}
	admission := FromError(fmt.Errorf(
		"operator policy detail: %w",
		storeerr.ErrManagedWorkAdmissionDenied,
	))
	if admission.Message != "Insufficient Omnara credits." {
		t.Fatalf("managed admission message = %q, want opaque base message", admission.Message)
	}
}

func TestScopedMappersUseSentinels(t *testing.T) {
	crossScopeErr := fmt.Errorf("machine does not belong to project: %w", storeerr.ErrNotFound)
	for name, mapper := range map[string]func(error) ResponseError{
		"user":    UserScoped,
		"org":     OrgScoped,
		"project": ProjectScoped,
	} {
		t.Run(name, func(t *testing.T) {
			got := mapper(crossScopeErr)
			if got.Status != http.StatusNotFound || got.Code != openapi.ErrorCodeNotFound {
				t.Fatalf("mapper(%v) = %+v, want not-found response", crossScopeErr, got)
			}
		})
	}
}

func TestFromErrorDoesNotClassifyByMessage(t *testing.T) {
	err := errors.New("machine does not belong to project")
	got := FromError(err)
	if got.Status != http.StatusInternalServerError || got.Code != openapi.ErrorCodeInternalError {
		t.Fatalf("FromError(%v) = %+v, want opaque internal error", err, got)
	}
}

func TestFromCodeDeterminesHTTPStatus(t *testing.T) {
	tests := []struct {
		code        openapi.ErrorCode
		wantStatus  int
		wantMessage string
	}{
		{openapi.ErrorCodeValidationFailed, http.StatusBadRequest, "validation failed: field is required"},
		{openapi.ErrorCodeIdempotencyKeyConflict, http.StatusConflict, "idempotency key conflict: field is required"},
		{
			openapi.ErrorCodeDaemonRuntimeUnregistered,
			http.StatusGone,
			"daemon runtime is no longer registered for this machine: field is required",
		},
		{
			openapi.ErrorCodeAuthenticationUnavailable,
			http.StatusServiceUnavailable,
			"authentication unavailable: field is required",
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := FromCode(tt.code, "field is required")
			if got.Status != tt.wantStatus {
				t.Fatalf("FromCode(%q).Status = %d, want %d", tt.code, got.Status, tt.wantStatus)
			}
			if got.Message != tt.wantMessage {
				t.Fatalf("FromCode(%q).Message = %q, want %q", tt.code, got.Message, tt.wantMessage)
			}
		})
	}
}

func TestFromCodeUsesDefinitionMessageWithoutAdditionalText(t *testing.T) {
	got := FromCode(openapi.ErrorCodeForbidden, "")
	if got.Status != http.StatusForbidden || got.Message != "forbidden" {
		t.Fatalf("FromCode(forbidden) = %+v, want mapped default response", got)
	}
}

func TestWriteUsesCodeDefinitionAndOptionalDetail(t *testing.T) {
	tests := []struct {
		name        string
		code        openapi.ErrorCode
		detail      string
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "base message",
			code:        openapi.ErrorCodeUnauthorized,
			wantStatus:  http.StatusUnauthorized,
			wantMessage: "unauthorized",
		},
		{
			name:        "contextual detail",
			code:        openapi.ErrorCodeValidationFailed,
			detail:      "name is required",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "validation failed: name is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			if tt.detail == "" {
				Write(recorder, tt.code)
			} else {
				Write(recorder, tt.code, tt.detail)
			}
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			var response openapi.Error
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != tt.code || response.Error != tt.wantMessage {
				t.Fatalf("response = %+v, want code=%q message=%q", response, tt.code, tt.wantMessage)
			}
		})
	}
}

func TestBodyMatchesWriteShape(t *testing.T) {
	body := Body(openapi.ErrorCodeServiceUnavailable, "event stream unavailable")
	if body.Code != openapi.ErrorCodeServiceUnavailable ||
		body.Error != "service unavailable: event stream unavailable" {
		t.Fatalf("Body = %+v, want service unavailable with detail", body)
	}
	opaque := Body(openapi.ErrorCodeInternalError)
	if opaque.Code != openapi.ErrorCodeInternalError || opaque.Error != "internal server error" {
		t.Fatalf("Body(internal) = %+v, want opaque base message", opaque)
	}
}

func TestUnknownCodeIsOpaque(t *testing.T) {
	got := FromCode(openapi.ErrorCode("unknown_code"), "sensitive detail")
	if got.Status != http.StatusInternalServerError || got.Code != openapi.ErrorCodeInternalError ||
		got.Message != "internal server error" {
		t.Fatalf("FromCode(unknown) = %+v, want opaque internal error", got)
	}
}

func TestWithIssuesWritesFieldLevelIssues(t *testing.T) {
	line := 4
	err := WithIssues(
		openapi.ErrorCodeInvalidRequest,
		"agent config is invalid: model.name: required field is missing",
		[]openapi.AgentConfigErrorIssue{{Path: "/model/name", Message: "required field is missing", Line: &line}},
	)
	recorder := httptest.NewRecorder()
	WriteError(recorder, err)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	var response openapi.Error
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &response); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if response.Error != "invalid request: agent config is invalid: model.name: required field is missing" {
		t.Fatalf("message = %q", response.Error)
	}
	if response.Issues == nil || len(*response.Issues) != 1 {
		t.Fatalf("issues = %+v, want one issue", response.Issues)
	}
	issue := (*response.Issues)[0]
	if issue.Path != "/model/name" || issue.Line == nil || *issue.Line != 4 || issue.Column != nil {
		t.Fatalf("issue = %+v", issue)
	}
	if plain := Body(openapi.ErrorCodeInvalidRequest, "x"); plain.Issues != nil {
		t.Fatalf("issue-less body should omit issues, got %+v", plain.Issues)
	}
}
