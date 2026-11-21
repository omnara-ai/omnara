package config

import (
	"encoding/base64"
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
const (
	MaxHeaders        = 20
	maxBasicUserBytes = 1024
)

type AuthKind string

const (
	AuthKindNone         AuthKind = "none"
	AuthKindBearerToken  AuthKind = "bearer_token"
	AuthKindAPIKeyHeader AuthKind = "api_key_header"
	AuthKindBasic        AuthKind = "basic"
)

type AuthOptions struct {
	HeaderName string `json:"header_name,omitempty"`
	Username   string `json:"username,omitempty"`
}

type Settings struct {
	Endpoint           string
	Variant            oteltrace.Variant
	Encoding           oteltrace.Encoding
	AuthKind           AuthKind
	AuthOptions        AuthOptions
	CredentialSecretID uuid.UUID
	Headers            map[string]string
	SecretHeaders      map[string]uuid.UUID
	SamplePercent      float64
}

// reservedHeaders are set by the sender or describe the connection and body
// framing it controls, so an exporter cannot override them.
var reservedHeaders = map[string]bool{
	"content-type": true, "content-length": true, "content-encoding": true, "host": true,
	"connection": true, "keep-alive": true, "proxy-connection": true, "transfer-encoding": true,
	"te": true, "trailer": true, "upgrade": true, "expect": true,
}

func ParseAuthKind(value string) (AuthKind, bool) {
	switch kind := AuthKind(value); kind {
	case "":
		return AuthKindNone, true
	case AuthKindNone, AuthKindBearerToken, AuthKindAPIKeyHeader, AuthKindBasic:
		return kind, true
	default:
		return "", false
	}
}

func validateBasicUsername(username string) error {
	if username == "" || len(username) > maxBasicUserBytes {
		return errors.New("auth_options.username must be between 1 and 1024 bytes")
	}
	for i := range len(username) {
		b := username[i]
		if b == ':' || b < 0x20 || b == 0x7f {
			return errors.New("auth_options.username cannot contain ':' or control characters")
		}
	}
	return nil
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
	authKind, ok := ParseAuthKind(string(settings.AuthKind))
	if !ok {
		return Settings{}, errors.New("unsupported exporter auth_kind")
	}
	settings.AuthKind = authKind
	var authHeaderLower string
	switch authKind {
	case AuthKindNone:
		if settings.CredentialSecretID != uuid.Nil {
			return Settings{}, errors.New("credential_secret_id cannot be set when auth_kind is none")
		}
		if settings.AuthOptions.HeaderName != "" || settings.AuthOptions.Username != "" {
			return Settings{}, errors.New("auth_options must be empty when auth_kind is none")
		}
	case AuthKindBearerToken:
		if settings.CredentialSecretID == uuid.Nil {
			return Settings{}, errors.New("credential_secret_id is required when auth_kind is bearer_token")
		}
		if settings.AuthOptions.HeaderName != "" || settings.AuthOptions.Username != "" {
			return Settings{}, errors.New("auth_options must be empty when auth_kind is bearer_token")
		}
		authHeaderLower = "authorization"
	case AuthKindAPIKeyHeader:
		if settings.CredentialSecretID == uuid.Nil {
			return Settings{}, errors.New("credential_secret_id is required when auth_kind is api_key_header")
		}
		if settings.AuthOptions.Username != "" {
			return Settings{}, errors.New("auth_options.username is not supported when auth_kind is api_key_header")
		}
		headerName := strings.TrimSpace(settings.AuthOptions.HeaderName)
		if !httpguts.ValidHeaderFieldName(headerName) {
			return Settings{}, errors.New("auth_options.header_name must be a valid HTTP header name")
		}
		lower := strings.ToLower(headerName)
		if reservedHeaders[lower] {
			return Settings{}, fmt.Errorf("auth_options.header_name %s is reserved", headerName)
		}
		settings.AuthOptions.HeaderName = headerName
		authHeaderLower = lower
	case AuthKindBasic:
		if settings.CredentialSecretID == uuid.Nil {
			return Settings{}, errors.New("credential_secret_id is required when auth_kind is basic")
		}
		if settings.AuthOptions.HeaderName != "" {
			return Settings{}, errors.New("auth_options.header_name is not supported when auth_kind is basic")
		}
		if err := validateBasicUsername(settings.AuthOptions.Username); err != nil {
			return Settings{}, err
		}
		authHeaderLower = "authorization"
	}
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
		case authHeaderLower != "" && lower == authHeaderLower:
			return fmt.Errorf("header %s is managed by auth_kind", name)
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

func FormatAuthHeader(kind AuthKind, options AuthOptions, secretValue string) (name, value string, err error) {
	switch kind {
	case AuthKindNone, "":
		return "", "", nil
	case AuthKindBearerToken:
		if secretValue == "" {
			return "", "", errors.New("bearer token credential is empty")
		}
		formatted := "Bearer " + secretValue
		if err := ValidateHeaderValue("Authorization", formatted); err != nil {
			return "", "", err
		}
		return "Authorization", formatted, nil
	case AuthKindAPIKeyHeader:
		if err := ValidateHeaderValue(options.HeaderName, secretValue); err != nil {
			return "", "", err
		}
		return options.HeaderName, secretValue, nil
	case AuthKindBasic:
		if secretValue == "" || len(secretValue) > 8192 || strings.ContainsAny(secretValue, "\r\n\x00") {
			return "", "", errors.New("basic auth credential has an invalid value")
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(options.Username + ":" + secretValue))
		return "Authorization", "Basic " + encoded, nil
	default:
		return "", "", fmt.Errorf("unsupported exporter auth_kind %q", kind)
	}
}

func ValidateHeaderValue(name, value string) error {
	if value == "" || len(value) > 8192 || !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("header %s has an invalid value", name)
	}
	return nil
}
