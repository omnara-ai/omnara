package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	EventBodyMaxBytes          = 1024 * 1024
	DefaultHistoryContextLimit = 15
)

var (
	userMentionPattern      = regexp.MustCompile(`<@([^>|]+)(?:\|([^>]+))?>`)
	channelMentionPattern   = regexp.MustCompile(`<#([^>|]+)(?:\|([^>]+))?>`)
	broadcastMentionPattern = regexp.MustCompile(`<!((?:here|channel|everyone))(?:\|[^>]*)?>`)
)

type Identity struct {
	AppID       string
	WorkspaceID string
	BotUserID   string
}

type EventsEnvelope struct {
	Type           string          `json:"type"`
	Challenge      string          `json:"challenge"`
	TeamID         string          `json:"team_id"`
	APIAppID       string          `json:"api_app_id"`
	EventID        string          `json:"event_id"`
	Authorizations []Authorization `json:"authorizations"`
	Event          Event           `json:"-"`
	RawEvent       json.RawMessage `json:"event"`
}

type Authorization struct {
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	IsBot  bool   `json:"is_bot"`
}

type Event struct {
	Type        string        `json:"type"`
	Subtype     string        `json:"subtype"`
	User        string        `json:"user"`
	BotID       string        `json:"bot_id"`
	Text        string        `json:"text"`
	Channel     string        `json:"channel"`
	ChannelType string        `json:"channel_type"`
	TS          string        `json:"ts"`
	ThreadTS    string        `json:"thread_ts"`
	Team        string        `json:"team"`
	SourceTeam  string        `json:"source_team"`
	UserTeam    string        `json:"user_team"`
	Tokens      RevokedTokens `json:"tokens"`
	Files       []File        `json:"files"`
}

type RevokedTokens struct {
	Bot []string `json:"bot"`
}

type UserProfile struct {
	DisplayName string `json:"display_name"`
	RealName    string `json:"real_name"`
	Name        string `json:"name"`
}

