package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/urlpolicy"
	"golang.org/x/net/http/httpguts"
)

// MaxHeaders bounds each of headers and secret_headers, as the API schema does.
const MaxHeaders = 20

type Settings struct {
	Endpoint      string
	Variant       oteltrace.Variant
	Encoding      oteltrace.Encoding
	Headers       map[string]string
	SecretHeaders map[string]uuid.UUID
	SamplePercent float64
}

// reservedHeaders are set by the sender or describe the connection and body
// framing it controls, so an exporter cannot override them.
var reservedHeaders = map[string]bool{
	"content-type": true, "content-length": true, "content-encoding": true, "host": true,
	"connection": true, "keep-alive": true, "proxy-connection": true, "transfer-encoding": true,
	"te": true, "trailer": true, "upgrade": true, "expect": true,
}

func Validate(settings Settings, endpointAllowlist *ssrf.Allowlist) (Settings, error) {
	endpoint, err := urlpolicy.ValidateEndpointURL(settings.Endpoint, endpointAllowlist, "exporter endpoint")
	if err != nil {
		return Settings{}, err
	}
	settings.Endpoint = endpoint
	variant, ok := oteltrace.ParseVariant(string(settings.Variant))
	if !ok {
		return Settings{}, errors.New("unsupported exporter variant")
	}
	settings.Variant = variant
	encoding, ok := oteltrace.ParseEncoding(string(settings.Encoding))
	if !ok {
		return Settings{}, errors.New("unsupported exporter encoding")
	}
	if !slices.Contains(variant.Encodings(), encoding) {
		return Settings{}, fmt.Errorf("the %s variant does not accept %s encoding", variant, encoding)
	}
	settings.Encoding = encoding
	if !(settings.SamplePercent >= 0 && settings.SamplePercent <= 100) {
		return Settings{}, errors.New("sample_percent must be between 0 and 100")
	}
	if settings.Headers == nil {
		settings.Headers = map[string]string{}
	}
	if settings.SecretHeaders == nil {
		settings.SecretHeaders = map[string]uuid.UUID{}
	}
	if len(settings.Headers) > MaxHeaders || len(settings.SecretHeaders) > MaxHeaders {
		return Settings{}, fmt.Errorf("headers and secret_headers may each contain at most %d entries", MaxHeaders)
	}
	seen := make(map[string]bool, len(settings.Headers)+len(settings.SecretHeaders))
	validateName := func(name string) error {
		lower := strings.ToLower(name)
		switch {
		case !httpguts.ValidHeaderFieldName(name):
			return fmt.Errorf("invalid header name %q", name)
		case reservedHeaders[lower]:
			return fmt.Errorf("header %s is reserved", name)
		case seen[lower]:
			return fmt.Errorf("header %s is set more than once", name)
		}
		seen[lower] = true
		return nil
	}
	for name, value := range settings.Headers {
		if err := validateName(name); err != nil {
			return Settings{}, err
		}
		if err := ValidateHeaderValue(name, value); err != nil {
			return Settings{}, err
		}
	}
	for name, secretID := range settings.SecretHeaders {
		if err := validateName(name); err != nil {
			return Settings{}, err
		}
		if secretID == uuid.Nil {
			return Settings{}, fmt.Errorf("secret header %s requires a secret", name)
		}
	}
	return settings, nil
}

func ValidateHeaderValue(name, value string) error {
	if value == "" || len(value) > 8192 || !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("header %s has an invalid value", name)
	}
	return nil
}
