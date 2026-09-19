package slack

import (
	"context"
	"encoding/json"
	"errors"
)

// PostProfileChoice makes one send attempt using the configured Slack endpoint.
// The caller supplies checked credentials and owns receipt persistence and
// bounded retries. Retrying an uncertain result may duplicate the menu.
func PostProfileChoice(
	ctx context.Context,
	config OAuthConfig,
	target MessageTarget,
	choiceID string,
	options []ProfileChoiceOption,
	expiryText string,
) (string, APIResult, error) {
	text, blocks, err := ProfileChoicePrompt(choiceID, options, expiryText)
	if err != nil {
		return "", APIResult{}, err
	}
	payload, err := PromptPayload(target, text, blocks)
	if err != nil {
		return "", APIResult{}, err
	}
	return profileChoiceMessage(ctx, config, target, "chat.postMessage", payload)
}

// UpdateProfileChoice replaces a confirmed menu with caller-supplied text and
// clears its controls. It never searches history or posts another message.
func UpdateProfileChoice(
	ctx context.Context, config OAuthConfig, target MessageTarget, messageID, text string,
) (APIResult, error) {
	if messageID == "" {
		return APIResult{}, errors.New("slack profile choice update requires a message ID")
	}
	payload, err := json.Marshal(map[string]any{
		"channel": target.Channel, "ts": messageID, "text": PromptLabel(text), "blocks": []any{},
	})
	if err != nil {
		return APIResult{}, err
	}
	confirmedID, result, err := profileChoiceMessage(ctx, config, target, "chat.update", payload)
	if err == nil && confirmedID != "" && confirmedID != messageID {
		return APIResult{DeliveryUnknown: true}, nil
	}
	return result, err
}

func profileChoiceMessage(
	ctx context.Context, config OAuthConfig, target MessageTarget, method string, payload json.RawMessage,
) (string, APIResult, error) {
	var out postMessageResponse
	result, err := callJSONAt(ctx, config.HTTPClient, config.APIURL, target.BotToken, method, payload, &out)
	if err != nil {
		return "", APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return "", result, nil
	}
	if !out.OK {
		return "", ErrorResult(out.Error), nil
	}
	if out.TS == "" || out.Channel != target.Channel {
		return "", APIResult{DeliveryUnknown: true}, nil
	}
	return out.TS, APIResult{}, nil
}
