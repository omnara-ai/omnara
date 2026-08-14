package blaxel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	apiBaseURL           = "https://api.blaxel.ai/v0"
	apiVersion           = "2026-04-28"
	maxSandboxListLimit  = 200
	requestRetries       = 2
	requestRetryDelay    = 250 * time.Millisecond
	maxRequestRetryDelay = 5 * time.Second
	maxAPIDiagnosticSize = 128
)

type apiClient interface {
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	StartSandboxProcess(context.Context, sandbox, processRequest) (sandboxProcess, error)
	GetSandboxProcess(context.Context, sandbox, string) (sandboxProcess, bool, error)
	WakeSandbox(context.Context, sandbox) error
}

type createSandboxRequest struct {
	Metadata resourceMetadata `json:"metadata"`
	Spec     sandboxSpec      `json:"spec"`
}

type resourceMetadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	URL    string            `json:"url,omitempty"`
}

type sandboxSpec struct {
	Region  string         `json:"region"`
	Runtime sandboxRuntime `json:"runtime"`
}

type sandboxRuntime struct {
	Image  string        `json:"image"`
	Memory int           `json:"memory"`
	Envs   []sandboxEnv  `json:"envs,omitempty"`
	Ports  []sandboxPort `json:"ports,omitempty"`
}

type sandboxEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sandboxPort struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Target   int    `json:"target"`
}

type sandbox struct {
	Metadata resourceMetadata        `json:"metadata"`
	State    sandboxRuntimeState     `json:"state"`
	Status   sandboxDeploymentStatus `json:"status"`
}

type sandboxListPage struct {
	Sandboxes  []sandbox
	HasMore    bool
	NextCursor string
}

