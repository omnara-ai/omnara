package slack

import (
	"context"
	"net/url"
)

const InboundReaction = "eyes"

type reactionResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func AddReaction(ctx context.Context, config OAuthConfig, token, channel, timestamp, name string) (APIResult, error) {
	values := url.Values{
		"channel":   {channel},
		"timestamp": {timestamp},
		"name":      {name},
	}
	var out reactionResponse
	result, err := callFormAt(ctx, config.HTTPClient, config.APIURL, token, "reactions.add", values, &out)
	if err != nil {
		return APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return result, nil
	}
	if !out.OK {
		if out.Error == "already_reacted" {
			return APIResult{}, nil
		}
		return ErrorResult(out.Error), nil
	}
	return APIResult{}, nil
}
