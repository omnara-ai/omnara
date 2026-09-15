package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

var defaultHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: 30 * time.Second},
)

func httpClientWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		return defaultHTTPClient
	}
	return outboundhttp.CloneWithoutRedirects(client)
}

func readResponseBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("slack response exceeds the byte limit")
	}
	return data, nil
}

const (
	defaultAPIURL        = "https://slack.com/api"
	defaultToolTimeout   = 15 * time.Second
	toolResponseMaxBytes = 1024 * 1024
)

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
