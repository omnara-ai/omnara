package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ErrorCode string

const (
	RateLimited        ErrorCode = "rate_limited"
	DeliveryUnknown    ErrorCode = "delivery_unknown"
	TransientFailure   ErrorCode = "transient_failure"
	PermanentFailure   ErrorCode = "permanent_failure"
	InvalidResponse    ErrorCode = "invalid_response"
	ResponseTooLarge   ErrorCode = "response_too_large"
	ScopeMismatch      ErrorCode = "scope_mismatch"
	UnsupportedAccount ErrorCode = "unsupported_account"
)

type APIError struct {
	Code       ErrorCode
	StatusCode int
	RetryAfter time.Duration
	cause      error
}

func (e *APIError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("github %s (HTTP %d): %v", e.Code, e.StatusCode, e.cause)
	}
	return fmt.Sprintf("github %s (HTTP %d)", e.Code, e.StatusCode)
}

func (e *APIError) Unwrap() error { return e.cause }

func (e *APIError) RetryDelay() time.Duration { return e.RetryAfter }

func contextCause(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return nil
}

func responseError(status int, header http.Header, body []byte, mutation bool, now time.Time) *APIError {
	result := &APIError{Code: PermanentFailure, StatusCode: status}
	var payload struct {
		Message string            `json:"message"`
		Errors  []json.RawMessage `json:"errors"`
	}
	_ = json.Unmarshal(body, &payload)
	message := strings.ToLower(payload.Message)
	if status == http.StatusUnprocessableEntity {
		messages := []string{message}
		for _, item := range payload.Errors {
			var text string
			if json.Unmarshal(item, &text) != nil {
				var detail struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(item, &detail)
				text = detail.Message
			}
			messages = append(messages, strings.ToLower(text))
		}
		for _, text := range messages {
			if strings.Contains(text, "can only have one pending review") {
				result.cause = errors.New("this bot already has a pending review on this PR; read pending_review to " +
					"continue it, submit_review to publish it, or discard_review to delete it")
				break
			}
		}
	}
	limited := status == http.StatusTooManyRequests || (status == http.StatusForbidden &&
		(header.Get("X-Ratelimit-Remaining") == "0" || header.Get("Retry-After") != "" ||
			strings.Contains(message, "rate limit") || strings.Contains(message, "abuse detection")))
	if limited {
		result.Code = RateLimited
		result.RetryAfter = time.Minute
		if delay, ok := retryAfter(header.Get("Retry-After"), now); ok {
			result.RetryAfter = delay
		}
		if header.Get("X-Ratelimit-Remaining") == "0" {
			if seconds, err := strconv.ParseInt(header.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
				if delay := time.Unix(seconds, 0).Sub(now); delay > result.RetryAfter {
					result.RetryAfter = delay
				}
			}
		}
		return result
	}
	if status >= 500 || status == http.StatusRequestTimeout {
		result.Code = TransientFailure
		if mutation {
			result.Code = DeliveryUnknown
		}
		if delay, ok := retryAfter(header.Get("Retry-After"), now); ok {
			result.RetryAfter = delay
		}
	}
	return result
}

func retryAfter(raw string, now time.Time) (time.Duration, bool) {
	const maxSeconds = math.MaxInt64 / int64(time.Second)
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, maxSeconds)) * time.Second, true
	}
	if until, err := http.ParseTime(raw); err == nil {
		return max(0, until.Sub(now)), true
	}
	return 0, false
}
