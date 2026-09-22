package discord

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/publicid"
)

const ProfileChoiceCustomIDPrefix = "omnara_profile_choice:"

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
) (string, []ActionRow, error) {
	// String-select component schema: https://docs.discord.com/developers/components/reference#string-select
	customID := ProfileChoiceCustomIDPrefix + choiceID
	if _, err := decodeProfileChoiceCustomID(customID); err != nil {
		return "", nil, err
	}
	if len(options) < 2 || len(options) > 16 || !utf8.ValidString(expiryText) {
		return "", nil, errors.New("invalid discord profile choice menu")
	}
	selectMenu := Component{
		Type: 3, CustomID: customID, Placeholder: "Choose a profile", MinValues: 1, MaxValues: 1,
	}
	for _, option := range options {
		if !utf8.ValidString(option.Name) {
			return "", nil, errors.New("invalid discord profile choice name")
		}
		selectMenu.Options = append(selectMenu.Options, SelectOption{
			Label: formText(strings.TrimSpace(option.Name), 100), Value: option.Key,
		})
	}
	if err := validateProfileChoiceComponent(selectMenu); err != nil {
		return "", nil, err
	}
	text := "Choose a profile before continuing. The selected profile will launch with your original request. " +
		"Anyone with access to this conversation can choose."
	if expiry := strings.TrimSpace(expiryText); expiry != "" {
		text += "\n" + expiry
	}
	return formText(text, 2000), []ActionRow{{Type: 1, Components: []Component{selectMenu}}}, nil
}

func ProfileChoiceFromInteraction(input Interaction) (ProfileChoiceSelection, error) {
	choiceID, err := decodeProfileChoiceCustomID(input.Data.CustomID)
	if err != nil {
		return ProfileChoiceSelection{}, err
	}
	if input.Type != InteractionTypeMessageComponent || input.Data.ComponentType != 3 || len(input.Data.Values) != 1 ||
		!validProfileChoiceKey(input.Data.Values[0]) {
		return ProfileChoiceSelection{}, errors.New("invalid discord profile choice selection")
	}
	return ProfileChoiceSelection{ChoiceID: choiceID, Key: input.Data.Values[0]}, nil
}

func decodeProfileChoiceCustomID(raw string) (string, error) {
	choiceID, ok := strings.CutPrefix(raw, ProfileChoiceCustomIDPrefix)
	if !ok {
		return "", errors.New("invalid discord profile choice identity")
	}
	if _, err := publicid.Decode(publicid.KindAppProfileChoice, choiceID); err != nil {
		return "", errors.New("invalid discord profile choice identity")
	}
	return choiceID, nil
}

func validProfileChoiceKey(key string) bool {
	return key != "" && len(key) <= 64 && utf8.ValidString(key) &&
		strings.TrimSpace(key) == key && !strings.ContainsRune(key, '\x00')
}

func validateProfileChoiceComponent(component Component) error {
	if _, err := decodeProfileChoiceCustomID(component.CustomID); err != nil {
		return err
	}
	if component.Type != 3 || component.Style != 0 || component.Label != "" ||
		component.Required != nil || component.MinLength != 0 || component.MaxLength != 0 ||
		component.MinValues != 1 || component.MaxValues != 1 ||
		!utf8.ValidString(component.Placeholder) || utf8.RuneCountInString(component.Placeholder) > 150 ||
		len(component.Options) < 2 || len(component.Options) > 16 {
		return errors.New("invalid discord profile choice menu")
	}
	seen := make(map[string]bool, len(component.Options))
	for _, option := range component.Options {
		if !utf8.ValidString(option.Label) || strings.TrimSpace(option.Label) == "" ||
			utf8.RuneCountInString(option.Label) > 100 || !validProfileChoiceKey(option.Value) || seen[option.Value] {
			return errors.New("invalid discord profile choice option")
		}
		seen[option.Value] = true
	}
	return nil
}
