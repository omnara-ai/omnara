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
const ScheduledLaunchKey = "scheduled"

type LauncherTrigger string

const (
	TriggerMention           LauncherTrigger = "mention"
	TriggerPullRequestOpened LauncherTrigger = "pull_request_opened"
	TriggerBoth              LauncherTrigger = "both"
)

type ChatLauncherSettings struct {
	Profiles []string `json:"profiles"`
}

type GitHubLauncherSettings struct {
	Profile      string          `json:"profile"`
	Trigger      LauncherTrigger `json:"trigger"`
	RepositoryID string          `json:"repository_id,omitempty"`
}

type GitHubSettings struct {
	Launcher *GitHubLauncherSettings `json:"launcher,omitempty"`
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
	var settings GitHubSettings
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

var chatSettings = &SettingsDefinition{
	InputSchema:      json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"launcher":{"type":"object","additionalProperties":false,"required":["profiles"],"properties":{"profiles":{"type":"array","minItems":1,"maxItems":16,"uniqueItems":true,"items":{"type":"string","pattern":"^aprf_[a-z2-7]{26}$"},"title":"Profiles","description":"One profile launches immediately; several offer a choice of exactly one."}}}}}`),
	Description:      "Launch from a mention using one profile or a menu of profiles.",
	ValidateSettings: func(raw json.RawMessage) error { _, err := ChatLaunchProfiles(raw); return err },
}

func newChatLauncher(provider Provider) *LauncherDefinition {
	return &LauncherDefinition{
		MayLaunchWithoutSelection: func(raw json.RawMessage, _ Event) bool {
			profiles, err := ChatLaunchProfiles(raw)
			return err == nil && len(profiles) == 1
		},
		Matches: func(raw json.RawMessage, event Event) bool {
			launcher, err := ReadChatLauncher(raw)
			return err == nil && launcher != nil && event.Scope.Provider() == provider && matchesTrigger(event, TriggerMention)
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
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"launcher":{"type":"object","additionalProperties":false,"required":["profile","trigger"],"properties":{"profile":{"type":"string","pattern":"^aprf_[a-z2-7]{26}$","title":"Profile","x-omnara-control":"agent_profile"},"trigger":{"type":"string","enum":["mention","pull_request_opened","both"],"title":"Start agents"},"repository_id":{"type":"string","pattern":"^[1-9][0-9]*$","title":"Repository ID","description":"Optional: restrict launches to this repository."}}}}}`),
	Description: "Launch on PR openings, mentions, or both, from someone with repository write access.",
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

func matchesTrigger(event Event, trigger LauncherTrigger) bool {
	if event.Validate() != nil || (event.Scope.Discord != nil && event.Scope.Discord.GuildID == "") {
		return false
	}
	switch trigger {
	case TriggerMention:
		return event.Mentioned &&
			(event.Kind == EventMessage || event.Kind == EventDiscussionComment || event.Kind == EventReviewComment)
	case TriggerPullRequestOpened:
		return event.Scope.GitHub != nil && event.Kind == EventPullRequestOpened
	case TriggerBoth:
		return event.Scope.GitHub != nil &&
			(matchesTrigger(event, TriggerMention) || matchesTrigger(event, TriggerPullRequestOpened))
	}
	return false
}
