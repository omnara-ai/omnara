package integrationdefinition

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const MaxChatProfiles = 16

// ProfileLaunchKey identifies the one launch owned by a conversation, independently
// of the configured profile or its position in a menu.
const ProfileLaunchKey = "default"

type ChatLauncherSettings struct {
	Profiles  []string `json:"profiles"`
	ChannelID string   `json:"channel_id,omitempty"`
}

type GitHubLauncherSettings struct {
	Profile      string `json:"profile"`
	Trigger      string `json:"trigger"`
	RepositoryID string `json:"repository_id,omitempty"`
}

type GitHubSettings struct {
	SenderPolicy string                  `json:"sender_policy,omitempty"`
	Launcher     *GitHubLauncherSettings `json:"launcher,omitempty"`
}

func ReadChatLauncher(raw json.RawMessage) (*ChatLauncherSettings, error) {
	var settings struct {
		Launcher *ChatLauncherSettings `json:"launcher"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, err
	}
	return settings.Launcher, nil
}

func ReadGitHubSettings(raw json.RawMessage) (GitHubSettings, error) {
	settings := GitHubSettings{SenderPolicy: "writers"}
	err := json.Unmarshal(raw, &settings)
	return settings, err
}

func ChatLaunchProfiles(raw json.RawMessage) ([]uuid.UUID, error) {
	launcher, err := ReadChatLauncher(raw)
	if err != nil || launcher == nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(launcher.Profiles))
	for _, id := range launcher.Profiles {
		decoded, err := publicid.Decode(publicid.KindAgentProfile, id)
		if err != nil {
			return nil, fmt.Errorf("invalid launcher profile: %w", err)
		}
		ids = append(ids, decoded)
	}
	return ids, nil
}

func newChatSettings(provider string) *SettingsDefinition {
	channel := ""
	if provider == ProviderSlack {
		channel = `,"channel_id":{"type":"string","pattern":"^[CG][A-Z0-9]+$","title":"Channel ID",
 "description":"Optional: restrict mentions to this channel."}`
	}
	return &SettingsDefinition{
		InputSchema:      json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"launcher":{"type":"object","additionalProperties":false,"required":["profiles"],"properties":{"profiles":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"type":"string","pattern":"^aprf_[a-z2-7]{26}$"},"title":"Profiles","description":"One profile launches immediately; several offer a choice of exactly one."}` + channel + `}}}}`),
		Description:      "Launch from a mention using one profile or a menu of profiles.",
		ValidateSettings: func(raw json.RawMessage) error { _, err := ChatLaunchProfiles(raw); return err },
	}
}

func newChatLauncher(provider string) *LauncherDefinition {
	return &LauncherDefinition{
		Matches: func(raw json.RawMessage, event Event) bool {
			launcher, err := ReadChatLauncher(raw)
			if err != nil || launcher == nil || event.Scope.Provider() != provider || !matchesTrigger(event, "mention") {
				return false
			}
			return launcher.ChannelID == "" || (event.Scope.Slack != nil && launcher.ChannelID == event.Scope.Slack.ChannelID)
		},
		AuthorizeIntent: func(raw json.RawMessage, _ Event, intent LaunchIntent) error {
			profiles, err := ChatLaunchProfiles(raw)
			if err != nil {
				return err
			}
			if intent.LaunchKey != ProfileLaunchKey || !slices.Contains(profiles, intent.ProfileID) {
				return fmt.Errorf("profile is not configured for this launcher")
			}
			return nil
		},
	}
}

var githubSettings = &SettingsDefinition{
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"sender_policy":{"type":"string","enum":["writers","anyone"],"default":"writers","title":"Who can direct agents","description":"Writers allows users with repository write access. Anyone permits every human commenter. This is independent of automatic PR-open launches."},"launcher":{"type":"object","additionalProperties":false,"required":["profile","trigger"],"properties":{"profile":{"type":"string","pattern":"^aprf_[a-z2-7]{26}$","title":"Profile","x-omnara-control":"agent_profile"},"trigger":{"type":"string","enum":["mention","pull_request_opened"],"title":"Start agents"},"repository_id":{"type":"string","pattern":"^[1-9][0-9]*$","title":"Repository ID","description":"Optional: restrict launches to this repository."}}}}}`),
	Description: "Control comment senders and optionally launch one profile on a mention or when a pull request opens.",
	ValidateSettings: func(raw json.RawMessage) error {
		settings, err := ReadGitHubSettings(raw)
		if err != nil || settings.Launcher == nil {
			return err
		}
		if _, err := publicid.Decode(publicid.KindAgentProfile, settings.Launcher.Profile); err != nil {
			return fmt.Errorf("invalid launcher profile: %w", err)
		}
		if id := settings.Launcher.RepositoryID; id != "" {
			if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
				return fmt.Errorf("invalid repository_id")
			}
		}
		return nil
	},
}

var githubLauncher = &LauncherDefinition{
	Matches: func(raw json.RawMessage, event Event) bool {
		settings, err := ReadGitHubSettings(raw)
		if err != nil || settings.Launcher == nil || event.Scope.GitHub == nil {
			return false
		}
		launcher := settings.Launcher
		return matchesTrigger(event, launcher.Trigger) &&
			(launcher.RepositoryID == "" || launcher.RepositoryID == strconv.FormatInt(event.Scope.GitHub.RepositoryID, 10))
	},
	AuthorizeIntent: func(raw json.RawMessage, _ Event, intent LaunchIntent) error {
		settings, err := ReadGitHubSettings(raw)
		if err != nil {
			return err
		}
		if settings.Launcher != nil && intent.LaunchKey == ProfileLaunchKey {
			id, err := publicid.Decode(publicid.KindAgentProfile, settings.Launcher.Profile)
			if err == nil && id == intent.ProfileID {
				return nil
			}
		}
		return fmt.Errorf("profile is not configured for this launcher")
	},
}

func matchesTrigger(event Event, trigger string) bool {
	if event.Validate() != nil || (event.Scope.Discord != nil && event.Scope.Discord.GuildID == "") {
		return false
	}
	switch trigger {
	case "mention":
		return event.Mentioned &&
			(event.Kind == "message" || event.Kind == "discussion_comment" || event.Kind == "review_comment")
	case "pull_request_opened":
		return event.Scope.GitHub != nil && event.Kind == "pull_request_opened"
	}
	return false
}
