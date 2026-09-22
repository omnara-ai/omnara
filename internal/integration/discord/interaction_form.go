package discord

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

func formText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func formCustomID(id, action string) string {
	value, _ := EncodeCustomID(CustomID{InteractionID: id, Action: action})
	return value
}

// InteractionFormPrompt uses direct buttons for a single choice. Larger forms
// use a modal with numbered choices, supporting multiple choices and optional
// text without persisting partial answers or provider interaction tokens.
func InteractionFormPrompt(form interactionform.Form, id string) (string, []ActionRow) {
	parts := []string{form.Title}
	for _, item := range form.Context {
		parts = append(parts, item.Label+": "+item.Value)
	}
	for i, question := range form.Questions {
		parts = append(parts, fmt.Sprintf("%d. %s", i+1, question.Prompt))
		for j, option := range question.Options {
			label := fmt.Sprintf("  %d. %s", j+1, option.Label)
			if option.AllowsText {
				label += " (optional text)"
			}
			parts = append(parts, label)
		}
	}
	text := strings.Join(parts, "\n")
	if len(form.Questions) > 5 || len([]rune(text)) > 1800 {
		return formText(text, 1900) + "\nRespond in Omnara to continue.", nil
	}
	if len(form.Questions) == 1 && !form.Questions[0].Multiple && len(form.Questions[0].Options) <= 5 {
		row := ActionRow{Type: 1}
		for i, option := range form.Questions[0].Options {
			action := "c" + strconv.Itoa(i)
			if option.AllowsText {
				action = "t" + strconv.Itoa(i)
			}
			row.Components = append(row.Components, Component{Type: 2, Style: 1,
				Label: formText(option.Label, 80), CustomID: formCustomID(id, action)})
		}
		return text, []ActionRow{row}
	}
	button := Component{Type: 2, Style: 1, Label: "Answer", CustomID: formCustomID(id, "form")}
	return text + "\nIn the form, enter choice numbers separated by commas; add ': text' for a text option.",
		[]ActionRow{{Type: 1, Components: []Component{button}}}
}

// ResolveInteractionForm returns either a modal to open or a complete normalized
// answer. Its caller validates the signed surface and current captured authority.
func ResolveInteractionForm(
	form interactionform.Form, input Interaction,
) (InteractionResponse, *interactionform.Resolution, error) {
	id, err := DecodeCustomID(input.Data.CustomID)
	if err != nil {
		return InteractionResponse{}, nil, err
	}
	invalid := func() (InteractionResponse, *interactionform.Resolution, error) {
		return InteractionResponse{}, nil, errors.New("invalid interaction answer")
	}
	var resolution interactionform.Resolution
	textOption := -1
	if strings.HasPrefix(id.Action, "c") || strings.HasPrefix(id.Action, "t") {
		index, err := strconv.Atoi(id.Action[1:])
		if err != nil || len(form.Questions) != 1 || form.Questions[0].Multiple ||
			index < 0 || index >= len(form.Questions[0].Options) {
			return invalid()
		}
		option := form.Questions[0].Options[index]
		if id.Action[0] == 't' {
			if !option.AllowsText {
				return invalid()
			}
			textOption = index
		} else {
			if input.Type != 3 {
				return invalid()
			}
			resolution.Answers = []interactionform.Answer{{OptionIndices: []int{index}}}
		}
	}
	if input.Type == 3 && (id.Action == "form" || textOption >= 0) {
		if len(form.Questions) > 5 {
			return invalid()
		}
		modal := &InteractionResponseData{
			Title:    formText(form.Title, 45),
			CustomID: formCustomID(id.InteractionID, "submit"),
		}
		for i, question := range form.Questions {
			label := "Choice number(s): " + question.Prompt
			field := "q" + strconv.Itoa(i)
			if textOption >= 0 {
				label = question.Options[textOption].Label
				modal.CustomID = input.Data.CustomID
			}
			required := textOption < 0
			modal.Components = append(modal.Components, ActionRow{Type: 1, Components: []Component{{
				Type: 4, Style: 2, CustomID: field, Label: formText(label, 45), Required: &required, MaxLength: 4000,
			}}})
		}
		return InteractionResponse{Type: 9, Data: modal}, nil, nil
	}
	if input.Type == 5 && (id.Action == "submit" || textOption >= 0) {
		var rows []struct {
			Components []struct {
				CustomID string `json:"custom_id"`
				Value    string `json:"value"`
			} `json:"components"`
		}
		if json.Unmarshal(input.Data.Components, &rows) != nil || len(rows) != len(form.Questions) {
			return invalid()
		}
		for i, row := range rows {
			if len(row.Components) != 1 || row.Components[0].CustomID != "q"+strconv.Itoa(i) ||
				len([]rune(row.Components[0].Value)) > 4000 {
				return invalid()
			}
			answer := interactionform.Answer{}
			if textOption >= 0 {
				answer.OptionIndices, answer.Text = []int{textOption}, row.Components[0].Value
			} else {
				choices, text, _ := strings.Cut(row.Components[0].Value, ":")
				answer.Text = text
				for choice := range strings.SplitSeq(choices, ",") {
					index, err := strconv.Atoi(strings.TrimSpace(choice))
					if err != nil {
						return invalid()
					}
					answer.OptionIndices = append(answer.OptionIndices, index-1)
				}
			}
			resolution.Answers = append(resolution.Answers, answer)
		}
	}
	normalized, err := interactionform.NormalizeResolution(form, resolution)
	if err != nil {
		return invalid()
	}
	return Acknowledge(input), &normalized, nil
}
