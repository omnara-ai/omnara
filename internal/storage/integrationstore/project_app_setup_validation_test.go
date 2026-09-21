package integrationstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProjectAppSetupValidation(t *testing.T) {
	base := ConfigureProjectAppInput{
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          IntegrationProviderSlack,
		AppID:             uuid.New(), ExpectedSetupRevision: 1, CredentialVersionID: uuid.New(),
		ProviderTenantID:   "tenant",
		ProviderAccountRef: "router",
		CredentialSecretID: uuid.New(),
	}
	normalized, err := normalizeConfigureProjectAppInput(base)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(normalized.ProviderConfig))
	tests := []struct {
		name, want string
		change     func(*ConfigureProjectAppInput)
	}{
		{
			"unknown provider",
			"unsupported integration provider",
			func(i *ConfigureProjectAppInput) { i.Provider = "unknown" },
		},
		{
			"missing owner",
			"installed-by user",
			func(i *ConfigureProjectAppInput) { i.InstalledByUserID = uuid.Nil },
		},
		{
			"missing identity",
			"provider_account_ref",
			func(i *ConfigureProjectAppInput) { i.ProviderAccountRef = " " },
		},
		{
			"byte bound",
			"512 bytes",
			func(i *ConfigureProjectAppInput) { i.ProviderTenantID = strings.Repeat("é", 257) },
		},
		{"nul text", "provider_tenant_id", func(i *ConfigureProjectAppInput) { i.ProviderTenantID = "bad\x00" }},
		{
			"invalid utf8",
			"provider_account_ref",
			func(i *ConfigureProjectAppInput) { i.ProviderAccountRef = string([]byte{255}) },
		},
		{"missing app", "app", func(i *ConfigureProjectAppInput) { i.AppID = uuid.Nil }},
		{"stale revision", "revision", func(i *ConfigureProjectAppInput) { i.ExpectedSetupRevision = 0 }},
		{
			"missing verified version",
			"verified credential version",
			func(i *ConfigureProjectAppInput) { i.CredentialVersionID = uuid.Nil },
		},
		{
			"missing slack credential",
			"credential secret",
			func(i *ConfigureProjectAppInput) { i.CredentialSecretID = uuid.Nil },
		},
		{
			"copied config credential",
			"empty object",
			func(i *ConfigureProjectAppInput) { i.ProviderConfig = json.RawMessage(`{"token":"private"}`) },
		},
		{
			"json type",
			"provider_metadata",
			func(i *ConfigureProjectAppInput) { i.ProviderMetadata = json.RawMessage(`[]`) },
		},
		{
			"json nul",
			"valid JSON",
			func(i *ConfigureProjectAppInput) { i.ProviderIdentity = json.RawMessage(`{"id":"\u0000"}`) },
		},
		{"json bound", "16384 bytes", func(i *ConfigureProjectAppInput) {
			i.ProviderMetadata = json.RawMessage(`{"data":"` + strings.Repeat("x", 16384) + `"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.change(&input)
			_, err := normalizeConfigureProjectAppInput(input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestProjectAppProviderIdentities(t *testing.T) {
	input := ConfigureProjectAppInput{
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          IntegrationProviderGitHub,
		AppID:             uuid.New(), ExpectedSetupRevision: 1,
		ProviderTenantID:    "00123",
		ProviderAccountRef:  "00456",
		CredentialSecretID:  uuid.New(),
		CredentialVersionID: uuid.New(),
		CredentialAppID:     123,
	}
	normalized, err := normalizeConfigureProjectAppInput(input)
	require.NoError(t, err)
	require.Equal(t, "123", normalized.ProviderTenantID)
	require.Equal(t, "456", normalized.ProviderAccountRef)
	for _, bad := range []string{"0", "-1", "repo-name", "9223372036854775808"} {
		changed := input
		changed.ProviderAccountRef = bad
		_, err := normalizeConfigureProjectAppInput(changed)
		require.ErrorContains(t, err, "Installation ID")
	}
	changed := input
	changed.CredentialAppID = 124
	_, err = normalizeConfigureProjectAppInput(changed)
	require.ErrorContains(t, err, "App ID must match")
	changed = input
	changed.CredentialVersionID = uuid.Nil
	_, err = normalizeConfigureProjectAppInput(changed)
	require.ErrorContains(t, err, "verified credential version")
	input.Provider = IntegrationProviderDiscord
	input.ProviderTenantID, input.ProviderAccountRef = "111", "222"
	input.ProviderConfig = json.RawMessage(`{"public_key":"` + strings.Repeat("AB", 32) + `"}`)
	normalized, err = normalizeConfigureProjectAppInput(input)
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"public_key":"`+strings.Repeat("ab", 32)+`","shard_count":1}`,
		string(normalized.ProviderConfig),
	)
	for _, bad := range []string{"0", "01", "+1", "-1", "18446744073709551616"} {
		changed := input
		changed.ProviderTenantID = bad
		_, err := normalizeConfigureProjectAppInput(changed)
		require.ErrorContains(t, err, "canonical")
	}
	for _, bad := range []string{
		`{"public_key":null}`,
		`{"public_key":"bad"}`,
		`{"public_key":3}`,
		`{"bot_token":"private"}`,
	} {
		changed := input
		changed.ProviderConfig = json.RawMessage(bad)
		_, err := normalizeConfigureProjectAppInput(changed)
		require.Error(t, err)
	}
}

func TestProjectAppDiscordShardTopology(t *testing.T) {
	input := ConfigureProjectAppInput{
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          IntegrationProviderDiscord,
		AppID:             uuid.New(), ExpectedSetupRevision: 1, CredentialVersionID: uuid.New(),
		ProviderTenantID:   "111",
		ProviderAccountRef: "222",
		CredentialSecretID: uuid.New(),
	}
	for _, test := range []struct{ name, raw, want string }{
		{"omitted config", "", `{"shard_count":1}`},
		{"empty config", `{}`, `{"shard_count":1}`},
		{"minimum", `{"shard_count":1}`, `{"shard_count":1}`},
		{"maximum", `{"shard_count":4096}`, `{"shard_count":4096}`},
		{
			"public key and topology",
			`{"public_key":"` + strings.Repeat("AB", 32) + `","shard_count":4}`,
			`{"public_key":"` + strings.Repeat("ab", 32) + `","shard_count":4}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input.ProviderConfig = json.RawMessage(test.raw)
			normalized, err := normalizeConfigureProjectAppInput(input)
			require.NoError(t, err)
			require.JSONEq(t, test.want, string(normalized.ProviderConfig))
			repeated, err := normalizeConfigureProjectAppInput(normalized)
			require.NoError(t, err)
			require.Equal(t, normalized.ProviderConfig, repeated.ProviderConfig)
		})
	}
	for _, bad := range []string{
		`0`,
		`-1`,
		`4097`,
		`1.5`,
		`1e100`,
		`9223372036854775808`,
		`null`,
		`true`,
		`"2"`,
		`[]`,
		`{}`,
	} {
		t.Run("reject "+bad, func(t *testing.T) {
			input.ProviderConfig = json.RawMessage(`{"shard_count":` + bad + `}`)
			_, err := normalizeConfigureProjectAppInput(input)
			require.ErrorContains(t, err, "shard_count must be an integer between 1 and 4096")
		})
	}
}
