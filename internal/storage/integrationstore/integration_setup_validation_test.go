package integrationstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSetupValidation(t *testing.T) {
	base := ConfigureIntegrationInput{
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          IntegrationProviderSlack,
		IntegrationID:     uuid.New(), ExpectedSetupRevision: 1, CredentialVersionID: uuid.New(),
		ProviderTenantID:   "tenant",
		ProviderAccountRef: "router",
		CredentialSecretID: uuid.New(),
	}
	normalized, err := normalizeConfigureIntegrationInput(base)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(normalized.ProviderConfig))
	tests := []struct {
		name, want string
		change     func(*ConfigureIntegrationInput)
	}{
		{
			"unknown provider",
			"unsupported integration provider",
			func(i *ConfigureIntegrationInput) { i.Provider = "unknown" },
		},
		{
			"missing owner",
			"installed-by user",
			func(i *ConfigureIntegrationInput) { i.InstalledByUserID = uuid.Nil },
		},
		{
			"missing identity",
			"provider_account_ref",
			func(i *ConfigureIntegrationInput) { i.ProviderAccountRef = " " },
		},
		{
			"byte bound",
			"512 bytes",
			func(i *ConfigureIntegrationInput) { i.ProviderTenantID = strings.Repeat("é", 257) },
		},
		{"nul text", "provider_tenant_id", func(i *ConfigureIntegrationInput) { i.ProviderTenantID = "bad\x00" }},
		{
			"invalid utf8",
			"provider_account_ref",
			func(i *ConfigureIntegrationInput) { i.ProviderAccountRef = string([]byte{255}) },
		},
		{"missing integration", "integration", func(i *ConfigureIntegrationInput) { i.IntegrationID = uuid.Nil }},
		{"stale revision", "revision", func(i *ConfigureIntegrationInput) { i.ExpectedSetupRevision = 0 }},
		{
			"missing verified version",
			"verified credential version",
			func(i *ConfigureIntegrationInput) { i.CredentialVersionID = uuid.Nil },
		},
		{
			"missing slack credential",
			"credential secret",
			func(i *ConfigureIntegrationInput) { i.CredentialSecretID = uuid.Nil },
		},
		{
			"copied config credential",
			"empty object",
			func(i *ConfigureIntegrationInput) { i.ProviderConfig = json.RawMessage(`{"token":"private"}`) },
		},
		{
			"json type",
			"provider_metadata",
			func(i *ConfigureIntegrationInput) { i.ProviderMetadata = json.RawMessage(`[]`) },
		},
		{
			"json nul",
			"valid JSON",
			func(i *ConfigureIntegrationInput) { i.ProviderIdentity = json.RawMessage(`{"id":"\u0000"}`) },
		},
		{"json bound", "16384 bytes", func(i *ConfigureIntegrationInput) {
			i.ProviderMetadata = json.RawMessage(`{"data":"` + strings.Repeat("x", 16384) + `"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.change(&input)
			_, err := normalizeConfigureIntegrationInput(input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestIntegrationProviderIdentities(t *testing.T) {
	input := ConfigureIntegrationInput{
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          IntegrationProviderGitHub,
		IntegrationID:     uuid.New(), ExpectedSetupRevision: 1,
		ProviderTenantID:    "00123",
		ProviderAccountRef:  "00456",
		CredentialSecretID:  uuid.New(),
		CredentialVersionID: uuid.New(),
		CredentialAppID:     123,
	}
	normalized, err := normalizeConfigureIntegrationInput(input)
	require.NoError(t, err)
	require.Equal(t, "123", normalized.ProviderTenantID)
	require.Equal(t, "456", normalized.ProviderAccountRef)
	for _, bad := range []string{"0", "-1", "repo-name", "9223372036854775808"} {
		changed := input
		changed.ProviderAccountRef = bad
		_, err := normalizeConfigureIntegrationInput(changed)
		require.ErrorContains(t, err, "Installation ID")
	}
	changed := input
	changed.CredentialAppID = 124
	_, err = normalizeConfigureIntegrationInput(changed)
	require.ErrorContains(t, err, "App ID must match")
	changed = input
	changed.CredentialVersionID = uuid.Nil
	_, err = normalizeConfigureIntegrationInput(changed)
	require.ErrorContains(t, err, "verified credential version")
	input.Provider = IntegrationProviderDiscord
	input.ProviderTenantID, input.ProviderAccountRef = "111", "222"
	input.ProviderConfig = json.RawMessage(`{"public_key":"` + strings.Repeat("AB", 32) + `"}`)
	normalized, err = normalizeConfigureIntegrationInput(input)
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"public_key":"`+strings.Repeat("ab", 32)+`"}`,
		string(normalized.ProviderConfig),
	)
	for _, bad := range []string{"0", "01", "+1", "-1", "18446744073709551616"} {
		changed := input
		changed.ProviderTenantID = bad
		_, err := normalizeConfigureIntegrationInput(changed)
		require.ErrorContains(t, err, "canonical")
	}
	for _, bad := range []string{
		`{"public_key":null}`,
		`{"public_key":"bad"}`,
		`{"public_key":3}`,
		`{"bot_token":"private"}`,
		`{"shard_count":1}`,
	} {
		changed := input
		changed.ProviderConfig = json.RawMessage(bad)
		_, err := normalizeConfigureIntegrationInput(changed)
		require.Error(t, err)
	}
}

func TestIntegrationDiscordOptionalConfig(t *testing.T) {
	input := ConfigureIntegrationInput{
		OrgID: uuid.New(), ProjectID: uuid.New(), InstalledByUserID: uuid.New(),
		Provider: IntegrationProviderDiscord, IntegrationID: uuid.New(), ExpectedSetupRevision: 1,
		CredentialVersionID: uuid.New(), CredentialSecretID: uuid.New(),
		ProviderTenantID: "111", ProviderAccountRef: "222",
	}
	for _, raw := range []string{"", "{}"} {
		input.ProviderConfig = json.RawMessage(raw)
		normalized, err := normalizeConfigureIntegrationInput(input)
		require.NoError(t, err)
		require.JSONEq(t, "{}", string(normalized.ProviderConfig))
		repeated, err := normalizeConfigureIntegrationInput(normalized)
		require.NoError(t, err)
		require.Equal(t, normalized.ProviderConfig, repeated.ProviderConfig)
	}
}
