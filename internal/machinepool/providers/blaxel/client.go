package blaxel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	apiBaseURL          = "https://api.blaxel.ai/v0"
	dataPlaneRetries    = 2
	dataPlaneRetryDelay = 500 * time.Millisecond
)

type apiClient interface {
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	GetSandbox(context.Context, string) (sandbox, bool, error)
	DeleteSandbox(context.Context, string) error
	StartSandboxProcess(context.Context, sandbox, processRequest) (sandboxProcess, error)
	GetSandboxProcess(context.Context, sandbox, string) (sandboxProcess, bool, error)
	WakeSandbox(context.Context, sandbox) error
}

type createSandboxRequest struct {
	Metadata resourceMetadata `json:"metadata"`
	Spec     sandboxSpec      `json:"spec"`
}

type resourceMetadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	URL    string            `json:"url,omitempty"`
}

type sandboxSpec struct {
	Region  string         `json:"region"`
	Runtime sandboxRuntime `json:"runtime"`
}

type sandboxRuntime struct {
	Image  string        `json:"image"`
	Memory int           `json:"memory"`
	Envs   []sandboxEnv  `json:"envs,omitempty"`
	Ports  []sandboxPort `json:"ports,omitempty"`
}

type sandboxEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sandboxPort struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Target   int    `json:"target"`
}

type sandbox struct {
	Metadata resourceMetadata `json:"metadata"`
	Status   string           `json:"status"`
}

type processRequest struct {
	Name              string `json:"name"`
	Command           string `json:"command"`
	KeepAlive         bool   `json:"keepAlive"`
	Timeout           int    `json:"timeout"`
	WaitForCompletion bool   `json:"waitForCompletion"`
}

type sandboxProcess struct {
	PID       string `json:"pid"`
	Status    string `json:"status"`
	KeepAlive bool   `json:"keepAlive"`
}

type restClient struct {
	apiBaseURL string
	workspace  string
	apiToken   string
	httpClient *http.Client
}

func (c *restClient) CreateSandbox(
	ctx context.Context,
	request createSandboxRequest,
) (sandbox, error) {
	var result sandbox
	_, err := c.doRequest(
		ctx,
		http.MethodPost,
		c.apiBaseURL+"/sandboxes?createIfNotExist=true",
		request,
		&result,
	)
	if err != nil {
		return sandbox{}, err
	}
	return result, nil
}

func (c *restClient) GetSandbox(
	ctx context.Context,
	name string,
) (sandbox, bool, error) {
	var result sandbox
	_, err := c.doRequest(
		ctx,
		http.MethodGet,
		c.apiBaseURL+"/sandboxes/"+url.PathEscape(name),
		nil,
		&result,
	)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	if err != nil {
		return sandbox{}, false, err
	}
	return result, true, nil
}

func (c *restClient) DeleteSandbox(ctx context.Context, name string) error {
	_, err := c.doRequest(
		ctx,
		http.MethodDelete,
		c.apiBaseURL+"/sandboxes/"+url.PathEscape(name),
		nil,
		nil,
	)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) StartSandboxProcess(
	ctx context.Context,
	target sandbox,
	request processRequest,
) (sandboxProcess, error) {
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return sandboxProcess{}, err
	}
	var process sandboxProcess
	if _, err := c.doRequest(
		ctx,
		http.MethodPost,
		sandboxURL+"/process",
		request,
		&process,
	); err != nil {
		return sandboxProcess{}, err
	}
	return process, nil
}

func (c *restClient) GetSandboxProcess(
	ctx context.Context,
	target sandbox,
	name string,
) (sandboxProcess, bool, error) {
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return sandboxProcess{}, false, err
	}
	var process sandboxProcess
	_, err = c.doDataPlaneRequest(
		ctx,
		http.MethodGet,
		sandboxURL+"/process/"+url.PathEscape(name),
		nil,
		&process,
	)
	if isNotFound(err) {
		return sandboxProcess{}, false, nil
	}
	if err != nil {
		return sandboxProcess{}, false, err
	}
	return process, true, nil
}

func (c *restClient) WakeSandbox(ctx context.Context, target sandbox) error {
	sandboxURL, err := sandboxDataPlaneURL(target)
	if err != nil {
		return err
	}
	statusCode, err := c.doDataPlaneRequest(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/port/%d/", sandboxURL, daemonprotocol.WakeListenerPort),
		nil,
		nil,
	)
	if err != nil {
		return err
	}
	if statusCode != http.StatusNoContent {
		return fmt.Errorf(
			"blaxel wake endpoint returned HTTP %d, want HTTP %d",
			statusCode,
			http.StatusNoContent,
		)
	}
	return nil
}

func sandboxDataPlaneURL(target sandbox) (string, error) {
	sandboxURL := strings.TrimRight(strings.TrimSpace(target.Metadata.URL), "/")
	if sandboxURL == "" {
		return "", fmt.Errorf("blaxel sandbox %q is missing its data-plane url", target.Metadata.Name)
	}
	parsed, err := url.Parse(sandboxURL)
	if err != nil {
		return "", fmt.Errorf("blaxel sandbox %q has an invalid data-plane url", target.Metadata.Name)
	}
	if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!providers.IsHTTPS(parsed) {
		return "", fmt.Errorf("blaxel sandbox %q has an invalid data-plane url", target.Metadata.Name)
	}
	return sandboxURL, nil
}

func (c *restClient) doDataPlaneRequest(
	ctx context.Context,
	method, requestURL string,
	body, out any,
) (int, error) {
	var statusCode int
	var lastErr error
	for attempt := 0; attempt <= dataPlaneRetries; attempt++ {
		statusCode, lastErr = c.doRequest(ctx, method, requestURL, body, out)
		if lastErr == nil || !isGatewayError(lastErr) || attempt == dataPlaneRetries {
			return statusCode, lastErr
		}
		timer := time.NewTimer(dataPlaneRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	return statusCode, lastErr
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, requestURL string,
	body, out any,
) (int, error) {
	statusCode, raw, err := providers.DoHTTPRequest(
		ctx,
		c.httpClient,
		providers.Blaxel,
		method,
		requestURL,
		map[string]string{
			"X-Blaxel-Authorization": "Bearer " + c.apiToken,
			"X-Blaxel-Workspace":     c.workspace,
		},
		body,
	)
	if err != nil {
		return 0, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return statusCode, apiError{StatusCode: statusCode}
	}
	if out == nil || len(raw) == 0 {
		return statusCode, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return statusCode, fmt.Errorf("decode blaxel response: %w", err)
	}
	return statusCode, nil
}

type apiError struct {
	StatusCode int
}

func (e apiError) Error() string {
	return fmt.Sprintf("blaxel API returned HTTP %d", e.StatusCode)
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

func isGatewayError(err error) bool {
	return isStatus(err, http.StatusBadGateway) ||
		isStatus(err, http.StatusServiceUnavailable) ||
		isStatus(err, http.StatusGatewayTimeout)
}
