package integrationdefinition

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func testLaunchSettings(kind Kind, trigger string) json.RawMessage {
	id, _ := publicid.Encode(publicid.KindAgentProfile, uuid.MustParse("11111111-1111-4111-8111-111111111111"))
	if kind == GitHubPR {
		return json.RawMessage(fmt.Sprintf(`{"launcher":{"profile":%q,"trigger":%q}}`, id, trigger))
	}
	return json.RawMessage(fmt.Sprintf(`{"launcher":{"profiles":[%q]}}`, id))
}

func TestIntegrationSettingsOwnedByDefinition(t *testing.T) {
	profile, _ := publicid.Encode(publicid.KindAgentProfile, uuid.New())
	for _, kind := range []Kind{SlackThread, DiscordThread, GitHubPR} {
		for _, settings := range []json.RawMessage{json.RawMessage(`{}`), testLaunchSettings(kind, "mention")} {
			_, err := ValidateSettings(kind, settings)
			require.NoError(t, err)
		}
	}
	for _, tc := range []struct {
		kind     Kind
		settings string
	}{
		{SlackThread, fmt.Sprintf(`{"launcher":{"profiles":[%q,%q]}}`, profile, profile)},
		{SlackThread, `{"launcher":{"profiles":[]}}`},
		{SlackThread, `{"launcher":{"profiles":["11111111-1111-4111-8111-111111111111"]}}`},
		{DiscordThread, fmt.Sprintf(`{"launcher":{"profiles":[%q],"channel_id":"123"}}`, profile)},
		{GitHubPR, fmt.Sprintf(`{"launcher":{"profiles":[%q]}}`, profile)},
		{GitHubPR, fmt.Sprintf(
			`{"launcher":{"profile":%q,"trigger":"mention","repository_id":"9223372036854775808"}}`, profile)},
	} {
		_, err := ValidateSettings(tc.kind, json.RawMessage(tc.settings))
		require.Error(t, err, tc.settings)
	}
}

func TestIntegrationLaunchAuthorizationAndScope(t *testing.T) {
	profiles, err := ChatLaunchProfiles(testLaunchSettings(SlackThread, "mention"))
	require.NoError(t, err)
	profile := profiles[0]
	event := Event{Scope: Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}, Kind: "message", Mentioned: true}
	definition, _ := Lookup(SlackThread)
	settings := testLaunchSettings(SlackThread, "mention")
	intent := LaunchIntent{LaunchKey: ProfileLaunchKey, ProfileID: profile}
	require.NoError(t, definition.AuthorizeLaunch(settings, event, intent))
	intent.LaunchKey = "profile_2"
	require.Error(t, definition.AuthorizeLaunch(settings, event, intent))
	intent.LaunchKey = ProfileLaunchKey
	intent.ProfileID = uuid.New()
	require.Error(t, definition.AuthorizeLaunch(settings, event, intent))
	intent.ProfileID = uuid.Nil
	require.Error(t, definition.AuthorizeLaunch(settings, event, intent))
	var raw map[string]map[string]any
	require.NoError(t, json.Unmarshal(settings, &raw))
	raw["launcher"]["channel_id"] = "C456"
	settings, err = json.Marshal(raw)
	require.NoError(t, err)
	require.False(t, definition.MatchesLaunch(settings, event))
	github, _ := Lookup(GitHubPR)
	pr := Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "pull_request_opened"}
	require.True(t, github.MatchesLaunch(testLaunchSettings(GitHubPR, "pull_request_opened"), pr))
	require.False(t, github.MatchesLaunch(testLaunchSettings(GitHubPR, "mention"), pr))
}

