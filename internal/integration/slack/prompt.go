package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

const (
	PromptType         = "omnara_interaction"
	PromptAction       = "omnara_interaction"
	PromptAnswerAction = "omnara_answer"
)

type PromptActionValue struct {
	Type                string `json:"type"`
	InteractionID       string `json:"interaction_id"`
	AgentID             string `json:"agent_id"`
	IntegrationTargetID string `json:"integration_target_id"`
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

func PostPrompt(
	ctx context.Context,
	client *http.Client,
	target MessageTarget,
	payload json.RawMessage,
) (APIResult, error) {
	var out postMessageResponse
	result, err := callJSON(ctx, client, target.BotToken, "chat.postMessage", payload, &out)
	if err != nil {
		return APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return result, nil
	}
	if !out.OK {
		return ErrorResult(out.Error), nil
	}
	return APIResult{}, nil
}

func ReconcilePrompt(
	ctx context.Context,
	client *http.Client,
	target MessageTarget,
	interactionID string,
) (bool, APIResult, error) {
	values := url.Values{
		"channel":   {target.Channel},
		"inclusive": {"true"},
		"limit":     {strconv.Itoa(readbackPageLimit)},
	}
	method := "conversations.history"
	if target.ThreadTS != "" {
		method = "conversations.replies"
		values.Set("ts", target.ThreadTS)
	}
	for range readbackMaxPages {
		var out historyResponse
		result, err := callFormAt(
			ctx,
			client,
			defaultAPIURL,
			target.BotToken,
			method,
			values,
			&out,
		)
		if err != nil || result.RateLimited || result.TransientFailure || result.PermanentFailure ||
			result.DeliveryUnknown {
			return false, result, err
		}
		if !out.OK {
			return false, ErrorResult(out.Error), nil
		}
		for _, message := range out.Messages {
			if interactionPromptMessage(message, []string{interactionID}) {
				return true, APIResult{}, nil
			}
		}
		nextCursor := strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if nextCursor == "" {
			return false, APIResult{}, nil
		}
		values.Set("cursor", nextCursor)
	}
	return false, APIResult{
		DeliveryUnknown: true,
		Message:         "integration prompt readback exceeded its page limit",
	}, nil
}

func DismissInteractionPrompts(
	ctx context.Context,
	config OAuthConfig,
	token string,
	event Event,
	interactionIDs []string,
) (APIResult, error) {
	method := "conversations.history"
	values := url.Values{
		"channel":   {event.Channel},
		"latest":    {event.TS},
		"inclusive": {"false"},
	}
	if event.ThreadTS != "" && event.ThreadTS != event.TS {
		method = "conversations.replies"
		values.Set("ts", event.ThreadTS)
	}
	messages, result, err := fetchHistory(ctx, config, token, method, values)
	if err != nil || result.RateLimited || result.TransientFailure || result.PermanentFailure ||
		result.DeliveryUnknown {
		return result, err
	}
	var missingMessageFailure APIResult
	for _, message := range messages {
		if message.TS == "" || !interactionPromptMessage(message, interactionIDs) {
			continue
		}
		label := PromptLabel(message.Text)
		dismissed := "Dismissed because a newer message was sent."
		body, err := json.Marshal(map[string]any{
			"channel": event.Channel,
			"ts":      message.TS,
			"as_user": true,
			"text":    PromptLabel(label + "\n" + dismissed),
			"blocks": []map[string]any{
				sectionBlock(label),
				{
					"type": "context",
					"elements": []map[string]any{
						{"type": "plain_text", "text": dismissed},
					},
				},
			},
		})
		if err != nil {
			return APIResult{}, err
		}
		var out postMessageResponse
		result, err = callJSONAt(
			ctx,
			config.HTTPClient,
			config.APIURL,
			token,
			"chat.update",
			body,
			&out,
		)
		if err != nil || result.RateLimited || result.TransientFailure ||
			result.DeliveryUnknown {
			return result, err
		}
		if !out.OK && !result.PermanentFailure {
			result = ErrorResult(out.Error)
		}
		if result.RateLimited || result.TransientFailure || result.DeliveryUnknown {
			return result, nil
		}
		if !result.PermanentFailure {
			continue
		}
		if result.ProviderCode != "message_not_found" {
			return result, nil
		}
		if !missingMessageFailure.PermanentFailure {
			missingMessageFailure = result
		}
	}
	return missingMessageFailure, nil
}

func interactionPromptMessage(message HistoryMessage, interactionIDs []string) bool {
	for _, block := range message.Blocks {
		for _, interactionID := range interactionIDs {
			if block.BlockID == promptMarkerBlockID(interactionID) {
				return true
			}
		}
		if block.Element != nil && interactionPromptAction(*block.Element, interactionIDs) {
			return true
		}
		for _, element := range block.Elements {
			if interactionPromptAction(element, interactionIDs) {
				return true
			}
		}
	}
	return false
}

func interactionPromptAction(action actionButton, interactionIDs []string) bool {
	if !PromptActionID(action.ActionID) || action.Value == "" {
		return false
	}
	value, err := decodePromptActionValue(action.Value)
	return err == nil && slices.Contains(interactionIDs, value.InteractionID)
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

func questionBlockID(index int) string {
	return "omnara_question_" + strconv.Itoa(index)
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