type historyResponse struct {
	OK               bool             `json:"ok"`
	Error            string           `json:"error"`
	Messages         []HistoryMessage `json:"messages"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

type HistoryMessage struct {
	User     string         `json:"user"`
	BotID    string         `json:"bot_id"`
	Text     string         `json:"text"`
	TS       string         `json:"ts"`
	ThreadTS string         `json:"thread_ts"`
	Blocks   []HistoryBlock `json:"blocks"`
}

type HistoryBlock struct {
	BlockID  string         `json:"block_id"`
	Element  *actionButton  `json:"element"`
	Elements []actionButton `json:"elements"`
}

type DisplayLabels struct {
	Users    map[string]string
	Channels map[string]string
}

func fetchHistory(
	ctx context.Context,
	config OAuthConfig,
	token, method string,
	values url.Values,
) ([]HistoryMessage, APIResult, error) {
	values.Set("limit", strconv.Itoa(DefaultHistoryContextLimit))
	var out historyResponse
	result, err := callFormAt(ctx, config.HTTPClient, config.APIURL, token, method, values, &out)
	if err != nil {
		return nil, APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return nil, result, nil
	}
	if !out.OK {
		return nil, ErrorResult(out.Error), nil
	}
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].TS < out.Messages[j].TS })
	if len(out.Messages) > DefaultHistoryContextLimit {
		out.Messages = out.Messages[len(out.Messages)-DefaultHistoryContextLimit:]
	}
	return out.Messages, APIResult{}, nil
}

func FetchRecentContextMessages(
	ctx context.Context,
	config OAuthConfig,
	token string,
	event Event,
	route InboundRoute,
	newlyMapped bool,
) ([]HistoryMessage, string, error) {
	if !ShouldFetchRecentContext(route, newlyMapped) {
		return nil, "skipped", nil
	}
	if strings.TrimSpace(token) == "" {
		return nil, "failed", nil
	}
	method := "conversations.history"
	values := url.Values{"channel": {event.Channel}, "latest": {event.TS}, "inclusive": {"false"}}
	if event.ThreadTS != "" && event.ThreadTS != event.TS {
		method = "conversations.replies"
		values.Set("ts", event.ThreadTS)
	}
	messages, result, err := fetchHistory(ctx, config, token, method, values)
	if err != nil {
		return nil, "failed", err
	}
	if result.RateLimited {
		return nil, "rate_limited", nil
	}
	if result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return nil, "failed", nil
	}
	return messages, "fetched", nil
}

func FormatRecentContext(messages []HistoryMessage, event Event, labels DisplayLabels) string {
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.TS == "" || message.TS == event.TS || strings.TrimSpace(message.Text) == "" {
			continue
		}
		lines = append(
			lines,
			labels.UserReference(message.User)+": "+
				labels.RenderText(strings.TrimSpace(message.Text)),
		)
	}
	if len(lines) == 0 {
		return ""
	}
	return "Recent Slack context:\n" + strings.Join(lines, "\n")
}

func ShouldFetchRecentContext(route InboundRoute, newlyMapped bool) bool {
	return route.ProviderRefKind == "thread" && newlyMapped
}

func InboundEventMetadata(
	provider string,
	envelope EventsEnvelope,
	route InboundRoute,
	historyStatus string,
	files []EventFileResult,
) (json.RawMessage, error) {
	metadata := map[string]any{
		"provider":       provider,
		"event_id":       envelope.EventID,
		"event_type":     envelope.Event.Type,
		"event_subtype":  envelope.Event.Subtype,
		"channel":        envelope.Event.Channel,
		"channel_type":   envelope.Event.ChannelType,
		"message_ts":     envelope.Event.TS,
		"provider_ref":   route.ProviderRef,
		"history_status": historyStatus,
	}
	if len(files) > 0 {
		metadata["files"] = files
	}
	return json.Marshal(metadata)
}

func DecodeEventsEnvelope(raw []byte) (EventsEnvelope, error) {
	var envelope EventsEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		return EventsEnvelope{}, err
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return EventsEnvelope{}, err
	}
	if len(envelope.RawEvent) == 0 {
		return envelope, nil
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(envelope.RawEvent, &probe); err != nil {
		return EventsEnvelope{}, err
	}
	if nameUpdateEventTypes[probe.Type] {
		envelope.Event = Event{Type: probe.Type}
		return envelope, nil
	}
	if err := json.Unmarshal(envelope.RawEvent, &envelope.Event); err != nil {
		return EventsEnvelope{}, err
	}
	return envelope, nil
}

var nameUpdateEventTypes = map[string]bool{
	"user_profile_changed": true,
	"channel_rename":       true,
	"group_rename":         true,
}

type NameUpdate struct {
	UserID         string
	ConversationID string
	DisplayName    string
}

func EventNameUpdate(envelope EventsEnvelope) (NameUpdate, bool) {
	switch envelope.Event.Type {
	case "user_profile_changed":
		var payload struct {
			User struct {
				ID       string       `json:"id"`
				Name     string       `json:"name"`
				RealName string       `json:"real_name"`
				Profile  *UserProfile `json:"profile"`
			} `json:"user"`
		}
		if err := json.Unmarshal(envelope.RawEvent, &payload); err != nil {
			return NameUpdate{}, false
		}
		name := userInfoDisplayName(slackUserInfo{
			Name:     payload.User.Name,
			RealName: payload.User.RealName,
			Profile:  payload.User.Profile,
		})
		if payload.User.ID == "" || name == "" {
			return NameUpdate{}, false
		}
		return NameUpdate{UserID: payload.User.ID, DisplayName: name}, true
	case "channel_rename", "group_rename":
		var payload struct {
			Channel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channel"`
		}
		if err := json.Unmarshal(envelope.RawEvent, &payload); err != nil {
			return NameUpdate{}, false
		}
		conversationID := strings.TrimSpace(payload.Channel.ID)
		name := strings.TrimSpace(payload.Channel.Name)
		if conversationID == "" || name == "" {
			return NameUpdate{}, false
		}
		return NameUpdate{ConversationID: conversationID, DisplayName: name}, true
	default:
		return NameUpdate{}, false
	}
}

func URLVerificationChallenge(envelope EventsEnvelope) (string, bool) {
	return envelope.Challenge, envelope.Type == "url_verification"
}

func EventCallbackEnvelope(envelope EventsEnvelope) bool {
	return envelope.Type == "event_callback"
}

type InboundRoute struct {
	ProviderRef     string
	ProviderRefKind string
	AppendOnly      bool
}

func ValidateRuntimeBotAuthorization(identity Identity, envelope EventsEnvelope) bool {
	if !ValidateEnvelopeIdentity(identity, envelope) {
		return false
	}
	if len(envelope.Authorizations) == 0 {
		return false
	}
	for _, authorization := range envelope.Authorizations {
		if authorization.TeamID == identity.WorkspaceID && authorization.UserID == identity.BotUserID &&
			authorization.IsBot {
			return true
		}
	}
	return false
}

func ValidateEnvelopeIdentity(identity Identity, envelope EventsEnvelope) bool {
	return envelope.APIAppID == identity.AppID && envelope.TeamID == identity.WorkspaceID
}

func RemoteUserEvent(workspaceID string, event Event) bool {
	for _, teamID := range []string{event.Team, event.SourceTeam, event.UserTeam} {
		if teamID != "" && teamID != workspaceID {
			return true
		}
	}
	return false
}

