package appdefinition

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
)

type ThreadScheduleSettings struct {
	AgentProfileID         string `json:"agent_profile_id"`
	ChannelID              string `json:"channel_id"`
	OpeningMessageTemplate string `json:"opening_message_template"`
	MessageTemplate        string `json:"message_template"`
}

type ScheduledThreadLaunch struct {
	ProfileID      uuid.UUID
	ChannelID      string
	OpeningMessage string
	Message        string
}

var (
	slackThreadSchedule   = newThreadScheduleDefinition(ProviderSlack)
	discordThreadSchedule = newThreadScheduleDefinition(ProviderDiscord)
)

func newThreadScheduleDefinition(provider string) *ScheduleDefinition {
	channelPattern := "^[CG][A-Z0-9]+$"
	channelDescription := "Use a Slack channel ID beginning with C or G. The bot must have access."
	if provider == ProviderDiscord {
		channelPattern = discordID.String()
		channelDescription = "Use a Discord text or announcement channel ID, not a thread. The bot must have access."
	}
	schema := json.RawMessage(fmt.Sprintf(`{
  "type": "object", "additionalProperties": false,
  "required": ["agent_profile_id", "channel_id", "opening_message_template", "message_template"],
  "x-omnara-field-order": ["agent_profile_id", "channel_id", "opening_message_template", "message_template"],
  "properties": {
    "agent_profile_id": {
      "type": "string", "minLength": 1, "title": "Agent profile",
      "description": "Each run launches a fresh agent from this profile.", "x-omnara-control": "agent_profile"
    },
    "channel_id": {"type": "string", "pattern": %q, "title": "Channel ID", "description": %q},
    "opening_message_template": {
      "type": "string", "minLength": 1, "maxLength": %d, "title": "Opening message", "x-omnara-control": "textarea",
      "description": "Posted before the agent starts. {{.trigger.name}} is the schedule name; {{.trigger.local_date}} is the date in its timezone.",
      "default": "{{.trigger.name}} — {{.trigger.local_date}}"
    },
    "message_template": {
      "type": "string", "minLength": 1, "maxLength": %d, "title": "Task instructions", "x-omnara-control": "textarea",
      "description": "Instructions for the agent. Its replies go in the new thread. The template source is limited to %d UTF-8 bytes. Variables can expand when rendered; runtime validation also enforces the rendered message's byte limit."
    }
  }
}`, channelPattern, channelDescription, maxThreadOpeningCodepoints,
		cronschedule.MaxMessageTemplateBytes, cronschedule.MaxMessageTemplateBytes))
	return &ScheduleDefinition{
		InputSchema: schema,
		Description: "Start a fresh agent in a new channel thread on each run.",
		ValidateSettings: func(raw json.RawMessage) error {
			_, err := prepareThreadSchedule(raw, cronschedule.MessageData("schedule", time.Time{}, nil))
			return err
		},
		ValidatePlan: func(plan SchedulePlan) error { return validateThreadSchedulePlan(provider, plan) },
	}
}

func PrepareThreadSchedule(
	appType Type, raw json.RawMessage, occurrence cronschedule.Occurrence,
) (ScheduledThreadLaunch, error) {
	if _, err := ValidateScheduleSettings(appType, raw); err != nil {
		return ScheduledThreadLaunch{}, err
	}
	data, err := occurrence.MessageData()
	if err != nil {
		return ScheduledThreadLaunch{}, err
	}
	return prepareThreadSchedule(raw, data)
}

func prepareThreadSchedule(raw json.RawMessage, data map[string]any) (ScheduledThreadLaunch, error) {
	settings, profileID, err := parseThreadScheduleSettings(raw)
	if err != nil {
		return ScheduledThreadLaunch{}, err
	}
	opening, err := renderThreadOpening(settings.OpeningMessageTemplate, data)
	if err != nil {
		return ScheduledThreadLaunch{}, err
	}
	message, err := cronschedule.RenderMessage(settings.MessageTemplate, data)
	if err != nil {
		return ScheduledThreadLaunch{}, err
	}
	return ScheduledThreadLaunch{
		ProfileID: profileID, ChannelID: settings.ChannelID, OpeningMessage: opening, Message: message,
	}, nil
}

func parseThreadScheduleSettings(raw json.RawMessage) (ThreadScheduleSettings, uuid.UUID, error) {
	var settings ThreadScheduleSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return settings, uuid.Nil, err
	}
	profileID, err := publicid.Decode(publicid.KindAgentProfile, settings.AgentProfileID)
	if err != nil {
		return settings, uuid.Nil, fmt.Errorf("invalid agent_profile_id: %w", err)
	}
	return settings, profileID, nil
}

func validateThreadSchedulePlan(provider string, plan SchedulePlan) error {
	data, err := plan.Occurrence.MessageData()
	if err != nil {
		return err
	}
	settings, profileID, err := parseThreadScheduleSettings(plan.Settings)
	if err != nil {
		return err
	}
	if len(plan.Slots) != 1 || plan.Slots[0].Key != "scheduled" {
		return fmt.Errorf("thread schedule requires one launch")
	}
	slot := plan.Slots[0]
	if slot.ProfileID != profileID {
		return fmt.Errorf("scheduled profile differs from app settings")
	}
	if err := slot.Scope.Validate(provider); err != nil {
		return err
	}
	matches := false
	switch provider {
	case ProviderSlack:
		matches = slot.Scope.Slack.ThreadTS != "" && slot.Scope.Slack.ChannelID == settings.ChannelID
	case ProviderDiscord:
		matches = slot.Scope.Discord.ThreadID != "" && slot.Scope.Discord.GuildID != "" &&
			slot.Scope.Discord.ChannelID == settings.ChannelID
	}
	if !matches {
		return fmt.Errorf("scheduled thread differs from its configured parent")
	}
	message, err := cronschedule.RenderMessage(settings.MessageTemplate, data)
	if err != nil {
		return err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": message}})
	if err != nil {
		return err
	}
	content, err = AppendInputContext(plan.AppName, slot.Scope, content)
	if err != nil {
		return err
	}
	if !jsoncanonical.Equal(content, slot.Content) {
		return fmt.Errorf("scheduled input differs from app settings")
	}
	return nil
}

const maxThreadOpeningCodepoints = 2000

func renderThreadOpening(source string, data map[string]any) (string, error) {
	if strings.TrimSpace(source) == "" || utf8.RuneCountInString(source) > maxThreadOpeningCodepoints {
		return "", fmt.Errorf("opening message template must contain 1 to %d codepoints", maxThreadOpeningCodepoints)
	}
	rendered, err := cronschedule.RenderMessage(source, data)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rendered) == "" || utf8.RuneCountInString(rendered) > maxThreadOpeningCodepoints {
		return "", fmt.Errorf("rendered opening message must contain 1 to %d codepoints", maxThreadOpeningCodepoints)
	}
	return rendered, nil
}
