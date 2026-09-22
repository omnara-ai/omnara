package slack

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/publicid"
)

const ProfileChoiceActionPrefix = "omnara_profile_choice:"

type ProfileChoiceOption struct {
	Key  string
	Name string
}

type ProfileChoiceSelection struct {
	ChoiceID string
	Key      string
}

func ProfileChoicePrompt(
	choiceID string, options []ProfileChoiceOption, expiryText string,
) (string, []map[string]any, error) {
	// Select-menu schema: https://docs.slack.dev/reference/block-kit/block-elements/select-menu-element/
	if _, err := publicid.Decode(publicid.KindAppProfileChoice, choiceID); err != nil {
		return "", nil, errors.New("invalid slack profile choice identity")
	}
	if len(options) < 2 || len(options) > 16 || !utf8.ValidString(expiryText) {
		return "", nil, errors.New("invalid slack profile choice menu")
	}
	seen := make(map[string]bool, len(options))
	items := make([]map[string]any, 0, len(options))
	for _, option := range options {
		if !validProfileChoiceKey(option.Key) || seen[option.Key] ||
			!utf8.ValidString(option.Name) || strings.TrimSpace(option.Name) == "" {
			return "", nil, errors.New("invalid slack profile choice option")
		}
		seen[option.Key] = true
		items = append(items, map[string]any{
			"text":  map[string]any{"type": "plain_text", "text": promptText(option.Name, 75)},
			"value": option.Key,
		})
	}
	text := "Choose a profile before continuing. The selected profile will launch with your original request. " +
		"Anyone with access to this conversation can choose."
	if expiry := strings.TrimSpace(expiryText); expiry != "" {
		text += "\n" + expiry
	}
	text = PromptLabel(text)
	return text, []map[string]any{
		sectionBlock(text),
		{
			"type": "actions",
			"elements": []map[string]any{{
				"type":        "static_select",
				"action_id":   ProfileChoiceActionPrefix + choiceID,
				"placeholder": map[string]any{"type": "plain_text", "text": "Choose a profile"},
				"options":     items,
			}},
		},
	}, nil
}

func ProfileChoiceFromActions(envelope ActionsEnvelope) (ProfileChoiceSelection, error) {
	if envelope.Type != "block_actions" || len(envelope.Actions) != 1 {
		return ProfileChoiceSelection{}, errors.New("invalid slack profile choice action")
	}
	action := envelope.Actions[0]
	choiceID, ok := strings.CutPrefix(action.ActionID, ProfileChoiceActionPrefix)
	if !ok || action.Type != "static_select" || action.Value != "" ||
		action.SelectedOption == nil || len(action.SelectedOptions) != 0 ||
		!validProfileChoiceKey(action.SelectedOption.Value) {
		return ProfileChoiceSelection{}, errors.New("invalid slack profile choice selection")
	}
	if _, err := publicid.Decode(publicid.KindAppProfileChoice, choiceID); err != nil {
		return ProfileChoiceSelection{}, errors.New("invalid slack profile choice identity")
	}
	return ProfileChoiceSelection{ChoiceID: choiceID, Key: action.SelectedOption.Value}, nil
}

func validProfileChoiceKey(key string) bool {
	return key != "" && len(key) <= 64 && utf8.ValidString(key) &&
		strings.TrimSpace(key) == key && !strings.ContainsRune(key, '\x00')
}
