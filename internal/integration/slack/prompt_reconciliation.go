package slack

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func reconcilePromptReceipt(
	ctx context.Context, client *http.Client, apiURL string, target MessageTarget,
	oldest time.Time, matches func(HistoryMessage) bool,
) (string, APIResult, error) {
	values := url.Values{
		"channel": {target.Channel}, "inclusive": {"true"}, "limit": {strconv.Itoa(readbackPageLimit)},
	}
	if !oldest.IsZero() {
		values.Set("oldest", slackTimestamp(oldest))
	}
	method := "conversations.history"
	if target.ThreadTS != "" {
		method = "conversations.replies"
		values.Set("ts", target.ThreadTS)
	}
	for range readbackMaxPages {
		var out struct {
			historyResponse
			HasMore bool `json:"has_more"`
		}
		result, err := callFormAt(ctx, client, apiURL, target.BotToken, method, values, &out)
		if err != nil || result != (APIResult{}) {
			return "", result, err
		}
		if !out.OK {
			return "", ErrorResult(out.Error), nil
		}
		if out.Messages == nil {
			return "", APIResult{DeliveryUnknown: true, Message: "integration prompt readback omitted messages"}, nil
		}
		for _, message := range out.Messages {
			if matches(message) {
				if message.TS == "" {
					return "", APIResult{DeliveryUnknown: true, Message: "integration prompt readback omitted message timestamp"}, nil
				}
				return message.TS, APIResult{}, nil
			}
		}
		nextCursor := strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if nextCursor == "" {
			if out.HasMore {
				return "", APIResult{DeliveryUnknown: true, Message: "integration prompt readback omitted continuation cursor"}, nil
			}
			return "", APIResult{}, nil
		}
		if nextCursor == values.Get("cursor") {
			return "", APIResult{DeliveryUnknown: true, Message: "integration prompt readback repeated its cursor"}, nil
		}
		values.Set("cursor", nextCursor)
	}
	return "", APIResult{DeliveryUnknown: true, Message: "integration prompt readback exceeded its page limit"}, nil
}
