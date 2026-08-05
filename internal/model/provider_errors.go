package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type ErrorKind = modelprotocol.ErrorKind

const (
	ErrorKindContextWindow       = modelprotocol.ErrorKindContextWindow
	ErrorKindPayloadTooLarge     = modelprotocol.ErrorKindPayloadTooLarge
	ErrorKindRateLimit           = modelprotocol.ErrorKindRateLimit
	ErrorKindTransient           = modelprotocol.ErrorKindTransient
	ErrorKindAuth                = modelprotocol.ErrorKindAuth
	ErrorKindBillingAccount      = modelprotocol.ErrorKindBillingAccount
	ErrorKindInvalidRequest      = modelprotocol.ErrorKindInvalidRequest
	ErrorKindProviderUnavailable = modelprotocol.ErrorKindProviderUnavailable
	ErrorKindReplayRejected      = modelprotocol.ErrorKindReplayRejected
	ErrorKindUnknown             = modelprotocol.ErrorKindUnknown
)

func ErrorKindFromHTTPStatus(statusCode int) ErrorKind {
	switch {
	case statusCode >= 300 && statusCode < 400:
		return ErrorKindInvalidRequest
	case statusCode == http.StatusPaymentRequired:
		return ErrorKindBillingAccount
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return ErrorKindAuth
	case statusCode == http.StatusRequestEntityTooLarge:
		return ErrorKindPayloadTooLarge
	case statusCode == http.StatusTooManyRequests:
		return ErrorKindRateLimit
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusConflict:
		return ErrorKindTransient
	case statusCode >= 500:
		return ErrorKindProviderUnavailable
	case statusCode >= 400:
		return ErrorKindInvalidRequest
	default:
		return ErrorKindUnknown
	}
}

type ProviderError struct {
	Kind       ErrorKind       `json:"kind"`
	Source     string          `json:"source,omitempty"`
	StatusCode int             `json:"status_code,omitempty"`
	Code       string          `json:"code,omitempty"`
	Message    string          `json:"message,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
	RetryAfter *RetryAfter     `json:"retry_after,omitempty"`
	Retryable  *bool           `json:"retryable,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Cause      error           `json:"-"`
}

type RetryAfter struct {
	DeltaSeconds      *int64     `json:"delta_seconds,omitempty"`
	DelayMilliseconds *int64     `json:"delay_milliseconds,omitempty"`
	HTTPDate          *time.Time `json:"http_date,omitempty"`
}

func ParseRetryAfter(value string) *RetryAfter {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return &RetryAfter{DeltaSeconds: &seconds}
	}
	if milliseconds, ok := parseRetryDelayMilliseconds(value, time.Second); ok {
		return &RetryAfter{DelayMilliseconds: &milliseconds}
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return nil
	}
	when = when.UTC()
	return &RetryAfter{HTTPDate: &when}
}

func ParseRetryAfterJSON(value json.RawMessage) *RetryAfter {
	if len(value) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return ParseRetryAfter(text)
	}
	var number json.Number
	if err := json.Unmarshal(value, &number); err == nil {
		return ParseRetryAfter(number.String())
	}
	return nil
}

func RetryAfterFromHeader(header http.Header) *RetryAfter {
	if milliseconds, ok := parseRetryDelayMilliseconds(
		header.Get("Retry-After-Ms"),
		time.Millisecond,
	); ok {
		return &RetryAfter{DelayMilliseconds: &milliseconds}
	}
	return ParseRetryAfter(header.Get("Retry-After"))
}

func parseRetryDelayMilliseconds(value string, unit time.Duration) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, false
	}
	milliseconds := math.Ceil(number * float64(unit/time.Millisecond))
	if milliseconds >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(milliseconds), true
}

func ShouldRetryFromHeader(header http.Header) *bool {
	switch strings.ToLower(strings.TrimSpace(header.Get("X-Should-Retry"))) {
	case "true":
		value := true
		return &value
	case "false":
		value := false
		return &value
	default:
		return nil
	}
}

func RequestIDFromHeader(header http.Header) string {
	if requestID := strings.TrimSpace(header.Get("Request-Id")); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(header.Get("X-Request-Id"))
}

type AmbiguousProviderOutcomeError struct {
	Err error
}

func (e *AmbiguousProviderOutcomeError) Error() string {
	if e == nil || e.Err == nil {
		return "provider outcome is ambiguous"
	}
	return e.Err.Error()
}

func (e *AmbiguousProviderOutcomeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func AmbiguousProviderOutcome(err error) error {
	if err == nil || IsAmbiguousProviderOutcome(err) {
		return err
	}
	return &AmbiguousProviderOutcomeError{Err: err}
}

func IsAmbiguousProviderOutcome(err error) bool {
	var ambiguous *AmbiguousProviderOutcomeError
	return errors.As(err, &ambiguous)
}

func (e ProviderError) Error() string {
	if e.Kind == "" {
		e.Kind = ErrorKindUnknown
	}
	if e.Message == "" {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e ProviderError) Unwrap() error {
	return e.Cause
}

func ClassifyError(err error) (ProviderError, bool) {
	var providerErr ProviderError
	if err == nil {
		return ProviderError{}, false
	}
	if errors.As(err, &providerErr) {
		if providerErr.Kind == "" {
			providerErr.Kind = ErrorKindUnknown
		}
		return providerErr, true
	}
	var classifier interface {
		ProviderErrorClassification() ProviderError
	}
	if errors.As(err, &classifier) {
		providerErr = classifier.ProviderErrorClassification()
		if providerErr.Kind == "" {
			providerErr.Kind = ErrorKindUnknown
		}
		return providerErr, true
	}
	return ProviderError{}, false
}
