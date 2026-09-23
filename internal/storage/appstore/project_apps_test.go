package appstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/stretchr/testify/require"
)

func TestProjectAppLauncherTriggers(t *testing.T) {
	for _, test := range []struct {
		appType             appdefinition.Type
		scopeKind, scopeRef string
		triggers            []string
	}{
		{appdefinition.SlackThread, "workspace", "T123", []string{"mention"}},
		{appdefinition.DiscordThread, "", "", []string{"mention"}},
		{appdefinition.GitHubPR, "repository", "123", []string{"mention", "pull_request_opened"}},
	} {
		t.Run(string(test.appType), func(t *testing.T) {
			definition, _ := appdefinition.Lookup(test.appType)
			require.ElementsMatch(t, test.triggers, definition.LaunchTriggers)
			profileID := uuid.New()
			input := SaveProjectAppInput{
				OrgID: uuid.New(), ProjectID: uuid.New(), Name: "launcher", AppType: test.appType,
				Settings: ProjectAppSettings{Launcher: &AppLauncher{
					ScopeKind: test.scopeKind, ScopeRef: test.scopeRef,
					Slots: []AppLaunchSlot{{Key: "profile", AgentProfileID: &profileID}},
				}},
			}
			for _, trigger := range []string{"mention", "pull_request_opened", "", "arbitrary"} {
				input.Settings.Launcher.Trigger = trigger
				_, err := normalizeProjectApp(input)
				if definition.SupportsLaunchTrigger(trigger) {
					require.NoError(t, err, trigger)
				} else {
					require.EqualError(t, err, "unsupported app launch trigger", trigger)
				}
			}
		})
	}
}
