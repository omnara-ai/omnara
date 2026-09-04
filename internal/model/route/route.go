package route

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
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"golang.org/x/net/http/httpguts"
)

type Protocol interface {
	APIFormat() modelprotocol.APIFormat
	ModelAPIVariant() modelprotocol.APIVariant
	BuildRequest(context.Context, model.PrepareInput) (json.RawMessage, error)
	ProjectRenderedMedia(modelcontext.Bundle) []modelcontext.RenderedMedia
	ParseResponse(context.Context, Response) (model.Response, error)
}

type StreamProtocol interface {
	StreamAccept() string
	IsStreamingResponse(contentType string) bool
	BuildStreamRequest(body json.RawMessage) (json.RawMessage, error)
	ConsumeStream(
		ctx context.Context,
		body io.Reader,
		statusCode int,
		header http.Header,
		sink model.StreamSink,
	) (model.Response, error)
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type Endpoint interface {
	URL() (string, error)
}

type StaticEndpoint struct {
	BaseURL        string
	DefaultBaseURL string
	Path           string
}

func (e StaticEndpoint) URL() (string, error) {
	baseURL := strings.TrimRight(e.BaseURL, "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(e.DefaultBaseURL, "/")
	}
	if baseURL == "" {
		return "", SetupError{Err: errors.New("model endpoint base URL is required")}
	}
	endpoint := baseURL
	if e.Path != "" {
		path := e.Path
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		endpoint += path
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", SetupError{Err: fmt.Errorf("model endpoint URL is invalid: %w", err)}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", SetupError{Err: fmt.Errorf("model endpoint URL must use http or https: %s", endpoint)}
	}
	if parsed.Host == "" {
		return "", SetupError{Err: fmt.Errorf("model endpoint URL must include a host: %s", endpoint)}
	}
	return endpoint, nil
}

// Auth implementations must tolerate Apply running during validation and again before sending.
type Auth interface {
	Apply(*http.Request) error
}

type BearerToken struct {
	Token string
}

func (a BearerToken) Apply(req *http.Request) error {
	if a.Token == "" {
		return errors.New("bearer token is required")
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	return nil
}

type HeaderAuth struct {
	Header string
	Value  string
}

func (a HeaderAuth) Apply(req *http.Request) error {
	if a.Header == "" {
		return errors.New("auth header name is required")
	}
	if a.Value == "" {
		return fmt.Errorf("%s is required", a.Header)
	}
	req.Header.Set(a.Header, a.Value)
	return nil
}

type Headers map[string]string

func (h Headers) Apply(req *http.Request) error {
	for key, value := range h {
		if key == "" {
			return errors.New("header name is required")
		}
		req.Header.Set(key, value)
	}
	return nil
}

type Chain []Auth

func (c Chain) Apply(req *http.Request) error {
	for _, auth := range c {
		if auth == nil {
			continue
		}
		if err := auth.Apply(req); err != nil {
			return err
		}
	}
	return nil
}

type HTTPTransport struct {
	Client                *http.Client
	Method                string
	MaxResponseBytes      int64
	MaxErrorResponseBytes int64
}

const (
	defaultProviderRequestTimeout = 10 * time.Minute

	defaultMaxProviderResponseBytes      int64 = 64 * 1024 * 1024
	defaultMaxProviderErrorResponseBytes int64 = 64 * 1024
	providerErrorCodeBodyReadFailed            = "provider_error_body_read_failed"
)

func (t HTTPTransport) responseByteLimit() int64 {
	if t.MaxResponseBytes > 0 {
		return t.MaxResponseBytes
	}
	return defaultMaxProviderResponseBytes
}

func (t HTTPTransport) errorResponseByteLimit() int64 {
	if t.MaxErrorResponseBytes > 0 {
		return t.MaxErrorResponseBytes
	}
	return defaultMaxProviderErrorResponseBytes
}

type SetupError struct {
	Kind model.ErrorKind
	Err  error
}

func (e SetupError) Error() string {
	if e.Err == nil {
		return "model route setup failed"
	}
	return e.Err.Error()
}

func (e SetupError) Unwrap() error {
	return e.Err
}

func (e SetupError) ProviderErrorClassification() model.ProviderError {
	kind := e.Kind
	if kind == "" {
		kind = model.ErrorKindInvalidRequest
	}
	return model.ProviderError{
		Kind:    kind,
		Message: e.Error(),
		Cause:   e.Err,
	}
}

func (t HTTPTransport) validateRequest(
	ctx context.Context,
	endpoint string,
	body json.RawMessage,
	auth Auth,
	streamMediaType string,
) error {
	_, err := t.newRequest(ctx, endpoint, body, auth, streamMediaType)
	return err
}

func (t HTTPTransport) newRequest(
	ctx context.Context,
	endpoint string,
	body json.RawMessage,
	auth Auth,
	streamMediaType string,
) (*http.Request, error) {
	if !json.Valid(body) {
		return nil, SetupError{Err: errors.New("model provider request body is not valid JSON")}
	}
	method := t.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, SetupError{Err: err}
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, SetupError{Err: fmt.Errorf("model endpoint URL must use http or https: %s", endpoint)}
	}
	if req.URL.Host == "" {
		return nil, SetupError{Err: fmt.Errorf("model endpoint URL must include a host: %s", endpoint)}
	}
	req.Header.Set("Content-Type", "application/json")
	if streamMediaType != "" {
		req.Header.Set("Accept", streamMediaType)
	}
	if auth != nil {
		if err := auth.Apply(req); err != nil {
			return nil, SetupError{Kind: model.ErrorKindAuth, Err: err}
		}
	}
	for name, values := range req.Header {
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, SetupError{Err: fmt.Errorf("invalid model route header name %q", name)}
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return nil, SetupError{Err: fmt.Errorf("invalid value for model route header %q", name)}
			}
		}
	}
	return req, nil
}

