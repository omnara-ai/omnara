package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

// MaxProviderIdentityBytes stays below PostgreSQL's B-tree entry limit for composite indexes.
const MaxProviderIdentityBytes = 2_000

func ValidateProviderJSON(body []byte) error {
	if !utf8.Valid(body) {
		return errors.New("provider response contains invalid UTF-8")
	}
	if !json.Valid(body) {
		return errors.New("provider response is not valid JSON")
	}
	if err := dbsafe.JSONStrings(body); err != nil {
		return errors.New("provider response contains a NUL string value")
	}
	return nil
}

func ValidateProviderResponse(response Response) error {
	if err := validateProviderRaw(response.ProviderReplay); err != nil {
		return fmt.Errorf("provider replay: %w", err)
	}
	if err := modelenvelope.ValidateProviderReportedCostUSD(response.ProviderReportedCostUSD); err != nil {
		return fmt.Errorf("provider-reported cost: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "response id", value: response.ID},
		{name: "request id", value: response.ProviderRequestID},
		{name: "served model slug", value: response.ServedProviderModelSlug},
	} {
		if err := validateProviderIdentity(field.value); err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if err := validateProviderString(string(response.StopReason)); err != nil {
		return fmt.Errorf("response stop reason: %w", err)
	}
	for index, part := range response.Content {
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "type", value: string(part.Type)},
			{name: "text", value: part.Text},
		} {
			if err := validateProviderString(field.value); err != nil {
				return fmt.Errorf("response content part %d %s: %w", index, field.name, err)
			}
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "provider call id", value: part.ProviderCallID},
			{name: "tool name", value: part.ToolName},
		} {
			if err := validateProviderIdentity(field.value); err != nil {
				return fmt.Errorf("response content part %d %s: %w", index, field.name, err)
			}
		}
		if err := validateProviderRaw(part.ToolInput); err != nil {
			return fmt.Errorf("response content part %d tool input: %w", index, err)
		}
		if part.Type == ResponsePartTypeToolCall {
			if _, err := modelenvelope.NormalizeToolInput(part.ToolInput); err != nil {
				return fmt.Errorf("response content part %d: %w", index, err)
			}
		}
	}
	return nil
}

func ResponseEvidenceForStorage(response Response) Response {
	evidence := Response{
		ID:                      response.ID,
		ProviderRequestID:       response.ProviderRequestID,
		ServedProviderModelSlug: response.ServedProviderModelSlug,
		ProviderReportedCostUSD: response.ProviderReportedCostUSD,
		ProviderMetadata:        response.ProviderMetadata,
		Usage:                   response.Usage,
	}
	if err := ValidateProviderResponse(evidence); err != nil {
		return Response{}
	}
	return evidence
}

func MalformedProviderResponse(source string, cause error) error {
	return MalformedProviderSuccess(
		source,
		"malformed_success_response",
		"The model provider returned a malformed successful response.",
		cause,
	)
}

func MalformedProviderSuccess(source, code, message string, cause error) error {
	return AmbiguousProviderOutcome(ProviderError{
		Kind:    ErrorKindUnknown,
		Source:  source,
		Code:    code,
		Message: message,
		Cause:   cause,
	})
}

func validateProviderString(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("contains invalid UTF-8")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("contains a NUL byte")
	}
	return nil
}

func validateProviderIdentity(value string) error {
	if err := validateProviderString(value); err != nil {
		return err
	}
	if len(value) > MaxProviderIdentityBytes {
		return fmt.Errorf("exceeds %d bytes", MaxProviderIdentityBytes)
	}
	return nil
}

func validateProviderRaw(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if !utf8.Valid(raw) {
		return errors.New("contains invalid UTF-8")
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return errors.New("contains a NUL byte")
	}
	if json.Valid(raw) {
		if err := dbsafe.JSONStrings(raw); err != nil {
			return errors.New("contains a NUL string value")
		}
	}
	return nil
}
