package superserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	defaultAPIBaseURL  = "https://api.superserve.ai"
	defaultSandboxHost = "sandbox.superserve.ai"
	listPageSize       = 200
	apiKeyHeader       = "X-Api-Key"
	sandboxIDHeader    = "X-Superserve-Sandbox-Id"
	accessTokenHeader  = "X-Access-Token"
	routingHintHeader  = "X-Superserve-Routing-Hint"
)

// Superserve API keys carry their home region, which selects the control plane
// and the shared data-plane host, as the official SDK does.
var regionKeyPattern = regexp.MustCompile(`^ss_live_([a-z0-9]{1,17})_[A-Za-z0-9_-]{32}$`)

type regionEndpoints struct {
	apiBaseURL  string
	sandboxHost string
}

var knownRegions = map[string]regionEndpoints{
	"use": {apiBaseURL: defaultAPIBaseURL, sandboxHost: defaultSandboxHost},
	"usw": {apiBaseURL: "https://api-usw.superserve.ai", sandboxHost: "usw-sandbox.superserve.ai"},
}

func endpointsForAPIKey(apiKey string) regionEndpoints {
	if match := regionKeyPattern.FindStringSubmatch(apiKey); match != nil {
		if endpoints, ok := knownRegions[match[1]]; ok {
			return endpoints
		}
	}
	return knownRegions["use"]
}

type sandboxStatus string

const (
	sandboxStatusStarting  sandboxStatus = "starting"
	sandboxStatusActive    sandboxStatus = "active"
	sandboxStatusPausing   sandboxStatus = "pausing"
	sandboxStatusPaused    sandboxStatus = "paused"
	sandboxStatusResuming  sandboxStatus = "resuming"
	sandboxStatusMigrating sandboxStatus = "migrating"
	sandboxStatusFailed    sandboxStatus = "failed"
	sandboxStatusDeleted   sandboxStatus = "deleted"
)

const templateStatusReady = "ready"

type template struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	VCPU      int    `json:"vcpu"`
	MemoryMiB int    `json:"memory_mib"`
}

type sandbox struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Status      sandboxStatus     `json:"status"`
	VCPUCount   int               `json:"vcpu_count"`
	MemoryMiB   int               `json:"memory_mib"`
	CreatedAt   time.Time         `json:"created_at"`
	Metadata    map[string]string `json:"metadata"`
	AccessToken string            `json:"access_token"`
	RoutingHint string            `json:"routing_hint"`
}

type createSandboxRequest struct {
	Name         string            `json:"name"`
	FromTemplate string            `json:"from_template,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// execSession carries the data-plane credentials that activate returns.
type execSession struct {
	SandboxID   string
	AccessToken string
	RoutingHint string
}

type execRequest struct {
	Command        string            `json:"command"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeout_s,omitempty"`
}

type execResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

type apiClient interface {
	ListTemplates(context.Context, string) ([]template, error)
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	ListSandboxes(context.Context, map[string]string) ([]sandbox, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	ActivateSandbox(context.Context, string) (sandbox, error)
	DeleteSandbox(context.Context, string) error
	Exec(context.Context, execSession, execRequest) (execResult, error)
}

type restClient struct {
	apiBaseURL  string
	sandboxHost string
	apiKey      string
	httpClient  *http.Client
}

func newRESTClient(endpoints regionEndpoints, apiKey string, httpClient *http.Client) *restClient {
	if httpClient == nil {
		httpClient = providers.NewHTTPClient()
	}
	return &restClient{
		apiBaseURL:  strings.TrimRight(endpoints.apiBaseURL, "/"),
		sandboxHost: endpoints.sandboxHost,
		apiKey:      apiKey,
		httpClient:  httpClient,
	}
}

func (c *restClient) ListTemplates(ctx context.Context, query string) ([]template, error) {
	values := url.Values{"q": {query}, "limit": {strconv.Itoa(listPageSize)}}
	var templates []template
	if err := c.control(ctx, http.MethodGet, "/templates?"+values.Encode(), nil, listDecoder(&templates)); err != nil {
		return nil, err
	}
	return templates, nil
}

func (c *restClient) CreateSandbox(ctx context.Context, request createSandboxRequest) (sandbox, error) {
	var created sandbox
	err := c.control(ctx, http.MethodPost, "/sandboxes", request, jsonDecoder(&created))
	return created, err
}

func (c *restClient) ListSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error) {
	var sandboxes []sandbox
	for offset := 0; ; offset += listPageSize {
		values := url.Values{
			"limit":  {strconv.Itoa(listPageSize)},
			"offset": {strconv.Itoa(offset)},
		}
		for key, value := range metadata {
			values.Set("metadata."+key, value)
		}
		var page []sandbox
		if err := c.control(ctx, http.MethodGet, "/sandboxes?"+values.Encode(), nil, listDecoder(&page)); err != nil {
			return nil, err
		}
		sandboxes = append(sandboxes, page...)
		if len(page) < listPageSize {
			return sandboxes, nil
		}
	}
}

