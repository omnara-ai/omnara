package maritime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	defaultAPIBaseURL   = "https://api.maritime.sh/v1"
	authorizationHeader = "Authorization"
	dataPlaneHeader     = "X-Maritime-Token"
	maxListPages        = 10
)

type sandboxState string

const (
	sandboxStateStarting sandboxState = "starting"
	sandboxStateRunning  sandboxState = "running"
	sandboxStatePausing  sandboxState = "pausing"
	sandboxStatePaused   sandboxState = "paused"
	sandboxStateResuming sandboxState = "resuming"
	sandboxStateError    sandboxState = "error"
	sandboxStateEnded    sandboxState = "ended"
)

type sandboxResources struct {
	VCPU      int `json:"vcpu"`
	MemoryMiB int `json:"memoryMib"`
	DiskGiB   int `json:"diskGib"`
}

type sandbox struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	State        sandboxState      `json:"state"`
	Resources    sandboxResources  `json:"resources"`
	DataPlaneURL string            `json:"dataPlaneUrl"`
	Secret       string            `json:"secret"`
	Metadata     map[string]string `json:"metadata"`
	EndedReason  string            `json:"endedReason"`
}

type createSandboxRequest struct {
	Name      string            `json:"name"`
	From      string            `json:"from,omitempty"`
	VCPU      int               `json:"vcpu,omitempty"`
	MemoryMiB int               `json:"memoryMib,omitempty"`
	DiskGiB   int               `json:"diskGib,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	KeepAwake bool              `json:"keepAwake"`
}

type sandboxPage struct {
	Data       []sandbox `json:"data"`
	NextCursor string    `json:"nextCursor"`
}

type runRequest struct {
	Cmd            []string          `json:"cmd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

type command struct {
	State    string `json:"state"`
	ExitCode *int   `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type apiClient interface {
	CheckConnection(context.Context) error
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	ListSandboxes(context.Context, map[string]string) ([]sandbox, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	Run(context.Context, sandbox, runRequest) (command, error)
}

type restClient struct {
	apiBaseURL string
	apiKey     string
	httpClient *http.Client
}

func newRESTClient(apiBaseURL, apiKey string, httpClient *http.Client) *restClient {
	return &restClient{
		apiBaseURL: strings.TrimRight(apiBaseURL, "/"),
		apiKey:     apiKey,
		httpClient: httpClient,
	}
}

func (c *restClient) CheckConnection(ctx context.Context) error {
	return c.control(ctx, http.MethodGet, "/sandboxes?limit=1", nil, &sandboxPage{})
}

func (c *restClient) CreateSandbox(ctx context.Context, request createSandboxRequest) (sandbox, error) {
	var created sandbox
	err := c.control(ctx, http.MethodPost, "/sandboxes", request, &created)
	return created, err
}

func (c *restClient) ListSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error) {
	var sandboxes []sandbox
	cursor := ""
	for range maxListPages {
		values := url.Values{}
		for key, value := range metadata {
			values.Set("metadata["+key+"]", value)
		}
		if cursor != "" {
			values.Set("cursor", cursor)
		}
		var page sandboxPage
		if err := c.control(ctx, http.MethodGet, "/sandboxes?"+values.Encode(), nil, &page); err != nil {
			return nil, err
		}
		sandboxes = append(sandboxes, page.Data...)
		if page.NextCursor == "" {
			return sandboxes, nil
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("maritime sandbox list has more than %d pages for one Omnara machine", maxListPages)
}

func (c *restClient) GetSandbox(ctx context.Context, id string) (sandbox, bool, error) {
	var current sandbox
	err := c.control(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(id), nil, &current)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	return current, err == nil, err
}

func (c *restClient) DeleteSandbox(ctx context.Context, id string) error {
	err := c.control(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(id), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

// Run starts a foreground command on the sandbox's data plane, which answers
// on its own host and takes the sandbox secret instead of the account key.
func (c *restClient) Run(ctx context.Context, target sandbox, request runRequest) (command, error) {
	dataPlaneURL, err := validateDataPlaneURL(target.DataPlaneURL)
	if err != nil {
		return command{}, err
	}
	if target.Secret == "" {
		return command{}, fmt.Errorf("maritime sandbox %q has no data plane secret", target.ID)
	}
	var result command
	err = c.do(
		ctx,
		http.MethodPost,
		dataPlaneURL+"/commands",
		map[string]string{dataPlaneHeader: target.Secret},
		request,
		&result,
	)
	return result, err
}

func validateDataPlaneURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || !providers.IsHTTPS(parsed) || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("maritime sandbox has an invalid data plane URL")
	}
	return parsed.String(), nil
}

func (c *restClient) control(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, c.apiBaseURL+path, map[string]string{authorizationHeader: "Bearer " + c.apiKey}, body, out)
}

func (c *restClient) do(
	ctx context.Context,
	method, requestURL string,
	headers map[string]string,
	body, out any,
) error {
	response, err := providers.DoHTTPResponse(ctx, c.httpClient, providers.Maritime, method, requestURL, headers, body)
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
		return fmt.Errorf("decode maritime response: %w", err)
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
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(response.Body, &body) == nil {
		apiErr.Code = body.Code
		apiErr.Message = body.Message
	}
	return apiErr
}

func (e apiError) Error() string {
	message := fmt.Sprintf("maritime API returned HTTP %d", e.StatusCode)
	if e.Code != "" {
		message += " (" + e.Code + ")"
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

func apiErrorOf(err error) (apiError, bool) {
	var apiErr apiError
	ok := errors.As(err, &apiErr)
	return apiErr, ok
}

func apiStatusCode(err error) int {
	apiErr, _ := apiErrorOf(err)
	return apiErr.StatusCode
}

func isNotFound(err error) bool {
	apiErr, _ := apiErrorOf(err)
	return apiErr.StatusCode == http.StatusNotFound && apiErr.Code != "file_not_found"
}

// transientAPIError reports errors worth retrying: transport failures, held
// calls (sandbox_starting), rate and concurrency limits, and server faults.
func transientAPIError(err error) bool {
	status := apiStatusCode(err)
	return status == 0 || status == http.StatusConflict || status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout || status >= http.StatusInternalServerError
}
