package slack

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

type MessagePage struct {
	Messages   []HistoryMessage `json:"messages"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// ReadMessages returns one provider page. The cursor is data, never a URL or a
// replacement conversation. Provider scopes/rate limits remain authoritative.
func ReadMessages(
	ctx context.Context,
	client *http.Client,
	target MessageTarget,
	cursor string,
	limit int,
) (MessagePage, APIResult, error) {
	if limit == 0 {
		limit = 15
	}
	if limit < 1 || limit > 100 || len(cursor) > 2048 {
		return MessagePage{}, APIResult{}, errors.New("slack read requires limit 1–100 and a bounded cursor")
	}
	values := url.Values{"channel": {target.Channel}, "limit": {strconv.Itoa(limit)}}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	method := "conversations.history"
	if target.ThreadTS != "" {
		method = "conversations.replies"
		values.Set("ts", target.ThreadTS)
	}
	var out historyResponse
	result, err := callFormAt(ctx, client, defaultAPIURL, target.BotToken, method, values, &out)
	if err != nil || result != (APIResult{}) {
		return MessagePage{}, result, err
	}
	if !out.OK {
		return MessagePage{}, ErrorResult(out.Error), nil
	}
	return MessagePage{Messages: out.Messages, NextCursor: out.ResponseMetadata.NextCursor}, APIResult{}, nil
}
