// Package openrouterbatch sends model requests through the OpenRouter Batch API
// (https://openrouter.ai/docs/guides/features/batch-api). It wraps one of the
// synchronous provider clients and reuses its request building and response
// parsing; only the transport differs.
package openrouterbatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	completionWindow = "24h"
	errorSource      = "openrouter_batch"

	maxBatchResponseBytes      int64 = 64 * 1024 * 1024
	maxBatchErrorResponseBytes int64 = 64 * 1024
	batchRequestTimeout              = 2 * time.Minute

	// notFoundGrace is how long after submission a missing batch is treated
	// as not readable yet: OpenRouter accepts a batch before it can be read back.
	notFoundGrace = 10 * time.Minute

	statusCompleted = "completed"
	statusFailed    = "failed"
	statusExpired   = "expired"
	statusCanceled  = "cancelled" //nolint:misspell // OpenRouter's wire spelling.
)

// RouteProvider is implemented by the synchronous provider clients.
type RouteProvider interface {
	model.Client
	RouteClient() route.Client
}

// Client defers agent model calls to an OpenRouter batch. Prepare, Respond,
// and every capability question are answered by the wrapped client, so callers
// that cannot defer still get an ordinary synchronous call.
type Client struct {
	Inner RouteProvider
	// BatchesURL is the batch collection URL, for example
	// https://openrouter.ai/api/v1/batches.
	BatchesURL string
	Auth       route.Auth
	HTTPClient *http.Client
}

var (
	_ model.DeferredClient           = Client{}
	_ modelcontext.MediaProjector    = Client{}
	_ model.OutputTokenLimitProvider = Client{}
)

// BatchesURLForBaseURL derives the batch collection URL from a provider base
// URL such as https://openrouter.ai/api/v1 or https://openrouter.ai/api.
func BatchesURLForBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("batch base URL is invalid: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("batch base URL must be an absolute http or https URL")
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	parsed.Path = path + "/batches"
	parsed.RawQuery, parsed.Fragment = "", ""
	return parsed.String(), nil
}

// EndpointForAPIFormat is the batch `endpoint` value for a request shape.
func EndpointForAPIFormat(format modelprotocol.APIFormat) (string, error) {
	switch format {
	case modelprotocol.APIFormatOpenAIChatCompletions:
		return "/v1/chat/completions", nil
	case modelprotocol.APIFormatOpenAIResponses:
		return "/v1/responses", nil
	case modelprotocol.APIFormatAnthropicMessages:
		return "/v1/messages", nil
	default:
		return "", fmt.Errorf("api_format %q is not supported by the OpenRouter Batch API", format)
	}
}

func (c Client) RequestedProviderModelSlug() string { return c.Inner.RequestedProviderModelSlug() }

func (c Client) APIFormat() modelprotocol.APIFormat { return c.Inner.APIFormat() }

func (c Client) ModelAPIVariant() modelprotocol.APIVariant { return c.Inner.ModelAPIVariant() }

func (c Client) Capabilities() model.Capabilities { return c.Inner.Capabilities() }

func (c Client) Prepare(ctx context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	return c.Inner.Prepare(ctx, input)
}

func (c Client) Respond(ctx context.Context, input model.Request) (model.Response, error) {
	return c.Inner.Respond(ctx, input)
}

func (c Client) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	return c.Inner.RouteClient().ProjectRenderedMedia(bundle)
}

func (c Client) OutputTokenLimits() (model.OutputTokenLimits, error) {
	if provider, ok := c.Inner.(model.OutputTokenLimitProvider); ok {
		return provider.OutputTokenLimits()
	}
	return model.OutputTokenLimits{}, nil
}

// WithoutManualThinking is used by compaction, which always runs synchronously.
func (c Client) WithoutManualThinking() (model.Client, error) {
	if provider, ok := c.Inner.(interface {
		WithoutManualThinking() (model.Client, error)
	}); ok {
		return provider.WithoutManualThinking()
	}
	return c.Inner, nil
}

type createBatchRequest struct {
	// The server streams the body, so these fields must precede requests.
	Endpoint         string         `json:"endpoint"`
	Model            string         `json:"model"`
	CompletionWindow string         `json:"completion_window"`
	CallbackURL      string         `json:"callback_url,omitempty"`
	Requests         []batchRequest `json:"requests"`
}