func (t HTTPTransport) httpClient() *http.Client {
	if t.Client == nil {
		return outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{
			DisableResponseHeaderTimeout: true,
			Timeout:                      defaultProviderRequestTimeout,
		})
	}
	return outboundhttp.CloneWithoutRedirects(t.Client)
}

type Client struct {
	ProviderModelSlug string
	ModelCapabilities model.Capabilities
	Endpoint          Endpoint
	Auth              Auth
	Transport         HTTPTransport
	Protocol          Protocol
}

func (c Client) RequestedProviderModelSlug() string {
	return c.ProviderModelSlug
}

func (c Client) APIFormat() modelprotocol.APIFormat {
	if c.Protocol == nil {
		return ""
	}
	return c.Protocol.APIFormat()
}

func (c Client) ModelAPIVariant() modelprotocol.APIVariant {
	if c.Protocol == nil {
		return ""
	}
	return c.Protocol.ModelAPIVariant()
}

func (c Client) Capabilities() model.Capabilities {
	return c.ModelCapabilities
}

func (c Client) ProjectRenderedMedia(bundle modelcontext.Bundle) []modelcontext.RenderedMedia {
	if c.Protocol == nil {
		return nil
	}
	return c.Protocol.ProjectRenderedMedia(bundle)
}

func (c Client) Prepare(ctx context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	if c.Protocol == nil {
		return model.PreparedRequest{}, SetupError{Err: errors.New("model route protocol is required")}
	}
	if c.Endpoint == nil {
		return model.PreparedRequest{}, SetupError{Err: errors.New("model route endpoint is required")}
	}
	endpoint, err := c.Endpoint.URL()
	if err != nil {
		return model.PreparedRequest{}, SetupError{Err: err}
	}
	body, err := c.Protocol.BuildRequest(ctx, input)
	if err != nil {
		return model.PreparedRequest{}, SetupError{Err: err}
	}
	if !json.Valid(body) {
		return model.PreparedRequest{}, SetupError{Err: errors.New("model protocol built an invalid JSON request")}
	}
	streamMediaType := ""
	if streamProtocol, ok := c.Protocol.(StreamProtocol); ok {
		streamBody, streamErr := streamProtocol.BuildStreamRequest(body)
		if streamErr != nil {
			return model.PreparedRequest{}, SetupError{Err: streamErr}
		}
		if !json.Valid(streamBody) {
			return model.PreparedRequest{}, SetupError{Err: errors.New("model protocol built an invalid JSON stream request")}
		}
		body = streamBody
		streamMediaType = streamProtocol.StreamAccept()
		if strings.TrimSpace(streamMediaType) == "" {
			return model.PreparedRequest{}, SetupError{Err: errors.New("model stream media type is required")}
		}
	}
	if err := c.Transport.validateRequest(ctx, endpoint, body, c.Auth, streamMediaType); err != nil {
		return model.PreparedRequest{}, err
	}
	renderedMedia := c.Protocol.ProjectRenderedMedia(input.Context)
	inputTokenEstimate := modelcontext.EstimatePreparedRequest(body, renderedMedia)
	return model.PreparedRequest{Body: body, InputTokenEstimate: inputTokenEstimate}, nil
}

