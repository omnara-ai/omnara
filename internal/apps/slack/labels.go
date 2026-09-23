package slack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

const (
	defaultLiveUserLookupLimit    = 8
	defaultLiveChannelLookupLimit = 2
)

type DisplayLabelInput struct {
	Event                  Event
	HistoryMessages        []HistoryMessage
	BotUserID              string
	BotDisplayName         string
	ChannelDisplayName     string
	LiveUserLookupLimit    int
	LiveChannelLookupLimit int
	StoredUserDisplayNames map[string]string
}

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

type conversationInfoResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel struct {
		Name string `json:"name"`
	} `json:"channel"`
}

func ResolveDisplayLabels(
	ctx context.Context,
	config OAuthConfig,
	token string,
	input DisplayLabelInput,
) (DisplayLabels, map[string]string, error) {
	labels := DisplayLabels{
		Users:    map[string]string{},
		Channels: map[string]string{},
	}
	if channelName := strings.TrimSpace(input.ChannelDisplayName); channelName != "" &&
		input.Event.Channel != "" {
		labels.Channels[input.Event.Channel] = channelName
	}
	if botName := strings.TrimSpace(input.BotDisplayName); botName != "" && input.BotUserID != "" {
		labels.Users[input.BotUserID] = botName
	}
	userIDs := ReferencedUserIDs(input.Event, input.HistoryMessages)
	for _, userID := range limitUnlabeledIDs(userIDs, labels.Users, len(userIDs)) {
		if name := strings.TrimSpace(input.StoredUserDisplayNames[userID]); name != "" {
			labels.Users[userID] = name
		}
	}
	users, channels, err := liveDisplayNames(
		ctx,
		config,
		token,
		limitUnlabeledIDs(
			userIDs,
			labels.Users,
			lookupLimit(input.LiveUserLookupLimit, defaultLiveUserLookupLimit),
		),
		limitUnlabeledIDs(
			referencedChannelIDs(input.Event, input.HistoryMessages),
			labels.Channels,
			lookupLimit(input.LiveChannelLookupLimit, defaultLiveChannelLookupLimit),
		),
	)
	for userID, name := range users {
		labels.Users[userID] = name
	}
	for channelID, name := range channels {
		labels.Channels[channelID] = name
	}
	return labels, channels, err
}

func liveDisplayNames(
	ctx context.Context,
	config OAuthConfig,
	token string,
	userIDs, channelIDs []string,
) (map[string]string, map[string]string, error) {
	type liveLookup struct {
		id      string
		channel bool
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil, nil
	}
	lookups := make([]liveLookup, 0, len(userIDs)+len(channelIDs))
	for _, id := range userIDs {
		lookups = append(lookups, liveLookup{id: id})
	}
	for _, id := range channelIDs {
		lookups = append(lookups, liveLookup{id: id, channel: true})
	}
	if len(lookups) == 0 {
		return nil, nil, nil
	}
	names := make([]string, len(lookups))
	lookupErrors := make([]error, len(lookups))
	var wg sync.WaitGroup
	for i, item := range lookups {
		wg.Add(1)
		go func(i int, item liveLookup) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					lookupErrors[i] = fmt.Errorf(
						"lookup slack display name for %q panicked: %v",
						item.id,
						recovered,
					)
				}
			}()
			var name string
			var result APIResult
			var err error
			if item.channel {
				name, result, err = LookupConversationName(ctx, config, token, item.id)
			} else {
				name, result, err = LookupUserDisplayName(ctx, config, token, item.id)
			}
			if err != nil {
				lookupErrors[i] = fmt.Errorf("lookup slack display name for %q: %w", item.id, err)
				return
			}
			if result != (APIResult{}) {
				lookupErrors[i] = apiResultError(
					fmt.Sprintf("lookup slack display name for %q", item.id),
					result,
				)
				return
			}
			names[i] = strings.TrimSpace(name)
		}(i, item)
	}
	wg.Wait()
	users := map[string]string{}
	channels := map[string]string{}
	for i, item := range lookups {
		if names[i] == "" {
			continue
		}
		if item.channel {
			channels[item.id] = names[i]
		} else {
			users[item.id] = names[i]
		}
	}
	return users, channels, errors.Join(lookupErrors...)
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

func LookupConversationName(
	ctx context.Context,
	config OAuthConfig,
	token, channelID string,
) (string, APIResult, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", APIResult{}, nil
	}
	var out conversationInfoResponse
	result, err := callFormAt(
		ctx,
		config.HTTPClient,
		config.APIURL,
		token,
		"conversations.info",
		url.Values{"channel": {channelID}},
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
	return strings.TrimSpace(out.Channel.Name), APIResult{}, nil
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

func lookupLimit(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func limitUnlabeledIDs(ids []string, labels map[string]string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	out := make([]string, 0, min(len(ids), limit))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(labels[id]) != "" {
			continue
		}
		out = append(out, id)
		if len(out) == limit {
			break
		}
	}
	return out
}
