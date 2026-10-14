package slack

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func ReconcileProfileChoice(
	ctx context.Context, config OAuthConfig, target MessageTarget, choiceID, botUserID string,
	createdAt time.Time,
) (string, APIResult, error) {
	_, err := publicid.Decode(publicid.KindIntegrationProfileChoice, choiceID)
	if err != nil || botUserID == "" || target.Channel == "" || createdAt.IsZero() {
		return "", APIResult{}, errors.New("slack profile choice readback requires choice, bot user, channel and creation time")
	}
	// Keep the bound fixed across retries and allow for DB/Slack clock skew before publication.
	oldest := createdAt.Add(-5 * time.Minute)
	matchesMenu := func(message HistoryMessage) bool {
		if message.User != botUserID || (message.Channel != "" && message.Channel != target.Channel) {
			return false
		}
		if target.ThreadTS != "" {
			if message.ThreadTS != target.ThreadTS {
				return false
			}
		} else if message.ThreadTS != "" && message.ThreadTS != message.TS {
			return false
		}
		matches := func(action actionButton) bool {
			return action.Type == "static_select" && action.ActionID == ProfileChoiceActionPrefix+choiceID
		}
		for _, block := range message.Blocks {
			if block.Element != nil && matches(*block.Element) {
				return true
			}
			for _, action := range block.Elements {
				if matches(action) {
					return true
				}
			}
		}
		return false
	}
	return reconcilePromptReceipt(ctx, config.HTTPClient, config.APIURL, target, oldest, matchesMenu)
}

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
