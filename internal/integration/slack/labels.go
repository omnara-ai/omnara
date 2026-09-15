package slack

import (
	"context"
	"net/url"
	"strings"
)

type userInfoResponse struct {
	OK    bool          `json:"ok"`
	Error string        `json:"error"`
	User  slackUserInfo `json:"user"`
}

type slackUserInfo struct {
	Name     string       `json:"name"`
	RealName string       `json:"real_name"`
	Profile  *UserProfile `json:"profile"`
}

func LookupUserDisplayName(
	ctx context.Context,
	config OAuthConfig,
	token, userID string,
) (string, APIResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", APIResult{}, nil
	}
	var out userInfoResponse
	result, err := callFormAt(
		ctx,
		config.HTTPClient,
		config.APIURL,
		token,
		"users.info",
		url.Values{"user": {userID}},
		&out,
	)
	if err != nil {
		return "", APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return "", result, nil
	}
	if !out.OK {
		return "", ErrorResult(out.Error), nil
	}
	return userInfoDisplayName(out.User), APIResult{}, nil
}

func userInfoDisplayName(user slackUserInfo) string {
	if value := profileDisplayName(user.Profile); value != "" {
		return value
	}
	for _, value := range []string{user.RealName, user.Name} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func profileDisplayName(profile *UserProfile) string {
	if profile == nil {
		return ""
	}
	for _, value := range []string{profile.DisplayName, profile.RealName, profile.Name} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
