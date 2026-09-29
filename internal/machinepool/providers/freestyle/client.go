package freestyle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

type apiClient interface {
	CreateVM(context.Context, createVMRequest) (vm, error)
	GetVM(context.Context, string) (vm, bool, error)
	ListVMs(context.Context, string, int, int) (vmList, error)
	ResizeVM(context.Context, string, resizeVMRequest) error
	StartVM(context.Context, string) (vm, error)
	DeleteVM(context.Context, string) error
	ExecVM(context.Context, string, execVMRequest) (execVMResponse, error)
}

type createVMRequest struct {
	SnapshotID         string            `json:"snapshotId"`
	Slug               string            `json:"slug"`
	DisplayName        string            `json:"displayName"`
	IdleTimeoutSeconds int               `json:"idleTimeoutSeconds,omitempty"`
	AutoDeleteSeconds  int               `json:"autoDeleteSeconds"`
	AutomaticRestart   bool              `json:"automaticRestart"`
	Metadata           map[string]string `json:"metadata"`
	Firewall           firewallSpec      `json:"firewall"`
	TLS                *tlsSpec          `json:"tls,omitempty"`
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

type tlsSpec struct {
	Rules []tlsRule `json:"rules"`
}

type tlsRule struct {
	Action      string      `json:"action"`
	Domain      string      `json:"domain"`
	Protocol    string      `json:"protocol"`
	Source      tlsEndpoint `json:"source"`
	Destination tlsEndpoint `json:"destination"`
}

type tlsEndpoint struct {
	Public bool `json:"public,omitempty"`
	Port   int  `json:"port,omitempty"`
}

type resizeVMRequest struct {
	CPU      int `json:"cpu,omitempty"`
	MemoryMB int `json:"memory,omitempty"`
}

type execVMRequest struct {
	Command   string `json:"command"`
	Stdin     string `json:"stdin,omitempty"`
	LinuxUser string `json:"linuxUser"`
	TimeoutMS int    `json:"timeoutMs"`
}

type execVMResponse struct {
	StatusCode *int `json:"statusCode"`
}

type vm struct {
	ID        string            `json:"id"`
	State     string            `json:"state"`
	Slug      string            `json:"slug"`
	Resources vmResources       `json:"resources"`
	Metadata  map[string]string `json:"metadata"`
}

type vmList struct {
	VMs        []vm `json:"vms"`
	TotalCount int  `json:"totalCount"`
}

type vmResources struct {
	CPU      int `json:"cpu"`
	MemoryMB int `json:"memory"`
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
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
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
	err := c.doRequest(ctx, http.MethodPost, c.apiBaseURL+"/vms", request, &response)
	return response, err
}

func (c *restClient) GetVM(ctx context.Context, idOrSlug string) (vm, bool, error) {
	var response vm
	err := c.doRequest(
		ctx,
		http.MethodGet,
		c.apiBaseURL+"/vms/"+url.PathEscape(idOrSlug),
		nil,
		&response,
	)
	if isNotFound(err) {
		return vm{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) ListVMs(
	ctx context.Context,
	metadata string,
	limit, offset int,
) (vmList, error) {
	query := url.Values{}
	query.Set("metadata", metadata)
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	var response vmList
	err := c.doRequest(ctx, http.MethodGet, c.apiBaseURL+"/vms?"+query.Encode(), nil, &response)
	return response, err
}

func (c *restClient) ResizeVM(ctx context.Context, id string, request resizeVMRequest) error {
	return c.doRequest(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/vms/"+url.PathEscape(id)+"/resize",
		request,
		nil,
	)
}

func (c *restClient) StartVM(ctx context.Context, id string) (vm, error) {
	var response vm
	err := c.doRequest(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/vms/"+url.PathEscape(id)+"/start",
		nil,
		&response,
	)
	return response, err
}

func (c *restClient) DeleteVM(ctx context.Context, id string) error {
	err := c.doRequest(
		ctx,
		http.MethodDelete,
		c.apiBaseURL+"/vms/"+url.PathEscape(id),
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
		c.apiBaseURL+"/vms/"+url.PathEscape(id)+"/exec-await",
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
		providers.Freestyle,
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
