package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const apiVersion = "v1.44"

type apiClient interface {
	Ping(context.Context) error
	CreateNetwork(context.Context, networkCreateRequest) error
	InspectNetwork(context.Context, string) (network, bool, error)
	RemoveNetwork(context.Context, string) error
	CreateContainer(context.Context, string, containerCreateRequest) (containerCreateResponse, error)
	InspectContainer(context.Context, string) (container, bool, error)
	StartContainer(context.Context, string) error
	RemoveContainer(context.Context, string) error
	ListContainers(context.Context, map[string][]string) ([]containerSummary, error)
	PullImage(context.Context, string) error
}

type networkCreateRequest struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels"`
}

type network struct {
	Labels map[string]string `json:"Labels"`
}

type containerCreateRequest struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	NanoCPUs    int64     `json:"NanoCpus"`
	Memory      int64     `json:"Memory"`
	MemorySwap  int64     `json:"MemorySwap"`
	PidsLimit   int64     `json:"PidsLimit"`
	Init        bool      `json:"Init"`
	CapDrop     []string  `json:"CapDrop,omitempty"`
	LogConfig   logConfig `json:"LogConfig"`
	Runtime     string    `json:"Runtime,omitempty"`
	NetworkMode string    `json:"NetworkMode,omitempty"`
}

type logConfig struct {
	Type string `json:"Type"`
}

type containerCreateResponse struct {
	Warnings []string `json:"Warnings"`
}

type container struct {
	State      containerState  `json:"State"`
	Config     containerConfig `json:"Config"`
	HostConfig hostConfig      `json:"HostConfig"`
}

type containerState struct {
	Status string `json:"Status"`
}

type containerConfig struct {
	Labels map[string]string `json:"Labels"`
}

type containerSummary struct {
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
}

type restClient struct {
	baseURL    string
	httpClient *http.Client
}

func (c *restClient) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/_ping", nil, nil, nil)
}

func (c *restClient) CreateNetwork(ctx context.Context, request networkCreateRequest) error {
	return c.do(ctx, http.MethodPost, "/networks/create", nil, request, nil)
}

func (c *restClient) InspectNetwork(ctx context.Context, name string) (network, bool, error) {
	var response network
	err := c.do(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil, &response)
	if isNotFound(err) {
		return network{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) RemoveNetwork(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodDelete, "/networks/"+url.PathEscape(name), nil, nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) CreateContainer(
	ctx context.Context,
	name string,
	request containerCreateRequest,
) (containerCreateResponse, error) {
	var response containerCreateResponse
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, request, &response)
	return response, err
}

func (c *restClient) InspectContainer(ctx context.Context, name string) (container, bool, error) {
	var response container
	err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, nil, &response)
	if isNotFound(err) {
		return container{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) StartContainer(ctx context.Context, name string) error {
	err := c.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(name)+"/start", nil, nil, nil)
	if isStatus(err, http.StatusNotModified) {
		return nil
	}
	return err
}

func (c *restClient) RemoveContainer(ctx context.Context, name string) error {
	err := c.do(
		ctx,
		http.MethodDelete,
		"/containers/"+url.PathEscape(name),
		url.Values{"force": {"true"}, "v": {"true"}},
		nil,
		nil,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) ListContainers(
	ctx context.Context,
	filters map[string][]string,
) ([]containerSummary, error) {
	encodedFilters, err := json.Marshal(filters)
	if err != nil {
		return nil, fmt.Errorf("encode docker container filters: %w", err)
	}
	var response []containerSummary
	err = c.do(
		ctx,
		http.MethodGet,
		"/containers/json",
		url.Values{"all": {"true"}, "filters": {string(encodedFilters)}},
		nil,
		&response,
	)
	return response, err
}

func (c *restClient) PullImage(ctx context.Context, image string) error {
	query := url.Values{"fromImage": {image}}
	if !strings.Contains(image[strings.LastIndex(image, "/")+1:], ":") {
		query.Set("tag", "latest")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/images/create?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("build docker pull request: %w", err)
	}
	client := *c.httpClient
	client.Timeout = 0
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("docker request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var message struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&message)
		return providers.WithResponse(
			apiError{StatusCode: response.StatusCode, Message: message.Message},
			providers.HTTPResponse{StatusCode: response.StatusCode, Header: response.Header},
		)
	}
	decoder := json.NewDecoder(response.Body)
	for {
		var progress struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&progress); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read docker pull progress: %w", err)
		}
		if progress.Error != "" {
			return errors.New(progress.Error)
		}
	}
}

func (c *restClient) do(
	ctx context.Context,
	method, path string,
	query url.Values,
	body, out any,
) error {
	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	response, err := providers.DoHTTPResponse(ctx, c.httpClient, providers.Docker, method, requestURL, nil, body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var message struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(response.Body, &message)
		return providers.WithResponse(apiError{StatusCode: response.StatusCode, Message: message.Message}, response)
	}
	if out == nil || len(response.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(response.Body, out); err != nil {
		return fmt.Errorf("decode docker response: %w", err)
	}
	return nil
}

type apiError struct {
	StatusCode int
	Message    string
}

func (e apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("docker API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("docker API returned HTTP %d: %s", e.StatusCode, e.Message)
}

func isStatus(err error, statusCode int) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

func isNotFound(err error) bool {
	return isStatus(err, http.StatusNotFound)
}

func isClientError(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

func isConflict(err error) bool {
	return isStatus(err, http.StatusConflict)
}
