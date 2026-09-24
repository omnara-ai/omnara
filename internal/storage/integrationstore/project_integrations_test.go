package integrationstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/stretchr/testify/require"
)

func TestProjectIntegrationLauncherTriggers(t *testing.T) {
	for _, test := range []struct {
		integrationType     integrationdefinition.Type
		scopeKind, scopeRef string
		triggers            []string
	}{
		{integrationdefinition.SlackThread, "workspace", "T123", []string{"mention"}},
		{integrationdefinition.DiscordThread, "", "", []string{"mention"}},
		{integrationdefinition.GitHubPR, "repository", "123", []string{"mention", "pull_request_opened"}},
	} {
		t.Run(string(test.integrationType), func(t *testing.T) {
			definition, _ := integrationdefinition.Lookup(test.integrationType)
			require.ElementsMatch(t, test.triggers, definition.LaunchTriggers)
			profileID := uuid.New()
			input := SaveProjectIntegrationInput{
				OrgID: uuid.New(), ProjectID: uuid.New(), Name: "launcher", IntegrationType: test.integrationType,
				Settings: ProjectIntegrationSettings{Launcher: &IntegrationLauncher{
					ScopeKind: test.scopeKind, ScopeRef: test.scopeRef,
					Slots: []IntegrationLaunchSlot{{Key: "profile", AgentProfileID: &profileID}},
				}},
			}
			for _, trigger := range []string{"mention", "pull_request_opened", "", "arbitrary"} {
				input.Settings.Launcher.Trigger = trigger
				_, err := normalizeProjectIntegration(input)
				if definition.SupportsLaunchTrigger(trigger) {
					require.NoError(t, err, trigger)
				} else {
					require.EqualError(t, err, "unsupported integration launch trigger", trigger)
				}
			}
		})
	}
}
