package integrationstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestIntegrationConnectionValidation(t *testing.T) {
	base := SaveIntegrationConnectionInput{
		OrgID:              uuid.New(),
		ProjectID:          uuid.New(),
		InstalledByUserID:  uuid.New(),
		Provider:           IntegrationProviderSlack,
		State:              IntegrationConnectionStateActive,
		ProviderTenantID:   "tenant",
		ProviderAccountRef: "router",
		CredentialSecretID: uuid.New(),
	}
	normalized, err := normalizeSaveIntegrationConnectionInput(base)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(normalized.ProviderConfig))
	tests := []struct {
		name, want string
		change     func(*SaveIntegrationConnectionInput)
	}{
		{
			"unknown provider",
			"unsupported integration provider",
			func(i *SaveIntegrationConnectionInput) { i.Provider = "unknown" },
		},
		{
			"missing owner",
			"installed-by user",
			func(i *SaveIntegrationConnectionInput) { i.InstalledByUserID = uuid.Nil },
		},
		{
			"missing identity",
			"provider_account_ref",
			func(i *SaveIntegrationConnectionInput) { i.ProviderAccountRef = " " },
		},
		{
			"byte bound",
			"512 bytes",
			func(i *SaveIntegrationConnectionInput) { i.ProviderTenantID = strings.Repeat("é", 257) },
		},
		{"nul text", "provider_tenant_id", func(i *SaveIntegrationConnectionInput) { i.ProviderTenantID = "bad\x00" }},
		{
			"invalid utf8",
			"provider_account_ref",
			func(i *SaveIntegrationConnectionInput) { i.ProviderAccountRef = string([]byte{255}) },
		},
		{"state", "state", func(i *SaveIntegrationConnectionInput) { i.State = "unknown" }},
		{
			"external provider is unsupported",
			"unsupported integration provider",
			func(i *SaveIntegrationConnectionInput) { i.Provider = "external" },
		},
		{
			"missing slack credential",
			"credential secret",
			func(i *SaveIntegrationConnectionInput) { i.CredentialSecretID = uuid.Nil },
		},
		{
			"copied config credential",
			"empty object",
			func(i *SaveIntegrationConnectionInput) { i.ProviderConfig = json.RawMessage(`{"token":"private"}`) },
		},
		{
			"json type",
			"provider_metadata",
			func(i *SaveIntegrationConnectionInput) { i.ProviderMetadata = json.RawMessage(`[]`) },
		},
		{
			"json nul",
			"valid JSON",
			func(i *SaveIntegrationConnectionInput) { i.ProviderIdentity = json.RawMessage(`{"id":"\u0000"}`) },
		},
		{"json bound", "16384 bytes", func(i *SaveIntegrationConnectionInput) {
			i.ProviderMetadata = json.RawMessage(`{"data":"` + strings.Repeat("x", 16384) + `"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.change(&input)
			_, err := normalizeSaveIntegrationConnectionInput(input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestIntegrationConnectionProviderIdentities(t *testing.T) {
	input := SaveIntegrationConnectionInput{
		OrgID:               uuid.New(),
		ProjectID:           uuid.New(),
		InstalledByUserID:   uuid.New(),
		Provider:            IntegrationProviderGitHub,
		State:               IntegrationConnectionStateActive,
		ProviderTenantID:    "00123",
		ProviderAccountRef:  "00456",
		CredentialSecretID:  uuid.New(),
		CredentialVersionID: uuid.New(),
		CredentialAppID:     123,
	}
	normalized, err := normalizeSaveIntegrationConnectionInput(input)
	require.NoError(t, err)
	require.Equal(t, "123", normalized.ProviderTenantID)
	require.Equal(t, "456", normalized.ProviderAccountRef)
	for _, bad := range []string{"0", "-1", "repo-name", "9223372036854775808"} {
		changed := input
		changed.ProviderAccountRef = bad
		_, err := normalizeSaveIntegrationConnectionInput(changed)
		require.ErrorContains(t, err, "Installation ID")
	}
	changed := input
	changed.CredentialAppID = 124
	_, err = normalizeSaveIntegrationConnectionInput(changed)
	require.ErrorContains(t, err, "App ID must match")
	changed = input
	changed.CredentialVersionID = uuid.Nil
	_, err = normalizeSaveIntegrationConnectionInput(changed)
	require.ErrorContains(t, err, "validated version")
	input.Provider = IntegrationProviderDiscord
	input.ProviderTenantID, input.ProviderAccountRef = "111", "222"
	input.ProviderConfig = json.RawMessage(`{"public_key":"` + strings.Repeat("AB", 32) + `"}`)
	normalized, err = normalizeSaveIntegrationConnectionInput(input)
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"public_key":"`+strings.Repeat("ab", 32)+`","shard_count":1}`,
		string(normalized.ProviderConfig),
	)
	for _, bad := range []string{"0", "01", "+1", "-1", "18446744073709551616"} {
		changed := input
		changed.ProviderTenantID = bad
		_, err := normalizeSaveIntegrationConnectionInput(changed)
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
		_, err := normalizeSaveIntegrationConnectionInput(changed)
		require.Error(t, err)
	}
}

func TestIntegrationConnectionDiscordShardTopology(t *testing.T) {
	input := SaveIntegrationConnectionInput{
		OrgID:              uuid.New(),
		ProjectID:          uuid.New(),
		InstalledByUserID:  uuid.New(),
		Provider:           IntegrationProviderDiscord,
		State:              IntegrationConnectionStateActive,
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
			normalized, err := normalizeSaveIntegrationConnectionInput(input)
			require.NoError(t, err)
			require.JSONEq(t, test.want, string(normalized.ProviderConfig))
			repeated, err := normalizeSaveIntegrationConnectionInput(normalized)
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
			_, err := normalizeSaveIntegrationConnectionInput(input)
			require.ErrorContains(t, err, "shard_count must be an integer between 1 and 4096")
		})
	}
}