func (c *restClient) GetSandbox(ctx context.Context, id string) (sandbox, bool, error) {
	var current sandbox
	err := c.control(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(id), nil, jsonDecoder(&current))
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	return current, err == nil, err
}

func (c *restClient) ActivateSandbox(ctx context.Context, id string) (sandbox, error) {
	var activated sandbox
	err := c.control(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(id)+"/activate", nil, jsonDecoder(&activated))
	return activated, err
}

func (c *restClient) DeleteSandbox(ctx context.Context, id string) error {
	err := c.control(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(id), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) Exec(ctx context.Context, session execSession, request execRequest) (execResult, error) {
	headers := map[string]string{
		sandboxIDHeader:   session.SandboxID,
		accessTokenHeader: session.AccessToken,
	}
	if session.RoutingHint != "" {
		headers[routingHintHeader] = session.RoutingHint
	}
	var result execResult
	err := c.do(ctx, http.MethodPost, "https://"+c.sandboxHost+"/exec", headers, request, jsonDecoder(&result))
	return result, err
}

func (c *restClient) control(
	ctx context.Context,
	method, path string,
	body any,
	decode func([]byte) error,
) error {
	return c.do(ctx, method, c.apiBaseURL+path, map[string]string{apiKeyHeader: c.apiKey}, body, decode)
}

func (c *restClient) do(
	ctx context.Context,
	method, requestURL string,
	headers map[string]string,
	body any,
	decode func([]byte) error,
) error {
	response, err := providers.DoHTTPResponse(ctx, c.httpClient, providers.Superserve, method, requestURL, headers, body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providers.WithResponse(apiErrorFromResponse(response), response)
	}
	if decode == nil || len(bytes.TrimSpace(response.Body)) == 0 {
		return nil
	}
	if err := decode(response.Body); err != nil {
		return fmt.Errorf("decode superserve response: %w", err)
	}
	return nil
}

func jsonDecoder(out any) func([]byte) error {
	return func(body []byte) error { return json.Unmarshal(body, out) }
}

// listDecoder accepts both the bare array the API returns today and the
// documented {"items": [...]} envelope.
func listDecoder[T any](out *[]T) func([]byte) error {
	return func(body []byte) error {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			return json.Unmarshal(trimmed, out)
		}
		var envelope struct {
			Items []T `json:"items"`
		}
		if err := json.Unmarshal(trimmed, &envelope); err != nil {
			return err
		}
		*out = envelope.Items
		return nil
	}
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
	message := fmt.Sprintf("superserve API returned HTTP %d", e.StatusCode)
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

// transientAPIError reports failures worth retrying: transport errors, rate
// limits, capacity limits, state conflicts, and server errors.
func transientAPIError(err error) bool {
	status := apiStatusCode(err)
	return status == 0 || status == http.StatusConflict || status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout || status >= http.StatusInternalServerError
}

// sandboxUnavailable reports data-plane failures that a fresh activate can fix:
// a paused sandbox or an expired access token.
func sandboxUnavailable(err error) bool {
	status := apiStatusCode(err)
	return status == http.StatusUnauthorized || status == http.StatusServiceUnavailable
}
