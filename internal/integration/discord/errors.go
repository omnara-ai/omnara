package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

type ErrorCode string

const (
	RateLimited      ErrorCode = "rate_limited"
	DeliveryUnknown  ErrorCode = "delivery_unknown"
	TransientFailure ErrorCode = "transient_failure"
	PermanentFailure ErrorCode = "permanent_failure"
	InvalidResponse  ErrorCode = "invalid_response"
	ScopeMismatch    ErrorCode = "scope_mismatch"
)

// APIError excludes provider text, URLs, credentials and transport error strings.
// A final DeliveryUnknown must not start a new send after the nonce window.
type APIError struct {
	Code         ErrorCode
	StatusCode   int
	ProviderCode int
	RetryAfter   time.Duration
	Global       bool
	cause        error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("discord %s (HTTP %d, code %d)", e.Code, e.StatusCode, e.ProviderCode)
}
func (e *APIError) Unwrap() error { return e.cause }

func contextCause(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func invalidResponse(mutation bool) *APIError {
	code := InvalidResponse
	if mutation {
		code = DeliveryUnknown
	}
	return &APIError{Code: code}
}

func responseError(status int, header http.Header, body []byte, mutation bool) *APIError {
	var payload struct {
		Code       int     `json:"code"`
		RetryAfter float64 `json:"retry_after"`
		Global     bool    `json:"global"`
	}
	_ = json.Unmarshal(body, &payload)
	err := &APIError{Code: PermanentFailure, StatusCode: status, ProviderCode: payload.Code}
	if status == http.StatusTooManyRequests {
		err.Code, err.Global = RateLimited, payload.Global || header.Get("X-Ratelimit-Global") == "true"
		err.RetryAfter = max(secondsDuration(payload.RetryAfter), parseRetryAfter(header.Get("Retry-After")))
		if err.RetryAfter <= 0 {
			err.RetryAfter = time.Second
		}
	} else if status >= 500 || status == http.StatusRequestTimeout {
		err.Code = TransientFailure
		if mutation {
			err.Code = DeliveryUnknown
		}
		err.RetryAfter = parseRetryAfter(header.Get("Retry-After"))
	}
	return err
}

func secondsDuration(seconds float64) time.Duration {
	if math.IsNaN(seconds) || seconds <= 0 {
		return 0
	}
	if seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds * float64(time.Second))
}

func parseRetryAfter(raw string) time.Duration {
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		return secondsDuration(seconds)
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		return max(0, time.Until(deadline))
	}
	return 0
}