func TestDefinitionDoesNotRequireProfileSettings(t *testing.T) {
	definition := Definition{
		Settings: &SettingsDefinition{InputSchema: json.RawMessage(`{"type":"object","required":["enabled"],"additionalProperties":false,"properties":{"enabled":{"type":"boolean"}}}`)},
		Launcher: &LauncherDefinition{
			Matches: func(settings json.RawMessage, _ Event) bool { return string(settings) == `{"enabled":true}` },
			AuthorizeIntent: func(_ json.RawMessage, _ Event, intent LaunchIntent) error {
				if intent.LaunchKey != "own-policy" {
					return fmt.Errorf("wrong policy")
				}
				return nil
			}},
	}
	require.NoError(t, definition.Settings.Validate(json.RawMessage(`{"enabled":true}`)))
	require.NoError(t, definition.AuthorizeLaunch(json.RawMessage(`{"enabled":true}`), Event{}, LaunchIntent{LaunchKey: "own-policy"}))
	require.Error(t, definition.AuthorizeLaunch(json.RawMessage(`{"enabled":false}`), Event{}, LaunchIntent{LaunchKey: "own-policy"}))
	require.False(t, definition.MayLaunchWithoutSelection(json.RawMessage(`{"enabled":true}`), Event{}))
	definition.Launcher = nil
	require.False(t, definition.MatchesLaunch(json.RawMessage(`{"enabled":true}`), Event{}))
	require.False(t, definition.MayLaunchWithoutSelection(json.RawMessage(`{"enabled":true}`), Event{}))
}

func TestGitHubLauncherTriggers(t *testing.T) {
	definition, _ := Lookup(GitHubPR)
	for _, trigger := range []string{"mention", "pull_request_opened", "both"} {
		t.Run(trigger, func(t *testing.T) {
			settings := testLaunchSettings(GitHubPR, trigger)
			_, err := ValidateSettings(GitHubPR, settings)
			require.NoError(t, err)
			for _, kind := range []string{"discussion_comment", "review_comment", "pull_request_opened", "commit"} {
				for _, mentioned := range []bool{false, true} {
					event := Event{
						Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: kind, Mentioned: mentioned,
					}
					want := (kind == "pull_request_opened" && trigger != "mention") ||
						((kind == "discussion_comment" || kind == "review_comment") && mentioned && trigger != "pull_request_opened")
					require.Equal(t, want, definition.MatchesLaunch(settings, event), "%s mentioned=%t", kind, mentioned)
				}
			}
			var restricted GitHubSettings
			require.NoError(t, json.Unmarshal(settings, &restricted))
			restricted.Launcher.RepositoryID = "456"
			settings, err = json.Marshal(restricted)
			require.NoError(t, err)
			for _, kind := range []string{"discussion_comment", "pull_request_opened"} {
				require.False(t, definition.MatchesLaunch(settings, Event{
					Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: kind, Mentioned: true,
				}))
			}
		})
	}
}

func TestPendingLaunchPredicateOnlyProtectsSingleProfileChat(t *testing.T) {
	for _, kind := range []Kind{SlackThread, DiscordThread} {
		t.Run(string(kind), func(t *testing.T) {
			definition, _ := Lookup(kind)
			event := Event{Kind: "message", Mentioned: true,
				Scope: Scope{Slack: &SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}}
			if kind == DiscordThread {
				event.Scope = Scope{Discord: &DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"}}
			}
			single := testLaunchSettings(kind, "mention")
			require.True(t, definition.MayLaunchWithoutSelection(single, event))
			event.Mentioned = false
			require.False(t, definition.MayLaunchWithoutSelection(single, event))
			event.Mentioned = true
			require.False(t, definition.MayLaunchWithoutSelection(json.RawMessage(`{}`), event))
			profiles, err := ReadChatLauncher(single)
			require.NoError(t, err)
			other, err := publicid.Encode(publicid.KindAgentProfile, uuid.New())
			require.NoError(t, err)
			profiles.Profiles = append(profiles.Profiles, other)
			menu, err := json.Marshal(map[string]any{"launcher": profiles})
			require.NoError(t, err)
			require.True(t, definition.MatchesLaunch(menu, event))
			require.False(t, definition.MayLaunchWithoutSelection(menu, event))
		})
	}
	github, _ := Lookup(GitHubPR)
	pr := Event{Scope: Scope{GitHub: &GitHubScope{RepositoryID: 123, PullRequest: 7}}, Kind: "pull_request_opened"}
	require.True(t, github.MatchesLaunch(testLaunchSettings(GitHubPR, "pull_request_opened"), pr))
	require.Nil(t, github.Launcher.MayLaunchWithoutSelection)
	require.False(t, github.MayLaunchWithoutSelection(testLaunchSettings(GitHubPR, "pull_request_opened"), pr))
}
