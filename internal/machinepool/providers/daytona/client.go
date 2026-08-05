package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

type apiClient interface {
	GetSnapshot(context.Context, string) (snapshot, error)
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	CreateSession(context.Context, sandbox, string) error
	GetSession(context.Context, sandbox, string) (session, bool, error)
	DeleteSession(context.Context, sandbox, string) error
	ExecuteSessionCommand(
		context.Context,
		sandbox,
		string,
		sessionExecuteRequest,
	) (sessionExecuteResponse, error)
}

type snapshot struct {
	Name      string   `json:"name"`
	State     string   `json:"state"`
	CPU       float64  `json:"cpu"`
	Memory    float64  `json:"mem"`
	GPU       float64  `json:"gpu"`
	RegionIDs []string `json:"regionIds"`
}

type createSandboxRequest struct {
	Name               string            `json:"name"`
	Snapshot           string            `json:"snapshot"`
	Env                map[string]string `json:"env"`
	Labels             map[string]string `json:"labels"`
	Target             string            `json:"target"`
	AutoStopInterval   int               `json:"autoStopInterval"`
	AutoDeleteInterval int               `json:"autoDeleteInterval"`
}

type sandbox struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Labels          map[string]string `json:"labels"`
	Target          string            `json:"target"`
	CPU             float64           `json:"cpu"`
	Memory          float64           `json:"memory"`
	State           string            `json:"state"`
	ToolboxProxyURL string            `json:"toolboxProxyUrl"`
}

type session struct {
	Commands []command `json:"commands"`
}

type command struct {
	ExitCode *int `json:"exitCode"`
}

type sessionExecuteRequest struct {
	Command  string `json:"command"`
	RunAsync bool   `json:"runAsync"`
}

type sessionExecuteResponse struct {
	CommandID string `json:"cmdId"`
	ExitCode  *int   `json:"exitCode"`
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

func (c *restClient) GetSnapshot(ctx context.Context, name string) (snapshot, error) {
	var response snapshot
	err := c.doRequest(
		ctx,
		http.MethodGet,
		c.apiBaseURL+"/snapshots/"+url.PathEscape(name),
		nil,
		&response,
	)
	return response, err
}

func (c *restClient) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	var response sandbox
	err := c.doRequest(ctx, http.MethodPost, c.apiBaseURL+"/sandbox", request, &response)
	return response, err
}

func (c *restClient) GetSandbox(
	ctx context.Context,
	idOrName string,
) (sandbox, bool, error) {
	var response sandbox
	err := c.doRequest(
		ctx,
		http.MethodGet,
		c.apiBaseURL+"/sandbox/"+url.PathEscape(idOrName),
		nil,
		&response,
	)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) DeleteSandbox(ctx context.Context, idOrName string) error {
	err := c.doRequest(
		ctx,
		http.MethodDelete,
		c.apiBaseURL+"/sandbox/"+url.PathEscape(idOrName),
		nil,
		nil,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) CreateSession(
	ctx context.Context,
	sandbox sandbox,
	sessionID string,
) error {
	baseURL, err := toolboxBaseURL(sandbox)
	if err != nil {
		return err
	}
	return c.doRequest(
		ctx,
		http.MethodPost,
		baseURL+"/process/session",
		map[string]string{"sessionId": sessionID},
		nil,
	)
}

func (c *restClient) GetSession(
	ctx context.Context,
	sandbox sandbox,
	sessionID string,
) (session, bool, error) {
	baseURL, err := toolboxBaseURL(sandbox)
	if err != nil {
		return session{}, false, err
	}
	var response session
	err = c.doRequest(
		ctx,
		http.MethodGet,
		baseURL+"/process/session/"+url.PathEscape(sessionID),
		nil,
		&response,
	)
	if isNotFound(err) {
		return session{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) DeleteSession(
	ctx context.Context,
	sandbox sandbox,
	sessionID string,
) error {
	baseURL, err := toolboxBaseURL(sandbox)
	if err != nil {
		return err
	}
	err = c.doRequest(
		ctx,
		http.MethodDelete,
		baseURL+"/process/session/"+url.PathEscape(sessionID),
		nil,
		nil,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) ExecuteSessionCommand(
	ctx context.Context,
	sandbox sandbox,
	sessionID string,
	request sessionExecuteRequest,
) (sessionExecuteResponse, error) {
	baseURL, err := toolboxBaseURL(sandbox)
	if err != nil {
		return sessionExecuteResponse{}, err
	}
	var response sessionExecuteResponse
	err = c.doRequest(
		ctx,
		http.MethodPost,
		baseURL+"/process/session/"+url.PathEscape(sessionID)+"/exec",
		request,
		&response,
	)
	return response, err
}

func toolboxBaseURL(sandbox sandbox) (string, error) {
	proxyURL := strings.TrimRight(strings.TrimSpace(sandbox.ToolboxProxyURL), "/")
	if proxyURL == "" || sandbox.ID == "" {
		return "", fmt.Errorf("daytona sandbox %q is missing its toolbox url or id", sandbox.Name)
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!providers.IsHTTPS(parsed) {
		return "", fmt.Errorf("daytona sandbox %q has an invalid toolbox url", sandbox.Name)
	}
	return proxyURL + "/" + url.PathEscape(sandbox.ID), nil
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, requestURL string,
	body, out any,
) error {
	statusCode, raw, err := providers.DoHTTPRequest(
		ctx,
		c.httpClient,
		providers.Daytona,
		method,
		requestURL,
		map[string]string{"Authorization": "Bearer " + c.apiToken},
		body,
	)
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode >= 300 {
		return apiError{StatusCode: statusCode}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode daytona response: %w", err)
	}
	return nil
}

type apiError struct {
	StatusCode int
}

func (e apiError) Error() string {
	return fmt.Sprintf("daytona API returned HTTP %d", e.StatusCode)
}

func isStatus(err error, statusCode int) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

func isNotFound(err error) bool {
	return isStatus(err, http.StatusNotFound)
}

func isConflict(err error) bool {
	return isStatus(err, http.StatusConflict)
}