type batchRequest struct {
	CustomID string          `json:"custom_id"`
	Body     json.RawMessage `json:"body"`
}

type batchObject struct {
	ID        string        `json:"id"`
	Status    string        `json:"status"`
	ExpiresAt *int64        `json:"expires_at"`
	Results   []batchResult `json:"results"`
	Usage     *batchUsage   `json:"usage"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// batchUsage is the whole batch's accounting: what OpenRouter charged, plus
// the upstream inference cost when the batch ran on a BYOK key.
type batchUsage struct {
	Cost        json.RawMessage `json:"cost"`
	IsBYOK      *bool           `json:"is_byok"`
	CostDetails *struct {
		UpstreamInferenceCost json.RawMessage `json:"upstream_inference_cost"`
	} `json:"cost_details"`
}

// reportedCost is the batch's total cost in USD, or "" when it is not reported.
func (u *batchUsage) reportedCost() modelenvelope.ProviderReportedCostUSD {
	if u == nil {
		return ""
	}
	costs := []string{strings.TrimSpace(string(u.Cost))}
	if u.IsBYOK != nil && *u.IsBYOK && u.CostDetails != nil {
		if upstream := strings.TrimSpace(string(u.CostDetails.UpstreamInferenceCost)); upstream != "" && upstream != "null" {
			costs = append(costs, upstream)
		}
	}
	if costs[0] == "" || costs[0] == "null" {
		return ""
	}
	total, valid := modelenvelope.SumProviderReportedCostUSD(costs...)
	if !valid {
		return ""
	}
	return total
}

type batchResult struct {
	CustomID string `json:"custom_id"`
	Response *struct {
		StatusCode int             `json:"status_code"`
		RequestID  string          `json:"request_id"`
		Body       json.RawMessage `json:"body"`
	} `json:"response"`
	Error *struct {
		Type      string `json:"type"`
		Message   string `json:"message"`
		ErrorType string `json:"error_type"`
	} `json:"error"`
}

func (c Client) SubmitDeferred(ctx context.Context, input model.DeferredRequest) (model.DeferredSubmission, error) {
	if strings.TrimSpace(input.CustomID) == "" {
		return model.DeferredSubmission{}, route.SetupError{Err: errors.New("batch custom id is required")}
	}
	endpoint, err := EndpointForAPIFormat(c.APIFormat())
	if err != nil {
		return model.DeferredSubmission{}, route.SetupError{Err: err}
	}
	body, modelSlug, err := batchRequestBody(input.ProviderRequest)
	if err != nil {
		return model.DeferredSubmission{}, route.SetupError{Err: err}
	}
	if modelSlug == "" {
		modelSlug = c.RequestedProviderModelSlug()
	}
	payload, err := json.Marshal(createBatchRequest{
		Endpoint:         endpoint,
		Model:            modelSlug,
		CompletionWindow: completionWindow,
		CallbackURL:      input.CallbackURL,
		Requests:         []batchRequest{{CustomID: input.CustomID, Body: body}},
	})
	if err != nil {
		return model.DeferredSubmission{}, route.SetupError{Err: err}
	}
	status, header, responseBody, err := c.do(ctx, http.MethodPost, c.BatchesURL, payload)
	if err != nil {
		var setupErr route.SetupError
		if errors.As(err, &setupErr) {
			return model.DeferredSubmission{}, err
		}
		// The batch may have been accepted even though the response was lost.
		return model.DeferredSubmission{}, model.AmbiguousProviderOutcome(transportError(c.BatchesURL, err))
	}
	if status < 200 || status >= 300 {
		return model.DeferredSubmission{}, httpStatusError(c.BatchesURL, status, header, responseBody)
	}
	var created batchObject
	if err := json.Unmarshal(responseBody, &created); err != nil || strings.TrimSpace(created.ID) == "" {
		return model.DeferredSubmission{}, model.MalformedProviderSuccess(
			errorSource, "malformed_batch_response",
			"OpenRouter accepted the batch but returned no batch id.", err,
		)
	}
	submission := model.DeferredSubmission{ProviderHandle: created.ID}
	if created.ExpiresAt != nil && *created.ExpiresAt > 0 {
		submission.ExpiresAt = time.Unix(*created.ExpiresAt, 0).UTC()
	}
	return submission, nil
}

func (c Client) CollectDeferred(ctx context.Context, handle model.DeferredHandle) (model.Response, error) {
	if strings.TrimSpace(handle.ProviderHandle) == "" {
		return model.Response{}, route.SetupError{Err: errors.New("batch id is required")}
	}
	endpoint := strings.TrimRight(c.BatchesURL, "/") + "/" + url.PathEscape(handle.ProviderHandle)
	status, header, body, err := c.do(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		var setupErr route.SetupError
		if errors.As(err, &setupErr) || ctx.Err() != nil {
			return model.Response{}, err
		}
		if errors.Is(err, route.ErrProviderResponseTooLarge) {
			return model.Response{}, model.ProviderError{
				Kind: model.ErrorKindUnknown, Source: errorSource, Code: "provider_response_too_large",
				Message: "The OpenRouter batch result is larger than the supported limit.", Cause: err,
			}
		}
		return model.Response{}, pending(err)
	}
	switch {
	case status == http.StatusNotFound && !handle.SubmittedAt.IsZero() &&
		time.Since(handle.SubmittedAt) < notFoundGrace:
		return model.Response{}, pending(httpStatusError(endpoint, status, header, body))
	case status == http.StatusTooManyRequests || status >= 500:
		// Reading the batch is idempotent; look again instead of failing the call.
		return model.Response{}, pending(httpStatusError(endpoint, status, header, body))
	case status < 200 || status >= 300:
		return model.Response{}, httpStatusError(endpoint, status, header, body)
	}
	var batch batchObject
	if err := json.Unmarshal(body, &batch); err != nil {
		return model.Response{}, model.MalformedProviderSuccess(
			errorSource, "malformed_batch_response", "OpenRouter returned a malformed batch.", err,
		)
	}
	switch batch.Status {
	case statusCompleted:
		if batch.Results == nil {
			// The final status can be visible shortly before the results are.
			return model.Response{}, model.ErrDeferredPending
		}
		return c.resultResponse(ctx, batch, handle.CustomID)
	case statusFailed:
		return model.Response{}, batchError(model.ErrorKindInvalidRequest, "batch_failed", batch,
			"OpenRouter rejected the batch.")
	case statusExpired:
		return model.Response{}, batchError(model.ErrorKindProviderUnavailable, "batch_expired", batch,
			"The OpenRouter batch expired before the provider finished it.")
	case statusCanceled:
		return model.Response{}, batchError(model.ErrorKindProviderUnavailable, "batch_canceled", batch,
			"The provider canceled the OpenRouter batch.")
	default:
		return model.Response{}, model.ErrDeferredPending
	}
}

func (c Client) resultResponse(ctx context.Context, batch batchObject, customID string) (model.Response, error) {
	for _, result := range batch.Results {
		if result.CustomID != customID {
			continue
		}
		if result.Response == nil {
			return model.Response{}, resultError(result)
		}
		header := http.Header{}
		if result.Response.RequestID != "" {
			header.Set("X-Request-ID", result.Response.RequestID)
		}
		statusCode := result.Response.StatusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		response, err := c.Inner.RouteClient().ParseBufferedResponse(ctx, route.Response{
			StatusCode: statusCode,
			Header:     header,
			Body:       result.Response.Body,
		})
		// A batch result need not repeat the cost in its body. This client
		// submits one request per batch, so the batch's cost is that request's.
		if err == nil && response.ProviderReportedCostUSD == "" && len(batch.Results) == 1 {
			response.ProviderReportedCostUSD = batch.Usage.reportedCost()
		}
		return response, err
	}
	return model.Response{}, model.ProviderError{
		Kind:    model.ErrorKindProviderUnavailable,
		Source:  errorSource,
		Code:    "batch_result_missing",
		Message: "The OpenRouter batch completed without a result for this request.",
	}
}

func (c Client) do(
	ctx context.Context, method, endpoint string, payload []byte,
) (int, http.Header, []byte, error) {
	if strings.TrimSpace(c.BatchesURL) == "" {
		return 0, nil, nil, route.SetupError{Err: errors.New("batch URL is required")}
	}
	ctx, cancel := context.WithTimeout(ctx, batchRequestTimeout)
	defer cancel()
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, nil, route.SetupError{Err: err}
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Auth != nil {
		if err := c.Auth.Apply(req); err != nil {
			return 0, nil, nil, route.SetupError{Kind: model.ErrorKindAuth, Err: err}
		}
	}
	resp, err := outboundhttp.CloneWithoutRedirects(c.HTTPClient).Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	limit := maxBatchResponseBytes
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limit = maxBatchErrorResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	if int64(len(body)) > limit {
		if limit == maxBatchErrorResponseBytes {
			return resp.StatusCode, resp.Header, body[:limit], nil
		}
		return resp.StatusCode, resp.Header, nil, route.ErrProviderResponseTooLarge
	}
	return resp.StatusCode, resp.Header, body, nil
}

// batchRequestBody turns a prepared synchronous request into a batch request
// body. Batches reject streaming, which is the only thing Prepare adds on top
// of the protocol's plain request.
func batchRequestBody(prepared json.RawMessage) (json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(prepared, &fields); err != nil || fields == nil {
		return nil, "", errors.New("batch request body must be a JSON object")
	}
	delete(fields, "stream")
	delete(fields, "stream_options")
	var modelSlug string
	if raw, ok := fields["model"]; ok {
		_ = json.Unmarshal(raw, &modelSlug)
	}
	body, err := json.Marshal(fields)
	return body, strings.TrimSpace(modelSlug), err
}

type pendingError struct{ cause error }

func (e pendingError) Error() string {
	return "openrouter batch is not readable yet: " + e.cause.Error()
}

func (e pendingError) Unwrap() []error { return []error{model.ErrDeferredPending, e.cause} }

func pending(cause error) error { return pendingError{cause: cause} }

func transportError(source string, cause error) error {
	return model.ProviderError{
		Kind:    model.ErrorKindTransient,
		Source:  source,
		Message: cause.Error(),
		Cause:   cause,
	}
}

func httpStatusError(source string, status int, header http.Header, body []byte) error {
	var decoded struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &decoded)
	message := strings.TrimSpace(decoded.Error.Message)
	if message == "" {
		message = fmt.Sprintf("The OpenRouter Batch API returned HTTP status %d.", status)
	}
	return model.ProviderError{
		Kind:       model.ErrorKindFromHTTPStatus(status),
		Source:     source,
		StatusCode: status,
		Code:       "batch_http_error",
		Message:    message,
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
	}
}

func batchError(kind model.ErrorKind, code string, batch batchObject, fallback string) error {
	message := fallback
	if batch.Error != nil && strings.TrimSpace(batch.Error.Message) != "" {
		message = strings.TrimSpace(batch.Error.Message)
	}
	return model.ProviderError{Kind: kind, Source: errorSource, Code: code, Message: message}
}

func resultError(result batchResult) error {
	providerErr := model.ProviderError{
		Kind:    model.ErrorKindUnknown,
		Source:  errorSource,
		Code:    "batch_request_failed",
		Message: "The OpenRouter batch request failed without an error message.",
	}
	if result.Error == nil {
		return providerErr
	}
	if message := strings.TrimSpace(result.Error.Message); message != "" {
		providerErr.Message = message
	}
	kind := strings.ToLower(result.Error.ErrorType + " " + result.Error.Type)
	switch {
	case strings.Contains(kind, "rate_limit"):
		providerErr.Kind = model.ErrorKindRateLimit
	case strings.Contains(kind, "auth"), strings.Contains(kind, "permission"):
		providerErr.Kind = model.ErrorKindAuth
	case strings.Contains(kind, "invalid"), strings.Contains(kind, "not_found"):
		providerErr.Kind = model.ErrorKindInvalidRequest
	case strings.Contains(kind, "overloaded"), strings.Contains(kind, "server"),
		strings.Contains(kind, "timeout"), strings.Contains(kind, "api_error"):
		providerErr.Kind = model.ErrorKindProviderUnavailable
	}
	if code := strings.TrimSpace(result.Error.ErrorType); code != "" {
		providerErr.Code = code
	} else if code := strings.TrimSpace(result.Error.Type); code != "" {
		providerErr.Code = code
	}
	return providerErr
}
