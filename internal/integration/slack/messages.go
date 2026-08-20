package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL        = "https://slack.com/api"
	defaultToolTimeout   = 15 * time.Second
	toolResponseMaxBytes = 1024 * 1024
	readbackPageLimit    = 100
	readbackMaxPages     = 8

	MessageMarkerEventType = "omnara_integration_message"
)

type MessageTarget struct {
	TargetRef string
	Channel   string
	ThreadTS  string
	BotToken  string
}

type APIResult struct {
	MessageID        string
	Code             string
	ProviderCode     string
	RateLimited      bool
	RetryAfter       time.Duration
	TransientFailure bool
	PermanentFailure bool
	DeliveryUnknown  bool
	Message          string
}

type postMessageResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

type messageMetadata struct {
	EventType    string         `json:"event_type"`
	EventPayload map[string]any `json:"event_payload"`
}

type readbackResponse struct {
	OK               bool              `json:"ok"`
	Error            string            `json:"error"`
	Messages         []readbackMessage `json:"messages"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

type readbackMessage struct {
	Channel  string           `json:"channel"`
	TS       string           `json:"ts"`
	Metadata *messageMetadata `json:"metadata"`
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

func ConversationURI(workspaceID, providerRef string) string {
	workspaceID = strings.TrimSpace(workspaceID)
	conversationID, _, _ := strings.Cut(strings.TrimSpace(providerRef), ":")
	if workspaceID == "" || conversationID == "" {
		return ""
	}
	values := url.Values{}
	values.Set("channel", conversationID)
	values.Set("team", workspaceID)
	return "https://slack.com/app_redirect?" + values.Encode()
}

func PostMessage(
	ctx context.Context,
	client *http.Client,
	target MessageTarget,
	agentPublicID, providerCallID, text string,
) (APIResult, error) {
	payload := map[string]any{
		"channel": target.Channel,
		"text":    text,
		"metadata": messageMetadata{
			EventType: MessageMarkerEventType,
			EventPayload: map[string]any{
				"agent_id":         agentPublicID,
				"provider_call_id": providerCallID,
				"target_ref":       target.TargetRef,
			},
		},
	}
	if target.ThreadTS != "" {
		payload["thread_ts"] = target.ThreadTS
	}
	return postMessageAt(ctx, client, defaultAPIURL, target, payload)
}

func PostPlainMessage(
	ctx context.Context,
	config OAuthConfig,
	target MessageTarget,
	text string,
) (APIResult, error) {
	payload := map[string]any{
		"channel": target.Channel,
		"text":    text,
	}
	if target.ThreadTS != "" {
		payload["thread_ts"] = target.ThreadTS
	}
	return postMessageAt(ctx, config.HTTPClient, config.APIURL, target, payload)
}

func postMessageAt(
	ctx context.Context,
	client *http.Client,
	apiURL string,
	target MessageTarget,
	payload map[string]any,
) (APIResult, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return APIResult{}, err
	}
	var out postMessageResponse
	result, err := callJSONAt(ctx, client, apiURL, target.BotToken, "chat.postMessage", body, &out)
	if err != nil {
		return APIResult{}, err
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		return result, nil
	}
	if !out.OK {
		return ErrorResult(out.Error), nil
	}
	channel := out.Channel
	if channel == "" {
		channel = target.Channel
	}
	if out.TS == "" {
		return APIResult{DeliveryUnknown: true, Message: "Message delivery could not be confirmed by Slack."}, nil
	}
	return APIResult{MessageID: channel + ":" + out.TS}, nil
}

func ReconcileMessage(
	ctx context.Context,
	client *http.Client,
	target MessageTarget,
	agentPublicID, providerCallID string,
	since time.Time,
) (string, bool, APIResult, error) {
	values := url.Values{
		"channel":              {target.Channel},
		"include_all_metadata": {"true"},
		"inclusive":            {"true"},
		"limit":                {strconv.Itoa(readbackPageLimit)},
		"oldest":               {slackTimestamp(since.Add(-time.Second))},
	}
	method := "conversations.history"
	if target.ThreadTS != "" {
		method = "conversations.replies"
		values.Set("ts", target.ThreadTS)
	}
	for range readbackMaxPages {
		var out readbackResponse
		result, err := callFormAt(ctx, client, defaultAPIURL, target.BotToken, method, values, &out)
		if err != nil {
			return "", false, APIResult{}, err
		}
		if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
			return "", false, result, nil
		}
		if !out.OK {
			return "", false, ErrorResult(out.Error), nil
		}
		for _, message := range out.Messages {
			if messageHasMarker(message, agentPublicID, providerCallID, target.TargetRef) {
				channel := message.Channel
				if channel == "" {
					channel = target.Channel
				}
				if message.TS == "" {
					continue
				}
				return channel + ":" + message.TS, true, APIResult{}, nil
			}
		}
		nextCursor := strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if nextCursor == "" {
			return "", false, APIResult{}, nil
		}
		values.Set("cursor", nextCursor)
	}
	return "", false, APIResult{
		DeliveryUnknown: true,
		Message:         "integration message readback exceeded its page limit",
	}, nil
}

func callJSON(
	ctx context.Context,
	client *http.Client,
	token, method string,
	body json.RawMessage,
	out any,
) (APIResult, error) {
	return callJSONAt(ctx, client, defaultAPIURL, token, method, body, out)
}

func callJSONAt(
	ctx context.Context,
	client *http.Client,
	apiURL, token, method string,
	body json.RawMessage,
	out any,
) (APIResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		endpointURL(apiURL, method),
		bytes.NewReader(body),
	)
	if err != nil {
		return APIResult{PermanentFailure: true, Message: err.Error()}, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return doRequest(client, req, out)
}

func callFormAt(
	ctx context.Context,
	client *http.Client,
	apiURL, token, method string,
	values url.Values,
	out any,
) (APIResult, error) {
	requestCtx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		endpointURL(apiURL, method),
		strings.NewReader(values.Encode()),
	)
	if err != nil {
		return APIResult{PermanentFailure: true, Message: err.Error()}, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return doRequest(client, req, out)
}

func doRequest(client *http.Client, req *http.Request, out any) (APIResult, error) {
	resp, err := httpClientWithoutRedirects(client).Do(req)
	if err != nil {
		return APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := retryAfter(resp.Header.Get("Retry-After"))
		return APIResult{RateLimited: true, RetryAfter: retryAfter, Message: "slack rate limited the request"}, nil
	}
	body, err := readResponseBody(resp.Body, toolResponseMaxBytes)
	if err != nil {
		return APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if code := slackErrorCode(body); code != "" {
			result := ErrorResult(code)
			if resp.StatusCode < 500 || result.RateLimited || result.TransientFailure ||
				result.Code == "integration_disabled" {
				return result, nil
			}
			return APIResult{
				Code:             "transient_failure",
				TransientFailure: true,
				Message:          fmt.Sprintf("slack returned status %d: %s", resp.StatusCode, code),
			}, nil
		}
		if resp.StatusCode >= 500 {
			return APIResult{
				Code:             "transient_failure",
				TransientFailure: true,
				Message:          fmt.Sprintf("slack returned status %d", resp.StatusCode),
			}, nil
		}
		return APIResult{
			Code:             "permanent_failure",
			PermanentFailure: true,
			Message:          fmt.Sprintf("slack returned status %d", resp.StatusCode),
		}, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	return APIResult{}, nil
}

func endpointURL(apiURL, method string) string {
	base := strings.TrimRight(apiURL, "/")
	if base == "" {
		base = defaultAPIURL
	}
	return base + "/" + method
}

func retryAfter(raw string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func slackTimestamp(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixNano())/float64(time.Second), 'f', 6, 64)
}

func ErrorResult(code string) APIResult {
	message := "slack rejected the request"
	if code != "" {
		message += ": " + code
	}
	switch code {
	case "ratelimited":
		return APIResult{ProviderCode: code, RateLimited: true, Message: message}
	case "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		return APIResult{Code: "transient_failure", ProviderCode: code, TransientFailure: true, Message: message}
	case "not_authed", "invalid_auth", "account_inactive", "token_revoked":
		return APIResult{
			Code:             "integration_disabled",
			ProviderCode:     code,
			PermanentFailure: true,
			Message:          "integration is disabled or credentials are invalid",
		}
	default:
		return APIResult{Code: "permanent_failure", ProviderCode: code, PermanentFailure: true, Message: message}
	}
}

func slackStatusError(action string, status int, body []byte) error {
	if code := slackErrorCode(body); code != "" {
		return fmt.Errorf("%s returned status %d: %s", action, status, code)
	}
	return fmt.Errorf("%s returned status %d", action, status)
}

func slackErrorCode(body []byte) string {
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Error)
}

func messageHasMarker(message readbackMessage, agentID, providerCallID, targetRef string) bool {
	if message.Metadata == nil || message.Metadata.EventType != MessageMarkerEventType {
		return false
	}
	payload := message.Metadata.EventPayload
	return payloadString(payload["agent_id"]) == agentID &&
		payloadString(payload["provider_call_id"]) == providerCallID &&
		payloadString(payload["target_ref"]) == targetRef
}

func payloadString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}
