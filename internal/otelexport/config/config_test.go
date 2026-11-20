package config

import (
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/stretchr/testify/require"
)

func TestValidateTypedAuth(t *testing.T) {
	secretID := uuid.New()
	base := func() Settings {
		return Settings{
			Endpoint:      "https://otel.example.com/v1/traces",
			Variant:       oteltrace.VariantOTel,
			Encoding:      oteltrace.EncodingProtobuf,
			SamplePercent: 100,
		}
	}

	t.Run("defaults empty auth_kind to none", func(t *testing.T) {
		got, err := Validate(base(), nil)
		require.NoError(t, err)
		require.Equal(t, AuthKindNone, got.AuthKind)
	})

	t.Run("rejects credential or options when none", func(t *testing.T) {
		s := base()
		s.CredentialSecretID = secretID
		_, err := Validate(s, nil)
		require.ErrorContains(t, err, "credential_secret_id")

		s = base()
		s.AuthOptions = AuthOptions{Username: "pk-lf"}
		_, err = Validate(s, nil)
		require.ErrorContains(t, err, "auth_options")
	})

	t.Run("bearer_token requires credential and reserves Authorization", func(t *testing.T) {
		s := base()
		s.AuthKind = AuthKindBearerToken
		_, err := Validate(s, nil)
		require.ErrorContains(t, err, "credential_secret_id")

		s.CredentialSecretID = secretID
		got, err := Validate(s, nil)
		require.NoError(t, err)
		require.Equal(t, AuthKindBearerToken, got.AuthKind)

		s.Headers = map[string]string{"authorization": "Bearer manual"}
		_, err = Validate(s, nil)
		require.ErrorContains(t, err, "managed by auth_kind")
	})

	t.Run("api_key_header validates header_name and reserves it", func(t *testing.T) {
		s := base()
		s.AuthKind = AuthKindAPIKeyHeader
		s.CredentialSecretID = secretID
		_, err := Validate(s, nil)
		require.ErrorContains(t, err, "header_name")

		s.AuthOptions = AuthOptions{HeaderName: "Content-Type"}
		_, err = Validate(s, nil)
		require.ErrorContains(t, err, "reserved")

		s.AuthOptions = AuthOptions{HeaderName: "x-api-key"}
		got, err := Validate(s, nil)
		require.NoError(t, err)
		require.Equal(t, "x-api-key", got.AuthOptions.HeaderName)

		s.SecretHeaders = map[string]uuid.UUID{"X-Api-Key": uuid.New()}
		_, err = Validate(s, nil)
		require.ErrorContains(t, err, "managed by auth_kind")
	})

	t.Run("basic validates username and reserves Authorization", func(t *testing.T) {
		s := base()
		s.AuthKind = AuthKindBasic
		s.CredentialSecretID = secretID
		_, err := Validate(s, nil)
		require.ErrorContains(t, err, "username")

		s.AuthOptions = AuthOptions{Username: "pk:bad"}
		_, err = Validate(s, nil)
		require.ErrorContains(t, err, ":")

		s.AuthOptions = AuthOptions{Username: "pk-lf-123"}
		got, err := Validate(s, nil)
		require.NoError(t, err)
		require.Equal(t, "pk-lf-123", got.AuthOptions.Username)

		s.SecretHeaders = map[string]uuid.UUID{"Authorization": uuid.New()}
		_, err = Validate(s, nil)
		require.ErrorContains(t, err, "managed by auth_kind")
	})
}

func TestFormatAuthHeader(t *testing.T) {
	name, value, err := FormatAuthHeader(AuthKindNone, AuthOptions{}, "")
	require.NoError(t, err)
	require.Empty(t, name)
	require.Empty(t, value)

	name, value, err = FormatAuthHeader(AuthKindBearerToken, AuthOptions{}, "bt_key_123")
	require.NoError(t, err)
	require.Equal(t, "Authorization", name)
	require.Equal(t, "Bearer bt_key_123", value)

	name, value, err = FormatAuthHeader(AuthKindAPIKeyHeader, AuthOptions{HeaderName: "x-api-key"}, "lsv2_key")
	require.NoError(t, err)
	require.Equal(t, "x-api-key", name)
	require.Equal(t, "lsv2_key", value)

	name, value, err = FormatAuthHeader(AuthKindBasic, AuthOptions{Username: "pk-lf-1"}, "sk-lf-2")
	require.NoError(t, err)
	require.Equal(t, "Authorization", name)
	require.Equal(t, "Basic "+base64.StdEncoding.EncodeToString([]byte("pk-lf-1:sk-lf-2")), value)

	_, _, err = FormatAuthHeader(AuthKindBasic, AuthOptions{Username: "pk-lf-1"}, "bad\nvalue")
	require.Error(t, err)
}
