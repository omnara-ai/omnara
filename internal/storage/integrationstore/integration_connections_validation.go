package integrationstore

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/secrets"
)

// IntegrationConnectionCredentialKind describes the existing secret contract.
// Secret availability is checked again at the write boundary.
func IntegrationConnectionCredentialKind(provider string) (secrets.Kind, error) {
	switch provider {
	case IntegrationProviderSlack:
		return secrets.KindSlackAppCredentials, nil
	case IntegrationProviderGitHub:
		return secrets.KindGitHubAppCredentials, nil
	case IntegrationProviderDiscord:
		return secrets.KindGeneric, nil
	default:
		return "", fmt.Errorf("unsupported integration provider %q", provider)
	}
}

func normalizeSaveIntegrationConnectionInput(input SaveIntegrationConnectionInput) (
	SaveIntegrationConnectionInput,
	error,
) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil || input.InstalledByUserID == uuid.Nil {
		return input, errors.New("org, project, and installed-by user are required")
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.ProviderTenantID = strings.TrimSpace(input.ProviderTenantID)
	input.ProviderAccountRef = strings.TrimSpace(input.ProviderAccountRef)
	input.ProviderAgentDisplayName = strings.TrimSpace(input.ProviderAgentDisplayName)
	_, err := IntegrationConnectionCredentialKind(input.Provider)
	if err != nil {
		return input, err
	}
	if input.CredentialSecretID == uuid.Nil {
		return input, fmt.Errorf("credential secret is required for %s connections", input.Provider)
	}
	if input.State != IntegrationConnectionStateActive && input.State != IntegrationConnectionStateDisabled {
		return input, fmt.Errorf("unsupported integration connection state %q", input.State)
	}
	for _, field := range []struct{ name, value string }{
		{"provider_tenant_id", input.ProviderTenantID}, {"provider_account_ref", input.ProviderAccountRef},
	} {
		if field.value == "" || len(field.value) > 512 || !utf8.ValidString(field.value) ||
			dbsafe.Text(field.value) != nil {
			return input, fmt.Errorf("%s must be nonempty text of at most 512 bytes", field.name)
		}
	}
	if len(input.ProviderAgentDisplayName) > 512 || !utf8.ValidString(input.ProviderAgentDisplayName) ||
		dbsafe.Text(input.ProviderAgentDisplayName) != nil {
		return input, errors.New("provider_agent_display_name must be text of at most 512 bytes")
	}
	if input.Provider == IntegrationProviderGitHub {
		appID, err := strconv.ParseInt(input.ProviderTenantID, 10, 64)
		if err != nil || appID <= 0 {
			return input, errors.New("github provider_tenant_id must be a positive numeric App ID")
		}
		installationID, err := strconv.ParseInt(input.ProviderAccountRef, 10, 64)
		if err != nil || installationID <= 0 {
			return input, errors.New("github provider_account_ref must be a positive numeric Installation ID")
		}
		input.ProviderTenantID = strconv.FormatInt(appID, 10)
		input.ProviderAccountRef = strconv.FormatInt(installationID, 10)
		if input.CredentialVersionID == uuid.Nil || input.CredentialAppID != appID {
			return input, errors.New(
				"github credential App ID must match provider_tenant_id and its validated version is required",
			)
		}
	}
	if input.Provider == IntegrationProviderDiscord {
		for _, id := range []string{input.ProviderTenantID, input.ProviderAccountRef} {
			value, err := strconv.ParseUint(id, 10, 64)
			if err != nil || value == 0 || strconv.FormatUint(value, 10) != id {
				return input, errors.New(
					"discord application and bot user IDs must be canonical positive decimal snowflakes",
				)
			}
		}
	}
	for _, field := range []struct {
		name  string
		value *json.RawMessage
	}{
		{"provider_config", &input.ProviderConfig}, {
			"provider_identity",
			&input.ProviderIdentity,
		}, {"provider_metadata", &input.ProviderMetadata},
	} {
		*field.value, err = normalizedJSONObject(*field.value, field.name)
		if err != nil {
			return input, err
		}
		if len(*field.value) > 16*1024 || dbsafe.JSONStrings(*field.value) != nil {
			return input, fmt.Errorf("%s must be a valid JSON object of at most 16384 bytes", field.name)
		}
	}
	// Closed provider config objects prevent accidentally storing app behavior,
	// copied credentials, or arbitrary outbound endpoints on the connection.
	var config map[string]json.RawMessage
	if err := json.Unmarshal(input.ProviderConfig, &config); err != nil {
		return input, err
	}
	if input.Provider == IntegrationProviderDiscord {
		for key := range config {
			if key != "public_key" && key != "shard_count" {
				return input, fmt.Errorf("unsupported discord provider_config field %q", key)
			}
		}
		// Persist the configured topology rather than deriving it at runtime;
		// connection updated_at fences workers using the previous shard count.
		shardCount := 1
		if raw, ok := config["shard_count"]; ok {
			var count int
			if err := json.Unmarshal(raw, &count); err != nil || count < 1 || count > 4096 {
				return input, errors.New("discord shard_count must be an integer between 1 and 4096")
			}
			shardCount = count
		}
		config["shard_count"] = json.RawMessage(strconv.Itoa(shardCount))
		if raw, ok := config["public_key"]; ok {
			var publicKey string
			if err := json.Unmarshal(raw, &publicKey); err != nil {
				return input, errors.New("discord public_key must be a hex-encoded Ed25519 public key")
			}
			decoded, err := hex.DecodeString(publicKey)
			if err != nil || len(decoded) != ed25519.PublicKeySize {
				return input, errors.New("discord public_key must be a hex-encoded Ed25519 public key")
			}
			config["public_key"], err = json.Marshal(hex.EncodeToString(decoded))
			if err != nil {
				return input, err
			}
		}
		input.ProviderConfig, err = json.Marshal(config)
		if err != nil {
			return input, err
		}
	} else if len(config) != 0 {
		return input, fmt.Errorf("%s provider_config must be an empty object", input.Provider)
	}
	return input, nil
}
