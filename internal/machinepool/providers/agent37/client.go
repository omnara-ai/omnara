package agent37

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const defaultAPIBaseURL = "https://api.agent37.com"

type instanceStatus string

const (
	instanceStatusProvisioning instanceStatus = "provisioning"
	instanceStatusRunning      instanceStatus = "running"
	instanceStatusStopping     instanceStatus = "stopping"
	instanceStatusStopped      instanceStatus = "stopped"
	instanceStatusStarting     instanceStatus = "starting"
	instanceStatusRestarting   instanceStatus = "restarting"
	instanceStatusUpdating     instanceStatus = "updating"
	instanceStatusSleeping     instanceStatus = "sleeping"
	instanceStatusWaking       instanceStatus = "waking"
	instanceStatusFailed       instanceStatus = "failed"
	instanceStatusDeleting     instanceStatus = "deleting"
	instanceStatusDeleted      instanceStatus = "deleted"
)

type instanceResources struct {
	CPU      int `json:"cpu"`
	MemoryGB int `json:"memory"`
	DiskGB   int `json:"disk,omitempty"`
}

type instance struct {
	ID        string            `json:"id"`
	Status    instanceStatus    `json:"status"`
	Resources instanceResources `json:"resources"`
	Metadata  map[string]any    `json:"metadata"`
}

type createInstanceRequest struct {
	Template  string            `json:"template"`
	Name      string            `json:"name"`
	Metadata  map[string]string `json:"metadata"`
	Resources instanceResources `json:"resources"`
}

type execResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type apiClient interface {
	ListTemplates(context.Context) error
	CreateInstance(context.Context, createInstanceRequest) (instance, error)
	ListInstances(context.Context) ([]instance, error)
	GetInstance(context.Context, string) (instance, bool, error)
	StartInstance(context.Context, string) error
	DeleteInstance(context.Context, string) error
	Exec(ctx context.Context, id, command string) (execResult, error)
}

// Create, start, and delete return once the instance has reached the new
// state, and exec returns once the command exits, so they get the provisioning
// budget instead of the short timeout used for reads.
type restClient struct {
	apiBaseURL      string
	apiKey          string
	httpClient      *http.Client
	lifecycleClient *http.Client
}

func newRESTClient(apiBaseURL, apiKey string, httpClient, lifecycleClient *http.Client) *restClient {
	if httpClient == nil {
		httpClient = providers.NewHTTPClient()
	}
	if lifecycleClient == nil {
		lifecycleClient = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{
			DisableResponseHeaderTimeout: true,
			Timeout:                      provisioningTimeout,
		})
	}
	return &restClient{
		apiBaseURL:      strings.TrimRight(apiBaseURL, "/"),
		apiKey:          apiKey,
		httpClient:      httpClient,
		lifecycleClient: lifecycleClient,
	}
}

func (c *restClient) ListTemplates(ctx context.Context) error {
	return c.do(ctx, c.httpClient, http.MethodGet, "/v1/templates", nil, nil)
}

func (c *restClient) CreateInstance(ctx context.Context, request createInstanceRequest) (instance, error) {
	var created instance
	err := c.do(ctx, c.lifecycleClient, http.MethodPost, "/v1/instances", request, &created)
	return created, err
}

func (c *restClient) ListInstances(ctx context.Context) ([]instance, error) {
	var out struct {
		Data []instance `json:"data"`
	}
	err := c.do(ctx, c.httpClient, http.MethodGet, "/v1/instances", nil, &out)
	return out.Data, err
}

func (c *restClient) GetInstance(ctx context.Context, id string) (instance, bool, error) {
	var current instance
	err := c.do(ctx, c.httpClient, http.MethodGet, "/v1/instances/"+url.PathEscape(id), nil, &current)
	if isNotFound(err) {
		return instance{}, false, nil
	}
	return current, err == nil, err
}

func (c *restClient) StartInstance(ctx context.Context, id string) error {
	return c.do(ctx, c.lifecycleClient, http.MethodPost, "/v1/instances/"+url.PathEscape(id)+"/start", nil, nil)
}

func (c *restClient) DeleteInstance(ctx context.Context, id string) error {
	err := c.do(ctx, c.lifecycleClient, http.MethodDelete, "/v1/instances/"+url.PathEscape(id), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) Exec(ctx context.Context, id, command string) (execResult, error) {
	var result execResult
	err := c.do(
		ctx,
		c.lifecycleClient,
		http.MethodPost,
		"/v1/instances/"+url.PathEscape(id)+"/exec",
		map[string]string{"command": command},
		&result,
	)
	return result, err
}

func (c *restClient) do(
	ctx context.Context,
	httpClient *http.Client,
	method, path string,
	body, out any,
) error {
	response, err := providers.DoHTTPResponse(
		ctx,
		httpClient,
		providers.Agent37,
		method,
		c.apiBaseURL+path,
		map[string]string{"Authorization": "Bearer " + c.apiKey},
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
		return fmt.Errorf("decode agent37 response: %w", err)
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
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
	}
	return apiErr
}

func (e apiError) Error() string {
	message := fmt.Sprintf("agent37 API returned HTTP %d", e.StatusCode)
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

// Agent37 answers 409 try_again while a sleep checkpoint is still being
// written or a wake is in flight, and 503 when a region has no room yet.
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