func (p *sandboxListPage) UnmarshalJSON(raw []byte) error {
	*p = sandboxListPage{}
	var envelope struct {
		Data json.RawMessage `json:"data"`
		Meta json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) ||
		len(envelope.Meta) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Meta), []byte("null")) {
		return errors.New("missing data or meta")
	}
	if err := json.Unmarshal(envelope.Data, &p.Sandboxes); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	var meta struct {
		HasMore    *bool   `json:"hasMore"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(envelope.Meta, &meta); err != nil {
		return fmt.Errorf("decode meta: %w", err)
	}
	if meta.HasMore == nil {
		return errors.New("missing hasMore pagination metadata")
	}
	p.HasMore = *meta.HasMore
	if meta.NextCursor != nil {
		p.NextCursor = *meta.NextCursor
	}
	return nil
}

type processRequest struct {
	Name              string `json:"name"`
	Command           string `json:"command"`
	KeepAlive         bool   `json:"keepAlive"`
	Timeout           int    `json:"timeout"`
	WaitForCompletion bool   `json:"waitForCompletion"`
}

type sandboxProcess struct {
	PID       string               `json:"pid"`
	Status    sandboxProcessStatus `json:"status"`
	KeepAlive bool                 `json:"keepAlive"`
}

type restClient struct {
	apiBaseURL string
	workspace  string
	apiToken   string
	httpClient *http.Client
}

func (c *restClient) ListSandboxes(
	ctx context.Context,
	cursor string,
	limit int,
) (sandboxListPage, error) {
	if limit <= 0 || limit > maxSandboxListLimit {
		return sandboxListPage{}, fmt.Errorf(
			"blaxel sandbox list limit must be between 1 and %d",
			maxSandboxListLimit,
		)
	}
	requestURL, err := url.Parse(c.apiBaseURL + "/sandboxes")
	if err != nil {
		return sandboxListPage{}, fmt.Errorf("build blaxel sandbox list url: %w", err)
	}
	query := requestURL.Query()
	query.Set("limit", strconv.Itoa(limit))
	query.Set("q", "omnara-mch-")
	query.Set("sort", "name:asc")
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	requestURL.RawQuery = query.Encode()

	var page sandboxListPage
	_, err = c.doRequestWithHeaders(
		ctx,
		http.MethodGet,
		requestURL.String(),
		nil,
		&page,
		map[string]string{"Blaxel-Version": apiVersion},
	)
	if err != nil {
		return sandboxListPage{}, err
	}
	return page, nil
}

func (c *restClient) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	var result sandbox
	_, err := c.doRequestWithRetry(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/sandboxes?createIfNotExist=true",
		request,
		&result,
	)
	if err == nil || !isTransientServerError(err) {
		return result, err
	}
	if request.Metadata.Name != "" {
		if existing, found, getErr := c.GetSandbox(ctx, request.Metadata.Name); getErr == nil && found {
			return existing, nil
		}
	}
	return sandbox{}, err
}

func requestRetryDelayFor(err error, attempt int) time.Duration {
	delay := requestRetryDelay << attempt
	delay = delay/2 + time.Duration(rand.Int64N(int64(delay)))
	if retryAfter, ok := providers.RetryAfter(err); ok && retryAfter > delay {
		delay = retryAfter
	}
	return min(delay, maxRequestRetryDelay)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *restClient) GetSandbox(
	ctx context.Context,
	name string,
) (sandbox, bool, error) {
	var result sandbox
	_, err := c.doRequest(
		ctx,
		http.MethodGet,
		c.apiBaseURL+"/sandboxes/"+url.PathEscape(name),
		nil,
		&result,
	)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return result, true, nil
}

func (c *restClient) DeleteSandbox(ctx context.Context, name string) error {
	_, err := c.doRequest(
		ctx,
		http.MethodDelete,
		c.apiBaseURL+"/sandboxes/"+url.PathEscape(name),
		nil,
		nil,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) StartSandboxProcess(
	ctx context.Context,
	target sandbox,
	request processRequest,
) (sandboxProcess, error) {
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return sandboxProcess{}, err
	}
	var process sandboxProcess
	if _, err := c.doRequest(
		ctx,
		http.MethodPost,
		sandboxURL+"/process",
		request,
		&process,
	); err != nil {
		return sandboxProcess{}, err
	}
	return process, nil
}

func (c *restClient) GetSandboxProcess(
	ctx context.Context,
	target sandbox,
	name string,
) (sandboxProcess, bool, error) {
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return sandboxProcess{}, false, err
	}
	var process sandboxProcess
	_, err = c.doRequestWithRetry(
		ctx,
		http.MethodGet,
		sandboxURL+"/process/"+url.PathEscape(name),
		nil,
		&process,
	)
	if isNotFound(err) {
		return sandboxProcess{}, false, nil
	}
	if err != nil {
		return sandboxProcess{}, false, err
	}
	return process, true, nil
}

func (c *restClient) WakeSandbox(ctx context.Context, target sandbox) error {
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return err
	}
	statusCode, err := c.doRequestWithRetry(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/port/%d/", sandboxURL, daemonprotocol.WakeListenerPort),
		nil,
		nil,
	)
	if err != nil {
		return err
	}
	if statusCode != http.StatusNoContent {
		return fmt.Errorf(
			"blaxel wake endpoint returned HTTP %d, want HTTP %d",
			statusCode,
			http.StatusNoContent,
		)
	}
	return nil
}

func sandboxDataPlaneURL(target sandbox) (string, error) {
	sandboxURL := strings.TrimRight(strings.TrimSpace(target.Metadata.URL), "/")
	if sandboxURL == "" {
		return "", fmt.Errorf("blaxel sandbox %q is missing its data-plane url", target.Metadata.Name)
	}
	parsed, err := url.Parse(sandboxURL)
	if err != nil {
		return "", fmt.Errorf("blaxel sandbox %q has an invalid data-plane url", target.Metadata.Name)
	}
	if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!providers.IsHTTPS(parsed) {
		return "", fmt.Errorf("blaxel sandbox %q has an invalid data-plane url", target.Metadata.Name)
	}
	return sandboxURL, nil
}

func (c *restClient) doRequestWithRetry(
	ctx context.Context,
	method, requestURL string,
	body, out any,
) (int, error) {
	var statusCode int
	var lastErr error
	for attempt := 0; attempt <= requestRetries; attempt++ {
		statusCode, lastErr = c.doRequest(ctx, method, requestURL, body, out)
		if lastErr == nil || !isTransientServerError(lastErr) || attempt == requestRetries {
			return statusCode, lastErr
		}
		if err := waitForRetry(ctx, requestRetryDelayFor(lastErr, attempt)); err != nil {
			return 0, err
		}
	}
	return statusCode, lastErr
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, requestURL string,
	body, out any,
) (int, error) {
	return c.doRequestWithHeaders(ctx, method, requestURL, body, out, nil)
}

func (c *restClient) doRequestWithHeaders(
	ctx context.Context,
	method, requestURL string,
	body, out any,
	extraHeaders map[string]string,
) (int, error) {
	headers := map[string]string{
		"X-Blaxel-Authorization": "Bearer " + c.apiToken,
		"X-Blaxel-Workspace":     c.workspace,
	}
	for name, value := range extraHeaders {
		headers[name] = value
	}
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.Blaxel,
		method,
		requestURL,
		headers,
		body,
	)
	if err != nil {
		return 0, err
	}
	statusCode, raw := response.StatusCode, response.Body
	if statusCode < 200 || statusCode >= 300 {
		return statusCode, providers.WithRetryAfter(
			apiError{
				StatusCode: statusCode,
				ErrorCode:  responseErrorCode(raw),
				RequestID:  responseRequestID(response.Header),
			},
			response.Header,
		)
	}
	if out == nil || len(raw) == 0 {
		return statusCode, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return statusCode, fmt.Errorf("decode blaxel response: %w", err)
	}
	return statusCode, nil
}

type apiError struct {
	StatusCode int
	ErrorCode  string
	RequestID  string
}

func (e apiError) Error() string {
	message := fmt.Sprintf("blaxel API returned HTTP %d", e.StatusCode)
	if e.ErrorCode != "" {
		message += fmt.Sprintf(" (code %q)", e.ErrorCode)
	}
	if e.RequestID != "" {
		message += fmt.Sprintf(" (request %q)", e.RequestID)
	}
	return message
}

func responseErrorCode(raw []byte) string {
	var response struct {
		ErrorCode string `json:"errorCode"`
		Code      string `json:"code"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ""
	}
	code := strings.TrimSpace(response.ErrorCode)
	if code == "" {
		code = strings.TrimSpace(response.Code)
	}
	return boundedAPIDiagnostic(code)
}

func responseRequestID(header http.Header) string {
	for _, name := range []string{"X-Blaxel-Request-Id", "X-Request-Id", "Request-Id"} {
		if requestID := boundedAPIDiagnostic(header.Get(name)); requestID != "" {
			return requestID
		}
	}
	return ""
}

func boundedAPIDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxAPIDiagnosticSize {
		value = value[:maxAPIDiagnosticSize]
	}
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
}

func isStatus(err error, statusCode int) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

func isNotFound(err error) bool {
	return isStatus(err, http.StatusNotFound)
}

func isConflict(err error) bool {
	return isStatus(err, http.StatusConflict)
}

func isTransientServerError(err error) bool {
	return isStatus(err, http.StatusInternalServerError) ||
		isStatus(err, http.StatusBadGateway) ||
		isStatus(err, http.StatusServiceUnavailable) ||
		isStatus(err, http.StatusGatewayTimeout)
}
