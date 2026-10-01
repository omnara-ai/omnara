package integrationdefinition

import (
	"fmt"
	"strings"
)

func (d Definition) ActorIdentity(providerTenantID string) (namespace, sourceLabel string, err error) {
	switch d.Provider {
	case ProviderSlack:
		if strings.TrimSpace(providerTenantID) == "" {
			return "", "", fmt.Errorf("slack actor identity requires a workspace ID")
		}
		return "slack:" + providerTenantID, "Slack", nil
	case ProviderDiscord:
		return "discord", "Discord", nil
	case ProviderGitHub:
		return "github:github.com", "GitHub", nil
	default:
		return "", "", fmt.Errorf("unsupported actor identity provider %q", d.Provider)
	}
}
