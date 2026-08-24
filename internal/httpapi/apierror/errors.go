package apierror

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type definition struct {
	status  int
	message string
}

var definitions = map[openapi.ErrorCode]definition{
	openapi.ErrorCodeInvalidRequest:          {http.StatusBadRequest, "invalid request"},
	openapi.ErrorCodeUnauthorized:            {http.StatusUnauthorized, "unauthorized"},
	openapi.ErrorCodeForbidden:               {http.StatusForbidden, "forbidden"},
	openapi.ErrorCodeNotFound:                {http.StatusNotFound, "not found"},
	openapi.ErrorCodeConflict:                {http.StatusConflict, "conflict"},
	openapi.ErrorCodeGone:                    {http.StatusGone, "gone"},
	openapi.ErrorCodeRequestTooLarge:         {http.StatusRequestEntityTooLarge, "request too large"},
	openapi.ErrorCodeUnsupportedMediaType:    {http.StatusUnsupportedMediaType, "unsupported media type"},
	openapi.ErrorCodeUnprocessable:           {http.StatusUnprocessableEntity, "unprocessable request"},
	openapi.ErrorCodeRateLimited:             {http.StatusTooManyRequests, "too many requests"},
	openapi.ErrorCodeInternalError:           {http.StatusInternalServerError, "internal server error"},
	openapi.ErrorCodeUpstreamError:           {http.StatusBadGateway, "upstream error"},
	openapi.ErrorCodeServiceUnavailable:      {http.StatusServiceUnavailable, "service unavailable"},
	openapi.ErrorCodeIdempotencyKeyConflict:  {http.StatusConflict, "idempotency key conflict"},
	openapi.ErrorCodeStateTransitionConflict: {http.StatusConflict, "state transition conflict"},
	openapi.ErrorCodeManagedWorkAdmissionDenied: {
		http.StatusConflict,
		storeerr.InsufficientOmnaraCreditsMessage,
	},
	openapi.ErrorCodeDaemonRuntimeUnregistered: {
		http.StatusGone,
		"daemon runtime is no longer registered for this machine",
	},
	openapi.ErrorCodeValidationFailed:          {http.StatusBadRequest, "validation failed"},
	openapi.ErrorCodeCsrfCheckFailed:           {http.StatusForbidden, "csrf check failed"},
	openapi.ErrorCodeAuthenticationUnavailable: {http.StatusServiceUnavailable, "authentication unavailable"},
}

type sentinelMapping struct {
	sentinel error
	code     openapi.ErrorCode
	opaque   bool
}

// ErrUnauthorized maps to 403/forbidden here because FromError classifies
// authorization failures; the authentication middleware maps the same sentinel
// to 401/unauthorized before requests reach handlers.
var sentinelCodes = []sentinelMapping{
	{storeerr.ErrNotFound, openapi.ErrorCodeNotFound, false},
	{storeerr.ErrInvalidRequest, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrNoClaimableAgentWakeup, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrAgentNotAdvanceable, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrRuntimeLockInactive, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrToolCallInProgress, openapi.ErrorCodeConflict, false},
	{storeerr.ErrInvalidToolCallDisposition, openapi.ErrorCodeInternalError, true},
	{storeerr.ErrNoOnlineDaemonRuntime, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrProcessTerminal, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrProcessTerminating, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrProcessAlreadyStopped, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrProcessStateUnknown, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrInvalidSecretRequest, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrInvalidActorRequest, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrInvalidModelProviderConfig, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrConflict, openapi.ErrorCodeConflict, false},
	{storeerr.ErrIdempotencyConflict, openapi.ErrorCodeIdempotencyKeyConflict, false},
	{storeerr.ErrAuthConnectorImmutable, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrAuthConnectorIdentityConflict, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrInvalidDeviceAuthFlow, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrDaemonRuntimeUnregistered, openapi.ErrorCodeDaemonRuntimeUnregistered, false},
	{storeerr.ErrDaemonInstanceSuperseded, openapi.ErrorCodeGone, false},
	{storeerr.ErrProcessActionReportBlocked, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrStateTransitionConflict, openapi.ErrorCodeStateTransitionConflict, false},
	{storeerr.ErrManagedWorkAdmissionDenied, openapi.ErrorCodeManagedWorkAdmissionDenied, true},
	{storeerr.ErrMachineProviderUnavailable, openapi.ErrorCodeServiceUnavailable, true},
	{storeerr.ErrModelGrantUnavailable, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrUnauthorized, openapi.ErrorCodeForbidden, false},
	{storeerr.ErrMCPOAuthFlowConsumed, openapi.ErrorCodeInvalidRequest, false},
	{storeerr.ErrIntegrationOAuthFlowConsumed, openapi.ErrorCodeInvalidRequest, false},
}

type ResponseError struct {
	Status  int
	Code    openapi.ErrorCode
	Message string
}

func (err ResponseError) Error() string {
	return err.Message
}

func FromCode(code openapi.ErrorCode, additionalText string) ResponseError {
	def, ok := definitions[code]
	if !ok {
		def = definitions[openapi.ErrorCodeInternalError]
		return ResponseError{Status: def.status, Code: openapi.ErrorCodeInternalError, Message: def.message}
	}
	message := def.message
	if additionalText != "" && additionalText != message {
		message += ": " + additionalText
	}
	return ResponseError{Status: def.status, Code: code, Message: message}
}

func FromError(err error) ResponseError {
	if errors.Is(err, pgx.ErrNoRows) {
		return FromCode(openapi.ErrorCodeNotFound, err.Error())
	}
	for _, mapping := range sentinelCodes {
		if !errors.Is(err, mapping.sentinel) {
			continue
		}
		if mapping.opaque {
			return FromCode(mapping.code, "")
		}
		return FromCode(mapping.code, err.Error())
	}
	return FromCode(openapi.ErrorCodeInternalError, "")
}

func UserScoped(err error) ResponseError {
	return FromError(err)
}

func OrgScoped(err error) ResponseError {
	return FromError(err)
}

func ProjectScoped(err error) ResponseError {
	return FromError(err)
}

func Body(code openapi.ErrorCode, additionalText ...string) openapi.Error {
	detail := ""
	if len(additionalText) > 0 {
		detail = additionalText[0]
	}
	err := FromCode(code, detail)
	return openapi.Error{Error: err.Message, Code: err.Code}
}

func Write(w http.ResponseWriter, code openapi.ErrorCode, additionalText ...string) {
	detail := ""
	if len(additionalText) > 0 {
		detail = additionalText[0]
	}
	err := FromCode(code, detail)
	httpjson.Write(w, err.Status, openapi.Error{Error: err.Message, Code: err.Code})
}

func WriteError(w http.ResponseWriter, err error) {
	var responseError ResponseError
	if errors.As(err, &responseError) && responseError.write(w) == nil {
		return
	}
	Write(w, openapi.ErrorCodeInternalError)
}

func (err ResponseError) write(w http.ResponseWriter) error {
	def, ok := definitions[err.Code]
	if !ok || def.status != err.Status {
		err = FromCode(openapi.ErrorCodeInternalError, "")
		def = definitions[openapi.ErrorCodeInternalError]
	}
	message := err.Message
	if message == "" {
		message = def.message
	}

	var buf bytes.Buffer
	if encodeErr := json.NewEncoder(&buf).Encode(openapi.Error{Error: message, Code: err.Code}); encodeErr != nil {
		return encodeErr
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(def.status)
	_, writeErr := buf.WriteTo(w)
	return writeErr
}
