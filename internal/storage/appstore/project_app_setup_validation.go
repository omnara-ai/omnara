package appstore

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

func ProjectAppCredentialKind(provider string) (secrets.Kind, error) {
	switch provider {
	case AppProviderSlack:
		return secrets.KindSlackAppCredentials, nil
	case AppProviderGitHub:
		return secrets.KindGitHubAppCredentials, nil
	case AppProviderDiscord:
		return secrets.KindGeneric, nil
	default:
		return "", fmt.Errorf("unsupported app provider %q", provider)
	}
}

func normalizeConfigureProjectAppInput(input ConfigureProjectAppInput) (
	ConfigureProjectAppInput,
	error,
) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil || input.InstalledByUserID == uuid.Nil {
		return input, errors.New("org, project, and installed-by user are required")
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.ProviderTenantID = strings.TrimSpace(input.ProviderTenantID)
	input.ProviderAccountRef = strings.TrimSpace(input.ProviderAccountRef)
	input.ProviderAgentDisplayName = strings.TrimSpace(input.ProviderAgentDisplayName)
	_, err := ProjectAppCredentialKind(input.Provider)
	if err != nil {
		return input, err
	}
	if input.CredentialSecretID == uuid.Nil {
		return input, fmt.Errorf("credential secret is required for %s apps", input.Provider)
	}
	if input.AppID == uuid.Nil || input.ExpectedSetupRevision < 1 || input.CredentialVersionID == uuid.Nil {
		return input, errors.New("app, expected setup revision, and verified credential version are required")
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
	if input.Provider == AppProviderGitHub {
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
	if input.Provider == AppProviderDiscord {
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
	var config map[string]json.RawMessage
	if err := json.Unmarshal(input.ProviderConfig, &config); err != nil {
		return input, err
	}
	if input.Provider == AppProviderDiscord {
		for key := range config {
			if key != "public_key" {
				return input, fmt.Errorf("unsupported discord provider_config field %q", key)
			}
		}
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

func validateLauncherProviderScope(launcher *AppLauncher, provider, tenantID, accountRef string) error {
	if launcher == nil {
		return nil
	}
	if provider == AppProviderSlack && launcher.ScopeKind == "workspace" && launcher.ScopeRef != tenantID {
		return errors.New("launcher workspace must match the app's verified Slack workspace")
	}
	if provider == AppProviderGitHub && launcher.ScopeKind == "installation" && launcher.ScopeRef != accountRef {
		return errors.New("launcher installation must match the app's verified GitHub installation")
	}
	return nil
}
