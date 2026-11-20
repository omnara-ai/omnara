package groundbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const defaultAPIBaseURL = "https://api.groundbox.dev/v1"

var lifecycleHTTPClient = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{
	DisableResponseHeaderTimeout: true,
	Timeout:                      lifecycleRequestTimeout,
})

type sandboxStatus string

const (
	sandboxStatusQueued       sandboxStatus = "queued"
	sandboxStatusStarting     sandboxStatus = "starting"
	sandboxStatusRunning      sandboxStatus = "running"
	sandboxStatusPausing      sandboxStatus = "pausing"
	sandboxStatusPaused       sandboxStatus = "paused"
	sandboxStatusResuming     sandboxStatus = "resuming"
	sandboxStatusShuttingDown sandboxStatus = "shutting_down"
	sandboxStatusShutDown     sandboxStatus = "shut_down"
	sandboxStatusStopping     sandboxStatus = "stopping"
	sandboxStatusStopped      sandboxStatus = "stopped"
	sandboxStatusFailed       sandboxStatus = "failed"

	commandStateRunning = "running"
)

type sandbox struct {
	ID       string            `json:"id"`
	Status   sandboxStatus     `json:"status"`
	CPUs     int               `json:"cpus"`
	MemoryGB int               `json:"memory_gb"`
	Metadata map[string]string `json:"metadata"`
	Error    string            `json:"error"`
}

type createSandboxRequest struct {
	CPUs               int               `json:"cpus"`
	MemoryGB           int               `json:"memory_gb"`
	Template           string            `json:"template,omitempty"`
	TimeoutMinutes     int               `json:"timeout_minutes"`
	IdleTimeoutSeconds int               `json:"idle_timeout_seconds,omitempty"`
	Metadata           map[string]string `json:"metadata"`
}

type startCommandRequest struct {
	Cmd      string            `json:"cmd"`
	Env      map[string]string `json:"env,omitempty"`
	TimeoutS int               `json:"timeout_s"`
}

type command struct {
	ID    string `json:"id"`
	Cmd   string `json:"cmd"`
	State string `json:"state"`
}

type apiClient interface {
	CreateSandbox(ctx context.Context, idempotencyKey string, request createSandboxRequest) (sandbox, error)
	ListSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error)
	GetSandbox(ctx context.Context, id string) (sandbox, bool, error)
	ResumeSandbox(ctx context.Context, id string) (sandbox, error)
	StartSandbox(ctx context.Context, id string) (sandbox, error)
	DeleteSandbox(ctx context.Context, id string) error
	ListRunningCommands(ctx context.Context, id string) ([]command, error)
	StartCommand(ctx context.Context, id string, request startCommandRequest) (command, error)
}

type restClient struct {
	baseURL         string
	apiKey          string
	httpClient      *http.Client
	lifecycleClient *http.Client
}

func newRESTClient(baseURL, apiKey string, httpClient, lifecycleClient *http.Client) *restClient {
	return &restClient{
		baseURL:         strings.TrimRight(baseURL, "/"),
		apiKey:          apiKey,
		httpClient:      httpClient,
		lifecycleClient: lifecycleClient,
	}
}

func (c *restClient) CreateSandbox(
	ctx context.Context,
	idempotencyKey string,
	request createSandboxRequest,
) (sandbox, error) {
	var created sandbox
	err := c.do(
		ctx,
		c.lifecycleClient,
		http.MethodPost,
		"/sandboxes",
		map[string]string{"Idempotency-Key": idempotencyKey},
		request,
		&created,
	)
	return created, err
}

func (c *restClient) ListSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error) {
	values := url.Values{"live": {"true"}, "limit": {"2"}}
	for key, value := range metadata {
		values.Set("metadata["+key+"]", value)
	}
	var out struct {
		Data []sandbox `json:"data"`
	}
	err := c.do(ctx, c.httpClient, http.MethodGet, "/sandboxes?"+values.Encode(), nil, nil, &out)
	return out.Data, err
}

func (c *restClient) GetSandbox(ctx context.Context, id string) (sandbox, bool, error) {
	var current sandbox
	err := c.do(ctx, c.httpClient, http.MethodGet, sandboxPath(id), nil, nil, &current)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	return current, err == nil, err
}

func (c *restClient) ResumeSandbox(ctx context.Context, id string) (sandbox, error) {
	var current sandbox
	err := c.do(ctx, c.lifecycleClient, http.MethodPost, sandboxPath(id)+"/resume", nil, nil, &current)
	return current, err
}

func (c *restClient) StartSandbox(ctx context.Context, id string) (sandbox, error) {
	var current sandbox
	err := c.do(ctx, c.lifecycleClient, http.MethodPost, sandboxPath(id)+"/start", nil, nil, &current)
	return current, err
}

func (c *restClient) DeleteSandbox(ctx context.Context, id string) error {
	err := c.do(ctx, c.lifecycleClient, http.MethodDelete, sandboxPath(id), nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) ListRunningCommands(ctx context.Context, id string) ([]command, error) {
	var out struct {
		Data []command `json:"data"`
	}
	path := sandboxPath(id) + "/commands?" + url.Values{"state": {commandStateRunning}, "limit": {"500"}}.Encode()
	err := c.do(ctx, c.httpClient, http.MethodGet, path, nil, nil, &out)
	return out.Data, err
}

func (c *restClient) StartCommand(ctx context.Context, id string, request startCommandRequest) (command, error) {
	var started command
	err := c.do(ctx, c.lifecycleClient, http.MethodPost, sandboxPath(id)+"/commands", nil, request, &started)
	return started, err
}

func sandboxPath(id string) string {
	return "/sandboxes/" + url.PathEscape(id)
}

func (c *restClient) do(
	ctx context.Context,
	httpClient *http.Client,
	method, path string,
	headers map[string]string,
	body, out any,
) error {
	requestHeaders := map[string]string{"Authorization": "Bearer " + c.apiKey}
	maps.Copy(requestHeaders, headers)
	response, err := providers.DoHTTPResponse(
		ctx,
		httpClient,
		providers.Groundbox,
		method,
		c.baseURL+path,
		requestHeaders,
		body,
	)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providers.WithResponse(apiErrorFromResponse(response), response)
	}
	if out == nil || len(bytes.TrimSpace(response.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(response.Body, out); err != nil {
		return fmt.Errorf("decode groundbox response: %w", err)
	}
	return nil
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
}

func apiErrorFromResponse(response providers.HTTPResponse) apiError {
	apiErr := apiError{StatusCode: response.StatusCode}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(response.Body, &envelope) == nil {
		apiErr.Code, apiErr.Message = envelope.Error.Code, envelope.Error.Message
	}
	return apiErr
}

func (e apiError) Error() string {
	message := fmt.Sprintf("groundbox API returned HTTP %d", e.StatusCode)
	if e.Code != "" {
		message += " (" + e.Code + ")"
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

func apiStatusCode(err error) int {
	var apiErr apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

func isNotFound(err error) bool {
	return apiStatusCode(err) == http.StatusNotFound
}

func transientAPIError(err error) bool {
	status := apiStatusCode(err)
	return status == 0 || status == http.StatusConflict || status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout || status >= http.StatusInternalServerError
}

func isDialError(err error) bool {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	return errors.As(err, &dnsErr) || errors.As(err, &opErr) && opErr.Op == "dial"
}
