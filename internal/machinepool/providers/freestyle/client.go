package freestyle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

type apiClient interface {
	CreateVM(context.Context, createVMRequest) (vm, error)
	GetVM(context.Context, string) (vm, bool, error)
	ResizeVM(context.Context, string, resizeVMRequest) (vm, error)
	StartVM(context.Context, string) (vm, error)
	DeleteVM(context.Context, string) error
	ExecVM(context.Context, string, execVMRequest) (execVMResponse, error)
}

type createVMRequest struct {
	SnapshotID         string            `json:"snapshotId"`
	Slug               string            `json:"slug"`
	DisplayName        string            `json:"displayName"`
	IdleTimeoutSeconds *int              `json:"idleTimeoutSeconds,omitempty"`
	AutoDeleteSeconds  int               `json:"autoDeleteSeconds"`
	AutomaticRestart   bool              `json:"automaticRestart"`
	Metadata           map[string]string `json:"metadata"`
	Firewall           firewallSpec      `json:"firewall"`
}

type firewallSpec struct {
	Rules []firewallRule `json:"rules"`
}

type firewallRule struct {
	Action      string           `json:"action"`
	Source      firewallEndpoint `json:"source"`
	Destination firewallEndpoint `json:"destination"`
}

type firewallEndpoint struct {
	Public bool `json:"public,omitempty"`
}

type resizeVMRequest struct {
	CPU      int `json:"cpu,omitempty"`
	MemoryMB int `json:"memory,omitempty"`
}

type execVMRequest struct {
	Command   string `json:"command"`
	LinuxUser string `json:"linuxUser"`
	TimeoutMS int    `json:"timeoutMs"`
}

type execVMResponse struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	StatusCode *int   `json:"statusCode"`
}

type vm struct {
	ID                         string            `json:"id"`
	State                      string            `json:"state"`
	Slug                       string            `json:"slug"`
	SnapshotID                 string            `json:"snapshotId"`
	SourceSnapshotSlugAtCreate string            `json:"sourceSnapshotSlugAtCreate"`
	Resources                  vmResources       `json:"resources"`
	Metadata                   map[string]string `json:"metadata"`
}

type vmResources struct {
	CPU      int `json:"cpu"`
	MemoryMB int `json:"memory"`
	Storage  int `json:"storage"`
}

type apiError struct {
	StatusCode int
	Code       string
}

func (e apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("freestyle API request returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("freestyle API request returned HTTP %d (%s)", e.StatusCode, e.Code)
}

func isNotFound(err error) bool {
	apiErr, ok := err.(apiError)
	return ok && apiErr.StatusCode == http.StatusNotFound
}

func isConflict(err error) bool {
	apiErr, ok := err.(apiError)
	return ok && apiErr.StatusCode == http.StatusConflict
}

type restClient struct {
	apiBaseURL string
	apiToken   string
	httpClient *http.Client
}

func newRESTClient(baseURL, token string, httpClient *http.Client) *restClient {
	if httpClient == nil {
		httpClient = providers.NewHTTPClient()
	}
	return &restClient{
		apiBaseURL: strings.TrimRight(baseURL, "/"),
		apiToken:   token,
		httpClient: httpClient,
	}
}

func (c *restClient) CreateVM(ctx context.Context, request createVMRequest) (vm, error) {
	var response vm
	err := c.doRequest(ctx, http.MethodPost, c.apiBaseURL+"/v5/vms", request, &response)
	return response, err
}

func (c *restClient) GetVM(ctx context.Context, idOrSlug string) (vm, bool, error) {
	var response vm
	err := c.doRequest(
		ctx,
		http.MethodGet,
		c.apiBaseURL+"/v5/vms/"+url.PathEscape(idOrSlug),
		nil,
		&response,
	)
	if isNotFound(err) {
		return vm{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) ResizeVM(
	ctx context.Context,
	id string,
	request resizeVMRequest,
) (vm, error) {
	var response vm
	err := c.doRequest(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/v5/vms/"+url.PathEscape(id)+"/resize",
		request,
		&response,
	)
	return response, err
}

func (c *restClient) StartVM(ctx context.Context, id string) (vm, error) {
	var response vm
	err := c.doRequest(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/v5/vms/"+url.PathEscape(id)+"/start",
		nil,
		&response,
	)
	return response, err
}

func (c *restClient) DeleteVM(ctx context.Context, id string) error {
	err := c.doRequest(
		ctx,
		http.MethodDelete,
		c.apiBaseURL+"/v5/vms/"+url.PathEscape(id),
		nil,
		nil,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) ExecVM(
	ctx context.Context,
	id string,
	request execVMRequest,
) (execVMResponse, error) {
	var response execVMResponse
	err := c.doRequest(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/v5/vms/"+url.PathEscape(id)+"/exec-await",
		request,
		&response,
	)
	return response, err
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, requestURL string,
	body any,
	out any,
) error {
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		"freestyle",
		method,
		requestURL,
		map[string]string{"Authorization": "Bearer " + c.apiToken},
		body,
	)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var errorBody struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(response.Body, &errorBody)
		return providers.WithRetryAfter(apiError{
			StatusCode: response.StatusCode,
			Code:       strings.TrimSpace(errorBody.Code),
		}, response.Header)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(response.Body, out); err != nil {
		return fmt.Errorf("decode freestyle response: %w", err)
	}
	return nil
}