func (c Client) RespondStream(ctx context.Context, input model.Request) (model.Response, error) {
	if c.Protocol == nil {
		return model.Response{}, SetupError{Err: errors.New("model route protocol is required")}
	}
	streamProto, ok := c.Protocol.(StreamProtocol)
	if !ok {
		return model.Response{}, SetupError{Err: errors.New("model route protocol does not support streaming")}
	}
	if c.Endpoint == nil {
		return model.Response{}, SetupError{Err: errors.New("model route endpoint is required")}
	}
	endpoint, err := c.Endpoint.URL()
	if err != nil {
		return model.Response{}, SetupError{Err: err}
	}
	streamMediaType := streamProto.StreamAccept()
	if strings.TrimSpace(streamMediaType) == "" {
		return model.Response{}, SetupError{Err: errors.New("model stream media type is required")}
	}
	resp, err := c.Transport.StreamingDo(
		ctx,
		endpoint,
		input.ProviderRequest,
		c.Auth,
		streamMediaType,
	)
	if err != nil {
		var setupErr SetupError
		if errors.As(err, &setupErr) {
			return model.Response{}, setupErr
		}
		return model.Response{}, ambiguousTransportError(endpoint, resp.StatusCode, resp.Header, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, exceeded, readErr := ReadAllAndClose(resp, c.Transport.errorResponseByteLimit())
		if readErr != nil {
			return model.Response{}, providerErrorBodyReadError(
				endpoint,
				resp.StatusCode,
				resp.Header,
				readErr,
			)
		}
		if exceeded {
			if prefix, ok := firstCompleteJSONValue(errBody); ok {
				parsed, parseErr := c.parseResponse(ctx, endpoint, Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header,
					Body:       prefix,
				})
				if parseErr != nil {
					return parsed, parseErr
				}
			}
			return model.Response{}, oversizedProviderResponseError(
				endpoint,
				resp.StatusCode,
				resp.Header,
				"provider_error_body_too_large",
				"The model provider returned an error body larger than the supported limit.",
			)
		}
		return c.parseResponse(ctx, endpoint, Response{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Body:       errBody,
		})
	}
	if !streamProto.IsStreamingResponse(resp.Header.Get("Content-Type")) {
		respBody, exceeded, readErr := ReadAllAndClose(resp, c.Transport.responseByteLimit())
		if readErr != nil {
			return model.Response{}, ambiguousTransportError(endpoint, resp.StatusCode, resp.Header, readErr)
		}
		if exceeded {
			return model.Response{}, model.AmbiguousProviderOutcome(oversizedProviderResponseError(
				endpoint,
				resp.StatusCode,
				resp.Header,
				"provider_response_too_large",
				"The model provider returned a response larger than the supported limit.",
			))
		}
		return c.parseResponse(ctx, endpoint, Response{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
			Body:       respBody,
		})
	}
	defer func() { _ = resp.Body.Close() }()
	limitedBody := &responseLimitReader{
		reader:    resp.Body,
		remaining: c.Transport.responseByteLimit(),
	}
	response, consumeErr := streamProto.ConsumeStream(
		ctx,
		limitedBody,
		resp.StatusCode,
		resp.Header,
		input.DeltaSink,
	)
	response.ProviderRequestID = model.RequestIDFromHeader(resp.Header)
	if limitedBody.exceeded || errors.Is(consumeErr, ErrProviderResponseTooLarge) {
		return response, model.AmbiguousProviderOutcome(oversizedProviderResponseError(
			endpoint,
			resp.StatusCode,
			resp.Header,
			"provider_response_too_large",
			"The model provider returned a streamed response larger than the supported limit.",
		))
	}
	return response, enrichProviderErrorFromHeader(consumeErr, resp.Header)
}

