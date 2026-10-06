package createos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

type apiClient interface {
	ListShapes(context.Context) ([]sandboxShape, error)
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	ListSandboxes(context.Context, sandboxStatus, int, int) ([]sandbox, int, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	ResumeSandbox(context.Context, string) error
	ListProcesses(context.Context, string) ([]process, error)
	CreateProcess(context.Context, string, commandRequest) (process, error)
	Exec(context.Context, string, commandRequest) error
}

type sandboxShape struct {
	ID     string `json:"id"`
	VCPU   int    `json:"vcpu"`
	MemMiB int    `json:"mem_mib"`
}

type sandboxStatus string

const (
	sandboxStatusCreating   sandboxStatus = "creating"
	sandboxStatusRunning    sandboxStatus = "running"
	sandboxStatusDestroyed  sandboxStatus = "destroyed"
	sandboxStatusFailed     sandboxStatus = "failed"
	sandboxStatusPausing    sandboxStatus = "pausing"
	sandboxStatusPaused     sandboxStatus = "paused"
	sandboxStatusResuming   sandboxStatus = "resuming"
	sandboxStatusForking    sandboxStatus = "forking"
	sandboxStatusDestroying sandboxStatus = "destroying"
	sandboxStatusError      sandboxStatus = "error"
)

type sandbox struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Status sandboxStatus `json:"status"`
	Shape  string        `json:"shape"`
}

type createSandboxRequest struct {
	Shape  string            `json:"shape"`
	RootFS string            `json:"rootfs"`
	Name   string            `json:"name"`
	Envs   map[string]string `json:"envs"`
}

const (
	processStateStarting = "starting"
	processStateRunning  = "running"
)

type process struct {
	ID           string   `json:"process_id"`
	Command      string   `json:"cmd"`
	Args         []string `json:"args"`
	State        string   `json:"state"`
	LeaderExited bool     `json:"leader_exited"`
}

type commandRequest struct {
	Command string   `json:"cmd"`
	Args    []string `json:"args,omitempty"`
}

type restClient struct {
	baseURL, token string
	httpClient     *http.Client
}

func newRESTClient(baseURL, token string, client *http.Client) *restClient {
	if client == nil {
		client = providers.NewHTTPClient()
	}
	return &restClient{baseURL: baseURL, token: token, httpClient: client}
}

func (c *restClient) ListShapes(ctx context.Context) ([]sandboxShape, error) {
	var out struct {
		Data []sandboxShape `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/shapes", nil, &out)
	return out.Data, err
}

func (c *restClient) CreateSandbox(ctx context.Context, input createSandboxRequest) (sandbox, error) {
	var out sandbox
	err := c.do(ctx, http.MethodPost, "/v1/sandboxes", input, &out)
	return out, err
}

func (c *restClient) ListSandboxes(
	ctx context.Context,
	status sandboxStatus,
	limit, offset int,
) ([]sandbox, int, error) {
	values := url.Values{
		"status": {string(status)},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var out struct {
		Data       []sandbox `json:"data"`
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/sandboxes?"+values.Encode(), nil, &out)
	return out.Data, out.Pagination.Total, err
}

func (c *restClient) GetSandbox(ctx context.Context, id string) (sandbox, bool, error) {
	var out sandbox
	err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+url.PathEscape(id), nil, &out)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	return out, err == nil, err
}

func (c *restClient) DeleteSandbox(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, "/v1/sandboxes/"+url.PathEscape(id), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) ResumeSandbox(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/sandboxes/"+url.PathEscape(id)+"/resume", nil, nil)
}

func (c *restClient) ListProcesses(ctx context.Context, id string) ([]process, error) {
	var out struct {
		Processes []process `json:"processes"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+url.PathEscape(id)+"/processes", nil, &out)
	return out.Processes, err
}

func (c *restClient) CreateProcess(ctx context.Context, id string, input commandRequest) (process, error) {
	var out process
	err := c.do(ctx, http.MethodPost, "/v1/sandboxes/"+url.PathEscape(id)+"/processes", input, &out)
	return out, err
}

func (c *restClient) Exec(ctx context.Context, id string, input commandRequest) error {
	var out struct {
		Result struct {
			ExitCode *int   `json:"exit_code"`
			Error    string `json:"error"`
		} `json:"result"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/sandboxes/"+url.PathEscape(id)+"/exec", input, &out)
	if err == nil && (out.Result.ExitCode == nil || *out.Result.ExitCode != 0 || out.Result.Error != "") {
		err = errors.New("createos command did not exit 0")
	}
	return err
}

func (c *restClient) do(ctx context.Context, method, path string, body, out any) error {
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.CreateOS,
		method,
		c.baseURL+path,
		map[string]string{"X-Api-Key": c.token},
		body,
	)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providers.WithRetryAfter(apiError{StatusCode: response.StatusCode}, response.Header)
	}
	if out == nil || len(response.Body) == 0 {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return fmt.Errorf("decode createos response: %w", err)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode createos response data: %w", err)
	}
	return nil
}

type apiError struct {
	StatusCode int
}

func (e apiError) Error() string {
	return fmt.Sprintf("createos API returned HTTP %d", e.StatusCode)
}

func apiStatusCode(err error) int {
	var apiErr apiError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

func transientAPIError(err error) bool {
	status := apiStatusCode(err)
	return status == 0 || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func isNotFound(err error) bool {
	return apiStatusCode(err) == http.StatusNotFound
}
