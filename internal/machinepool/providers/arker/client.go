package arker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

type forkRequest struct {
	SourceVMName string    `json:"source_vm_name"`
	Name         string    `json:"name"`
	Resources    resources `json:"resources"`
}

type resources struct {
	VCPU      int `json:"vcpu"`
	MemoryMiB int `json:"memory_mib"`
}

type vm struct {
	ID    string `json:"vm_id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type runRequest struct {
	Command          string `json:"command"`
	SessionID        string `json:"session_id,omitempty"`
	SessionIdx       *int   `json:"session_idx,omitempty"`
	TimeToBackground int    `json:"time_to_background"`
}

type run struct {
	Command string `json:"command"`
	State   string `json:"state"`
}

type restClient struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

func (c *restClient) Fork(ctx context.Context, request forkRequest, idempotencyKey string) (vm, error) {
	var response vm
	err := c.doRequest(
		ctx,
		http.MethodPost,
		"/v1/fork",
		map[string]string{"Idempotency-Key": idempotencyKey},
		request,
		&response,
	)
	return response, err
}

func (c *restClient) GetVM(ctx context.Context, idOrName string) (vm, bool, error) {
	var response vm
	err := c.doRequest(ctx, http.MethodGet, vmPath(idOrName), nil, nil, &response)
	if isNotFound(err) {
		return vm{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) DeleteVM(ctx context.Context, id string) error {
	err := c.doRequest(ctx, http.MethodDelete, vmPath(id), nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) CreateSession(ctx context.Context, vmID string, env map[string]string) (string, error) {
	var response struct {
		SessionID string `json:"session_id"`
	}
	err := c.doRequest(
		ctx,
		http.MethodPost,
		vmPath(vmID)+"/sessions",
		nil,
		map[string]any{"env": env},
		&response,
	)
	return response.SessionID, err
}

func (c *restClient) StartRun(ctx context.Context, vmID string, request runRequest) (run, error) {
	var response run
	err := c.doRequest(ctx, http.MethodPost, vmPath(vmID)+"/runs", nil, request, &response)
	return response, err
}

func (c *restClient) ListRuns(ctx context.Context, vmID string) ([]run, error) {
	var response struct {
		Runs []run `json:"runs"`
	}
	err := c.doRequest(ctx, http.MethodGet, vmPath(vmID)+"/runs?limit=100", nil, nil, &response)
	return response.Runs, err
}

func vmPath(idOrName string) string {
	return "/v1/vms/" + url.PathEscape(idOrName)
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, path string,
	headers map[string]string,
	body, out any,
) error {
	requestHeaders := map[string]string{"Authorization": "Bearer " + c.apiToken}
	maps.Copy(requestHeaders, headers)
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.Arker,
		method,
		c.baseURL+path,
		requestHeaders,
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
	if err := json.Unmarshal(response.Body, out); err != nil {
		return fmt.Errorf("decode arker response: %w", err)
	}
	return nil
}

type apiError struct {
	StatusCode int
}

func (e apiError) Error() string {
	return fmt.Sprintf("arker API returned HTTP %d", e.StatusCode)
}

func isNotFound(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}