func BotOrSelfEvent(botUserID string, event Event) bool {
	return event.User == "" || event.User == botUserID || event.BotID != "" || event.Subtype == "bot_message"
}

func DisabledInstallEvent(botUserID string, event Event) bool {
	return event.Type == "app_uninstalled" || revokedInstallToken(botUserID, event)
}

func IgnoredLifecycleEvent(botUserID string, event Event) bool {
	return event.Type == "tokens_revoked" && !revokedInstallToken(botUserID, event)
}

func revokedInstallToken(botUserID string, event Event) bool {
	if event.Type != "tokens_revoked" {
		return false
	}
	for _, revokedBotUserID := range event.Tokens.Bot {
		if revokedBotUserID == botUserID {
			return true
		}
	}
	return false
}

func InboundRouting(botUserID string, event Event) (InboundRoute, bool) {
	if event.Channel == "" || event.TS == "" {
		return InboundRoute{}, false
	}
	switch event.Type {
	case "app_mention":
		threadTS := event.ThreadTS
		if threadTS == "" {
			threadTS = event.TS
		}
		return InboundRoute{ProviderRef: event.Channel + ":" + threadTS, ProviderRefKind: "thread"}, true
	case "message":
		if event.Subtype != "" && event.Subtype != "file_share" {
			return InboundRoute{}, false
		}
		switch event.ChannelType {
		case "im":
			return InboundRoute{ProviderRef: event.Channel, ProviderRefKind: "dm"}, true
		case "channel", "group", "mpim":
			isExistingThreadReply := event.ThreadTS != "" && event.ThreadTS != event.TS
			targetTS := event.TS
			if isExistingThreadReply {
				targetTS = event.ThreadTS
			}
			if event.Subtype == "file_share" && len(event.Files) > 0 {
				return InboundRoute{
					ProviderRef:     event.Channel + ":" + targetTS,
					ProviderRefKind: "thread",
					AppendOnly:      !textMentionsBot(event.Text, botUserID),
				}, true
			}
			if !isExistingThreadReply || textMentionsBot(event.Text, botUserID) {
				return InboundRoute{}, false
			}
			return InboundRoute{
				ProviderRef:     event.Channel + ":" + targetTS,
				ProviderRefKind: "thread",
				AppendOnly:      true,
			}, true
		default:
			return InboundRoute{}, false
		}
	default:
		return InboundRoute{}, false
	}
}

func InputIdempotencyKeyPair(envelope EventsEnvelope) (currentEventKey, siblingEventKey string) {
	team := envelope.TeamID
	if team == "" {
		team = envelope.Event.Team
	}
	identity := team + ":" + envelope.Event.Channel + ":" + envelope.Event.TS
	plainMessageKey := "slack:message:" + identity
	fileMessageKey := "slack:message-files:" + identity
	if len(envelope.Event.Files) > 0 {
		return fileMessageKey, plainMessageKey
	}
	return plainMessageKey, fileMessageKey
}

func textMentionsBot(text, botUserID string) bool {
	if botUserID == "" {
		return false
	}
	for _, userID := range userMentionIDs(text) {
		if userID == botUserID {
			return true
		}
	}
	return false
}

func ModelInputTextParts(
	event Event,
	route InboundRoute,
	newlyMapped bool,
	history string,
	labels DisplayLabels,
) (string, string) {
	location := labels.ChannelLocation(event.Channel, event.ChannelType)
	if threadTS := routeThreadTS(route); threadTS != "" {
		location += ", thread " + threadTS
	}
	prefix := labels.UserReference(event.User) + " in " + location + ":\n"
	message := labels.RenderText(strings.TrimSpace(event.Text))
	if event.ChannelType == "im" {
		return message, prefix
	}
	context := modelVisibleContext(event, route, newlyMapped)
	if history != "" {
		context += "\n\n" + history
	}
	return message, context + "\n\n" + prefix
}

