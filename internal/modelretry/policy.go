package modelretry

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	MaxModelCallRetriesPerOperation = executionstore.MaxModelCallRetriesPerOperation

	maxProviderMetadataBytes = 64 * 1024
	maxDuration              = time.Duration(math.MaxInt64)
)

type Action string

const (
	ActionRetry   Action = "retry"
	ActionCompact Action = "compact"
	ActionStop    Action = "stop"
)

type InputOverflowPolicy uint8

const (
	StopOnInputOverflow InputOverflowPolicy = iota
	CompactOnInputOverflow
)

type Decision struct {
	Action     Action
	RetryDelay time.Duration
}

type Attempt struct {
	Number int
}

type Evidence struct {
	Kind      model.ErrorKind
	Code      string
	Message   string
	Details   json.RawMessage
	RequestID string
	Ambiguous bool
	Provider  model.ProviderError
}

func Decide(
	err error,
	attempt Attempt,
	contextID string,
	now time.Time,
	inputOverflowPolicy InputOverflowPolicy,
) (Evidence, Decision) {
	evidence := EvidenceFor(err)
	now = now.UTC()
	decision := Decision{}
	if compactableInputFailure(evidence.Provider.Kind) {
		if inputOverflowPolicy == CompactOnInputOverflow {
			decision.Action = ActionCompact
		} else {
			decision.Action = ActionStop
		}
		return evidence, decision
	}
	if !retryable(evidence) {
		decision.Action = ActionStop
		return evidence, decision
	}
	if attempt.Number > MaxModelCallRetriesPerOperation {
		decision.Action = ActionStop
		return evidence, decision
	}
	providerRetryDelay, representable := retryAfterDelay(evidence.Provider.RetryAfter, now)
	if !representable {
		decision.Action = ActionStop
		return evidence, decision
	}

	decision.Action = ActionRetry
	decision.RetryDelay = backoff(attempt.Number, contextID)
	if providerRetryDelay > decision.RetryDelay {
		decision.RetryDelay = providerRetryDelay
	}
	return evidence, decision
}

func retryAfterDelay(retryAfter *model.RetryAfter, now time.Time) (time.Duration, bool) {
	providerRetryDelay := time.Duration(0)
	if retryAfter == nil {
		return providerRetryDelay, true
	}

	if retryAfter.DeltaSeconds != nil {
		seconds := *retryAfter.DeltaSeconds
		if seconds < 0 {
			seconds = 0
		}
		candidate, ok := providerDelaySeconds(seconds)
		if !ok {
			return 0, false
		}
		if candidate > providerRetryDelay {
			providerRetryDelay = candidate
		}
	}
	if retryAfter.DelayMilliseconds != nil {
		candidate, ok := providerDelayMilliseconds(*retryAfter.DelayMilliseconds)
		if !ok {
			return 0, false
		}
		if candidate > providerRetryDelay {
			providerRetryDelay = candidate
		}
	}
	if retryAfter.HTTPDate != nil {
		at := retryAfter.HTTPDate.UTC()
		if at.After(now.Add(maxDuration)) {
			return 0, false
		}
		candidate := at.Sub(now)
		if candidate < 0 {
			candidate = 0
		}
		if candidate > providerRetryDelay {
			providerRetryDelay = candidate
		}
	}
	return providerRetryDelay, true
}

func providerDelaySeconds(seconds int64) (time.Duration, bool) {
	if seconds <= 0 {
		return 0, true
	}
	if seconds > int64(maxDuration/time.Second) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func providerDelayMilliseconds(milliseconds int64) (time.Duration, bool) {
	if milliseconds <= 0 {
		return 0, true
	}
	if milliseconds > int64(maxDuration/time.Millisecond) {
		return 0, false
	}
	return time.Duration(milliseconds) * time.Millisecond, true
}

func EvidenceFor(err error) Evidence {
	providerErr, classified := model.ClassifyError(err)
	if !classified {
		providerErr = model.ProviderError{
			Kind:    model.ErrorKindUnknown,
			Code:    "request_failed",
			Message: errorText(err),
		}
	}
	if providerErr.Kind == "" {
		providerErr.Kind = model.ErrorKindUnknown
	}
	code := strings.TrimSpace(providerErr.Code)
	if code == "" {
		code = string(providerErr.Kind)
	}
	message := strings.TrimSpace(providerErr.Message)
	if message == "" {
		message = errorText(err)
	}
	message = sanitize(message)
	if message == "" {
		message = "The model provider request failed."
	}
	requestID := sanitize(providerErr.RequestID)
	ambiguous := model.IsAmbiguousProviderOutcome(err)
	detailFields := map[string]any{
		"source":      sanitize(providerErr.Source),
		"status_code": providerErr.StatusCode,
		"request_id":  requestID,
	}
	if ambiguous {
		detailFields["outcome_ambiguous"] = true
	}
	if providerErr.RetryAfter != nil {
		detailFields["retry_after"] = providerErr.RetryAfter
	}
	if providerErr.Retryable != nil {
		detailFields["provider_retryable"] = *providerErr.Retryable
	}
	if metadata, ok := providerMetadataForStorage(providerErr.Metadata); ok {
		detailFields["provider_metadata"] = metadata
	}
	details, marshalErr := json.Marshal(detailFields)
	if marshalErr != nil {
		details = json.RawMessage(`{}`)
	}
	return Evidence{
		Kind:      providerErr.Kind,
		Code:      sanitize(code),
		Message:   message,
		Details:   details,
		RequestID: requestID,
		Ambiguous: ambiguous,
		Provider:  providerErr,
	}
}

func providerMetadataForStorage(raw json.RawMessage) (json.RawMessage, bool) {
	metadata := bytes.TrimSpace(raw)
	if len(metadata) == 0 || len(metadata) > maxProviderMetadataBytes ||
		bytes.Equal(metadata, []byte("null")) {
		return nil, false
	}
	if err := model.ValidateProviderJSON(metadata); err != nil {
		return nil, false
	}
	var rangeChecked any
	if err := json.Unmarshal(metadata, &rangeChecked); err != nil {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.UseNumber()
	var exact any
	if err := decoder.Decode(&exact); err != nil {
		return nil, false
	}
	metadata, err := json.Marshal(exact)
	if err != nil {
		return nil, false
	}
	return metadata, true
}

func retryable(evidence Evidence) bool {
	if evidence.Ambiguous {
		return true
	}
	if evidence.Provider.Retryable != nil {
		return *evidence.Provider.Retryable
	}
	switch evidence.Provider.Kind {
	case model.ErrorKindTransient,
		model.ErrorKindRateLimit,
		model.ErrorKindProviderUnavailable,
		model.ErrorKindContextWindow,
		model.ErrorKindPayloadTooLarge,
		model.ErrorKindReplayRejected,
		model.ErrorKindUnknown:
		return true
	case model.ErrorKindAuth,
		model.ErrorKindBillingAccount,
		model.ErrorKindInvalidRequest:
		return false
	default:
		return false
	}
}

func compactableInputFailure(kind model.ErrorKind) bool {
	return kind == model.ErrorKindContextWindow || kind == model.ErrorKindPayloadTooLarge
}

func backoff(attemptNumber int, contextID string) time.Duration {
	return executionstore.ModelCallRetryBackoff(attemptNumber, contextID)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sanitize(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.TrimSpace(value)
	const maxLength = 2_000
	if len(value) > maxLength {
		end := maxLength
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		value = value[:end]
	}
	return value
}