func firstCompleteJSONValue(body []byte) (json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil || !json.Valid(value) {
		return nil, false
	}
	return value, true
}

func oversizedProviderResponseError(
	source string,
	statusCode int,
	header http.Header,
	code, message string,
) error {
	kind := model.ErrorKindFromHTTPStatus(statusCode)
	return model.ProviderError{
		Kind:       kind,
		Source:     source,
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
		Retryable:  model.ShouldRetryFromHeader(header),
		Cause:      ErrProviderResponseTooLarge,
	}
}

func providerErrorBodyReadError(
	source string,
	statusCode int,
	header http.Header,
	cause error,
) error {
	return model.ProviderError{
		Kind:       model.ErrorKindFromHTTPStatus(statusCode),
		Source:     source,
		StatusCode: statusCode,
		Code:       providerErrorCodeBodyReadFailed,
		Message: fmt.Sprintf(
			"The model provider returned HTTP status %d, but its error body could not be read.",
			statusCode,
		),
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
		Retryable:  model.ShouldRetryFromHeader(header),
		Cause:      cause,
	}
}

func (c Client) parseResponse(ctx context.Context, source string, response Response) (model.Response, error) {
	out, err := c.Protocol.ParseResponse(ctx, response)
	out.ProviderRequestID = model.RequestIDFromHeader(response.Header)
	if err == nil {
		return out, nil
	}
	if _, ok := model.ClassifyError(err); ok || model.IsAmbiguousProviderOutcome(err) {
		return out, enrichProviderErrorFromHeader(err, response.Header)
	}
	return out, model.ProviderError{
		Kind:       model.ErrorKindUnknown,
		Source:     source,
		StatusCode: response.StatusCode,
		Message:    err.Error(),
		RequestID:  model.RequestIDFromHeader(response.Header),
		RetryAfter: model.RetryAfterFromHeader(response.Header),
		Retryable:  model.ShouldRetryFromHeader(response.Header),
		Cause:      err,
	}
}

func enrichProviderErrorFromHeader(err error, header http.Header) error {
	if err == nil {
		return nil
	}
	providerErr, ok := model.ClassifyError(err)
	if !ok {
		return err
	}
	changed := false
	if providerErr.RequestID == "" {
		providerErr.RequestID = model.RequestIDFromHeader(header)
		changed = providerErr.RequestID != ""
	}
	if providerErr.RetryAfter == nil {
		providerErr.RetryAfter = model.RetryAfterFromHeader(header)
		changed = changed || providerErr.RetryAfter != nil
	}
	if providerErr.Retryable == nil {
		providerErr.Retryable = model.ShouldRetryFromHeader(header)
		changed = changed || providerErr.Retryable != nil
	}
	if !changed {
		return err
	}
	if providerErr.Cause == nil {
		providerErr.Cause = err
	}
	enriched := error(providerErr)
	if model.IsAmbiguousProviderOutcome(err) {
		enriched = model.AmbiguousProviderOutcome(enriched)
	}
	return enriched
}

func ambiguousTransportError(source string, statusCode int, header http.Header, cause error) error {
	return model.AmbiguousProviderOutcome(model.ProviderError{
		Kind:       model.ErrorKindTransient,
		Source:     source,
		StatusCode: statusCode,
		Message:    cause.Error(),
		RequestID:  model.RequestIDFromHeader(header),
		RetryAfter: model.RetryAfterFromHeader(header),
		Retryable:  model.ShouldRetryFromHeader(header),
		Cause:      cause,
	})
}

func MatchesMediaType(contentType, expected string) bool {
	mediaType, _, _ := strings.Cut(strings.ToLower(contentType), ";")
	return strings.TrimSpace(mediaType) == strings.ToLower(strings.TrimSpace(expected))
}