func routeThreadTS(route InboundRoute) string {
	if route.ProviderRefKind != "thread" {
		return ""
	}
	_, threadTS, ok := strings.Cut(route.ProviderRef, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(threadTS)
}

func modelVisibleContext(event Event, route InboundRoute, newlyMapped bool) string {
	switch {
	case route.AppendOnly:
		return "This message was posted in a Slack thread that is already attached to this agent. It may be part of the ongoing conversation even if it does not directly mention the agent."
	case event.Type == "app_mention" && event.ThreadTS != "" && event.ThreadTS != event.TS:
		if newlyMapped {
			return "This message directly mentioned the agent inside an existing Slack thread."
		}
		return "This message directly mentioned the agent inside a Slack thread that is already attached to this agent."
	case event.Type == "app_mention":
		if newlyMapped {
			return "The agent was mentioned in a Slack channel, so this message starts a new Slack thread for communicating with the agent."
		}
		return "This message directly mentioned the agent in a Slack thread that is already attached to this agent."
	case route.ProviderRefKind == "thread":
		return "This message was routed to a Slack thread attached to this agent."
	default:
		return "This message was received from Slack."
	}
}

func (labels DisplayLabels) StoredDisplayName(userID string) string {
	return validUserDisplayLabel(label(labels.Users, userID), userID)
}

func (labels DisplayLabels) UserReference(userID string) string {
	return userReference(userID, label(labels.Users, userID))
}

func (labels DisplayLabels) ChannelLocation(channelID, channelType string) string {
	if channelType == "im" {
		return "Slack DM"
	}
	return labels.ChannelReference(channelID)
}

func (labels DisplayLabels) ChannelReference(channelID string) string {
	return channelReference(channelID, label(labels.Channels, channelID))
}

func (labels DisplayLabels) RenderText(text string) string {
	return labels.renderText(text, true)
}

func (labels DisplayLabels) RenderDisplayText(text string) string {
	return labels.renderText(text, false)
}

func (labels DisplayLabels) renderText(text string, includeIDs bool) string {
	text = userMentionPattern.ReplaceAllStringFunc(text, func(raw string) string {
		match := userMentionPattern.FindStringSubmatch(raw)
		if len(match) < 2 {
			return raw
		}
		value := labelOrFallback(label(labels.Users, match[1]), match, 2)
		if value := validUserDisplayLabel(value, match[1]); value != "" {
			if !includeIDs {
				return "@" + strings.TrimPrefix(value, "@")
			}
			return userReference(match[1], value)
		}
		return raw
	})
	text = channelMentionPattern.ReplaceAllStringFunc(text, func(raw string) string {
		match := channelMentionPattern.FindStringSubmatch(raw)
		if len(match) < 2 {
			return raw
		}
		if value := labelOrFallback(label(labels.Channels, match[1]), match, 2); value != "" {
			if !includeIDs {
				return "#" + strings.TrimPrefix(value, "#")
			}
			return channelReference(match[1], value)
		}
		return raw
	})
	return broadcastMentionPattern.ReplaceAllStringFunc(text, func(raw string) string {
		match := broadcastMentionPattern.FindStringSubmatch(raw)
		if len(match) < 2 {
			return raw
		}
		return "@" + match[1]
	})
}

func userReference(userID, displayName string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "Slack user"
	}
	if value := validUserDisplayLabel(displayName, userID); value != "" {
		return "<@" + userID + "> (" + strings.TrimPrefix(value, "@") + ")"
	}
	return "<@" + userID + ">"
}

func channelReference(channelID, displayName string) string {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "Slack channel"
	}
	displayName = strings.TrimSpace(displayName)
	if displayName != "" {
		return "<#" + channelID + "> (#" + strings.TrimPrefix(displayName, "#") + ")"
	}
	return "<#" + channelID + ">"
}

func label(values map[string]string, id string) string {
	if values == nil {
		return ""
	}
	return strings.TrimSpace(values[id])
}

func labelOrFallback(label string, match []string, fallbackIndex int) string {
	if strings.TrimSpace(label) != "" {
		return strings.TrimSpace(label)
	}
	if len(match) > fallbackIndex {
		return strings.TrimSpace(match[fallbackIndex])
	}
	return ""
}

func validUserDisplayLabel(value, userID string) string {
	value = strings.TrimSpace(value)
	if value == "" || (userID != "" && value == "<@"+userID+">") {
		return ""
	}
	return value
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

func ReferencedUserIDs(event Event, messages []HistoryMessage) []string {
	ids := userMentionIDs(event.Text)
	for _, message := range messages {
		ids = append(ids, message.User)
	}
	for _, message := range messages {
		ids = append(ids, userMentionIDs(message.Text)...)
	}
	return orderedReferencedIDs(event.User, ids...)
}

func referencedChannelIDs(event Event, messages []HistoryMessage) []string {
	ids := []string{}
	if event.ChannelType != "im" {
		ids = append(ids, event.Channel)
	}
	ids = append(ids, mentionIDs(channelMentionPattern, event.Text)...)
	for _, message := range messages {
		ids = append(ids, mentionIDs(channelMentionPattern, message.Text)...)
	}
	return orderedReferencedIDs("", ids...)
}

func userMentionIDs(text string) []string {
	return mentionIDs(userMentionPattern, text)
}

func mentionIDs(pattern *regexp.Regexp, text string) []string {
	ids := []string{}
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			ids = append(ids, match[1])
		}
	}
	return ids
}

func orderedReferencedIDs(first string, rest ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	addOrderedReferencedID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	addOrderedReferencedID(first)
	for _, id := range rest {
		addOrderedReferencedID(id)
	}
	return out
}
