package slack

// Frozen rendering oracle for the shared TS-generated action fixtures. Provider
// rendering and network sends are owned by the gateway; these pure helpers
// exist only to check wire compatibility with the core action consumer.
import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

type MessageTarget struct {
	Channel  string
	ThreadTS string
}

func Destination(kind, ref string) (channel, threadTS string, err error) {
	switch kind {
	case "dm":
		if ref == "" {
			return "", "", errors.New("slack dm target is missing channel")
		}
		return ref, "", nil
	case "thread":
		channel, threadTS, ok := strings.Cut(ref, ":")
		if !ok || channel == "" || threadTS == "" {
			return "", "", errors.New("slack thread target is malformed")
		}
		return channel, threadTS, nil
	default:
		return "", "", fmt.Errorf("unsupported slack target kind %q", kind)
	}
}

const (
	promptLabelLimit      = 3000
	promptInputLabelLimit = 2000
	promptOptionTextLimit = 75
)

func PromptPayload(target MessageTarget, text string, blocks []map[string]any) (json.RawMessage, error) {
	payload := map[string]any{
		"channel": target.Channel,
		"text":    PromptLabel(text),
		"blocks":  blocks,
	}
	if target.ThreadTS != "" {
		payload["thread_ts"] = target.ThreadTS
	}
	return json.Marshal(payload)
}

func InteractionFormPromptBlocks(
	value interactionform.Form,
	base PromptActionValue,
) (string, []map[string]any) {
	summary := interactionFormSummary(value)
	heading := interactionFormHeading(value)
	supported := interactionFormSupportedInMessage(value)
	if !supported {
		heading = summary
	}
	blocks := []map[string]any{
		sectionBlockWithID(heading, promptMarkerBlockID(base.InteractionID)),
	}
	if !supported {
		blocks = append(blocks, sectionBlock("Respond in Omnara to continue."))
		return summary, blocks
	}
	for index, question := range value.Questions {
		options := make([]map[string]any, 0, len(question.Options))
		for optionIndex, option := range question.Options {
			options = append(options, map[string]any{
				"text":  map[string]any{"type": "plain_text", "text": promptText(option.Label, promptOptionTextLimit)},
				"value": strconv.Itoa(optionIndex),
			})
		}
		elementType := "radio_buttons"
		if question.Multiple {
			elementType = "checkboxes"
		}
		blocks = append(blocks, map[string]any{
			"type":     "input",
			"block_id": questionBlockID(index),
			"element": map[string]any{
				"type":      elementType,
				"action_id": PromptAnswerAction,
				"options":   options,
			},
			"label": map[string]any{
				"type": "plain_text",
				"text": promptText(question.Prompt, promptInputLabelLimit),
			},
		})
	}
	blocks = append(blocks, map[string]any{
		"type": "actions",
		"elements": []map[string]any{
			primaryButton("Submit", PromptAction, base),
		},
	})
	return summary, blocks
}

func interactionFormSupportedInMessage(value interactionform.Form) bool {
	if len(value.Questions)+2 > 50 {
		return false
	}
	for _, question := range value.Questions {
		if len(question.Options) > 10 {
			return false
		}
	}
	return true
}

func interactionFormHeading(value interactionform.Form) string {
	parts := []string{value.Title}
	for _, item := range value.Context {
		parts = append(parts, item.Label+": "+item.Value)
	}
	return strings.Join(parts, "\n")
}

func interactionFormSummary(value interactionform.Form) string {
	parts := []string{interactionFormHeading(value)}
	for questionIndex, question := range value.Questions {
		parts = append(
			parts,
			strconv.Itoa(questionIndex+1)+". "+question.Prompt,
		)
		for optionIndex, option := range question.Options {
			parts = append(
				parts,
				"   "+strconv.Itoa(optionIndex+1)+". "+option.Label,
			)
		}
	}
	return strings.Join(parts, "\n")
}

func promptMarkerBlockID(interactionID string) string {
	return "omnara_interaction_" + interactionID
}

func button(text, actionID string, value PromptActionValue) map[string]any {
	body, err := json.Marshal(value)
	if err != nil {
		body = json.RawMessage(`{}`)
	}
	button := map[string]any{
		"type":      "button",
		"text":      map[string]any{"type": "plain_text", "text": text},
		"action_id": actionID,
		"value":     string(body),
	}
	return button
}

func primaryButton(text, actionID string, value PromptActionValue) map[string]any {
	out := button(text, actionID, value)
	out["style"] = "primary"
	return out
}

func PromptLabel(value string) string {
	return promptText(value, promptLabelLimit)
}

func promptText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-3]) + "..."
}

func sectionBlock(text string) map[string]any {
	return map[string]any{
		"type": "section",
		"text": map[string]any{"type": "plain_text", "text": PromptLabel(text)},
	}
}

func sectionBlockWithID(text, blockID string) map[string]any {
	block := sectionBlock(text)
	block["block_id"] = blockID
	return block
}
