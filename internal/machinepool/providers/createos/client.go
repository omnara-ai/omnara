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
	ListShapes(context.Context) ([]Shape, error)
	ListRootFS(context.Context) (RootFSCatalog, error)
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	ListSandboxes(context.Context, int, int) ([]sandbox, int, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	ListProcesses(context.Context, string) ([]process, error)
	CreateProcess(context.Context, string, createProcessRequest) (process, error)
}

type Shape struct {
	ID     string `json:"id"`
	VCPU   int    `json:"vcpu"`
	MemMiB int    `json:"mem_mib"`
}

type RootFS struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty"`
	Successor   *string `json:"successor,omitempty"`
}

type RootFSCatalog struct {
	Names   []string `json:"rootfs"`
	Default string   `json:"default"`
	Entries []RootFS `json:"entries,omitempty"`
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
	RootFS string        `json:"rootfs"`
	Region string        `json:"region"`
}

type createSandboxRequest struct {
	Shape  string            `json:"shape"`
	RootFS string            `json:"rootfs,omitempty"`
	Name   string            `json:"name"`
	Region string            `json:"region,omitempty"`
	Envs   map[string]string `json:"envs"`
}

type process struct {
	ID           string `json:"process_id"`
	State        string `json:"state"`
	LeaderExited bool   `json:"leader_exited"`
}

type createProcessRequest struct {
	Command string            `json:"cmd"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
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

func (c *restClient) ListShapes(ctx context.Context) ([]Shape, error) {
	var out struct {
		Data []Shape `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/shapes", nil, &out)
	return out.Data, err
}

func ListShapes(ctx context.Context, token string) ([]Shape, error) {
	return newRESTClient(defaultAPIBaseURL, token, nil).ListShapes(ctx)
}

func (c *restClient) ListRootFS(ctx context.Context) (RootFSCatalog, error) {
	var out RootFSCatalog
	err := c.do(ctx, http.MethodGet, "/v1/rootfs", nil, &out)
	return out, err
}

func ListRootFS(ctx context.Context, token string) (RootFSCatalog, error) {
	return newRESTClient(defaultAPIBaseURL, token, nil).ListRootFS(ctx)
}

func (c *restClient) CreateSandbox(ctx context.Context, input createSandboxRequest) (sandbox, error) {
	var out sandbox
	err := c.do(ctx, http.MethodPost, "/v1/sandboxes", input, &out)
	return out, err
}

func (c *restClient) ListSandboxes(ctx context.Context, limit, offset int) ([]sandbox, int, error) {
	values := url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}}
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

func (c *restClient) ListProcesses(ctx context.Context, id string) ([]process, error) {
	var out struct {
		Processes []process `json:"processes"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+url.PathEscape(id)+"/processes", nil, &out)
	return out.Processes, err
}

func (c *restClient) CreateProcess(ctx context.Context, id string, input createProcessRequest) (process, error) {
	var out process
	err := c.do(ctx, http.MethodPost, "/v1/sandboxes/"+url.PathEscape(id)+"/processes", input, &out)
	return out, err
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
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return fmt.Errorf("decode createos response: %w", err)
	}
	// Managed-process endpoints return their resource directly.
	raw := response.Body
	if envelope.Status != "" {
		raw = envelope.Data
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode createos response data: %w", err)
	}
	return nil
}

type apiError struct{ StatusCode int }

func (e apiError) Error() string { return fmt.Sprintf("createos API returned HTTP %d", e.StatusCode) }
func isNotFound(err error) bool {
	var e apiError
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}
