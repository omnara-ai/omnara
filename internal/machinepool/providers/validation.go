package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

func DecodeStrictJSON(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func DecodeStringOptions(
	rawOptions map[string]json.RawMessage,
	what string,
	targets map[string]*string,
) error {
	for key, raw := range rawOptions {
		target, ok := targets[key]
		if !ok {
			return fmt.Errorf(`decode %s: json: unknown field %q`, what, key)
		}
		if len(raw) == 0 {
			raw = json.RawMessage(`null`)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return fmt.Errorf("decode %s.%s: %w", what, key, err)
		}
	}
	return nil
}

func ValidateImageRef(image string) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return errors.New("image reference must be non-empty")
	}
	if image == "*" {
		return errors.New(`image reference must not be "*"`)
	}
	return nil
}

func ValidateDNSLabel(value string) error {
	if value == "" || len(value) > 63 {
		return fmt.Errorf("%q must be a valid DNS label", value)
	}
	for i, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-'
		if !valid || char == '-' && (i == 0 || i == len(value)-1) {
			return fmt.Errorf("%q must be a valid DNS label", value)
		}
	}
	return nil
}

func NormalizeAllowlist(
	what string,
	values []string,
	validate func(string) error,
) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("%s must not be empty", what)
	}
	normalized := make([]string, len(values))
	hasWildcard := false
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "*" {
			hasWildcard = true
		} else if err := validate(value); err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", what, i, err)
		}
		normalized[i] = value
	}
	if hasWildcard && len(normalized) != 1 {
		return nil, fmt.Errorf("%s cannot mix wildcard with concrete values", what)
	}
	return normalized, nil
}

func ValidateAllowedValue(what, configKey, value string, allowed []string, defaultValue string) error {
	if allowed == nil {
		allowed = []string{defaultValue}
	}
	if slices.Contains(allowed, "*") || slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s %q is not allowed by provider_config.%s", what, value, configKey)
}
