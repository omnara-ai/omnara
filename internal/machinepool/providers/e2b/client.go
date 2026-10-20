package e2b

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	envdPort             = 49983
	maxStartFrameBytes   = 64 * 1024
	connectEndStreamFlag = 0x02
	sandboxStateRunning  = "running"
	sandboxStatePaused   = "paused"
)

type apiClient interface {
	ListTemplates(ctx context.Context, search, cursor string) ([]template, string, error)
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	FindSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error)
	GetSandbox(ctx context.Context, id string) (sandbox, bool, error)
	DeleteSandbox(ctx context.Context, id string) error
	ExtendTimeout(ctx context.Context, id string, seconds int) error
	ListProcesses(context.Context, sandbox) ([]process, error)
	StartProcess(context.Context, sandbox, startProcessRequest) error
}

type template struct {
	TemplateID string   `json:"templateID"`
	Aliases    []string `json:"aliases"`
	Names      []string `json:"names"`
	CPUCount   int      `json:"cpuCount"`
	MemoryMB   int      `json:"memoryMB"`
	IsDefault  bool     `json:"isDefault"`
}

type sandbox struct {
	SandboxID       string            `json:"sandboxID"`
	State           string            `json:"state"`
	CPUCount        int               `json:"cpuCount"`
	MemoryMB        int               `json:"memoryMB"`
	Metadata        map[string]string `json:"metadata"`
	EnvdAccessToken string            `json:"envdAccessToken"`
	Domain          string            `json:"domain"`
}

type createSandboxRequest struct {
	TemplateID string            `json:"templateID"`
	Timeout    int               `json:"timeout"`
	Metadata   map[string]string `json:"metadata"`
	Network    networkConfig     `json:"network"`
}

type networkConfig struct {
	AllowPublicTraffic bool `json:"allowPublicTraffic"`
}

type process struct {
	Tag string `json:"tag"`
}

type startProcessRequest struct {
	Process processConfig `json:"process"`
	Tag     string        `json:"tag"`
	Stdin   bool          `json:"stdin"`
}

type processConfig struct {
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args"`
	Envs map[string]string `json:"envs"`
}

type restClient struct {
	domain, token string
	httpClient    *http.Client
}

func newRESTClient(domain, token string, client *http.Client) *restClient {
	if client == nil {
		client = providers.NewHTTPClient()
	}
	return &restClient{domain: domain, token: token, httpClient: client}
}

func (c *restClient) ListTemplates(ctx context.Context, search, cursor string) ([]template, string, error) {
	values := url.Values{"search": {search}, "limit": {"100"}}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	var out struct {
		Data       []template `json:"data"`
		NextCursor *string    `json:"nextCursor"`
	}
	requestURL := "https://dashboard-api." + c.domain + "/templates?" + values.Encode()
	err := c.do(ctx, http.MethodGet, requestURL, c.apiKey(), nil, &out)
	next := ""
	if out.NextCursor != nil {
		next = *out.NextCursor
	}
	return out.Data, next, err
}

func (c *restClient) CreateSandbox(ctx context.Context, input createSandboxRequest) (sandbox, error) {
	var out sandbox
	err := c.do(ctx, http.MethodPost, c.apiURL("/v2/sandboxes"), c.apiKey(), input, &out)
	return out, err
}

func (c *restClient) FindSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error) {
	values := url.Values{"state": {sandboxStateRunning + "," + sandboxStatePaused}, "limit": {"2"}}
	if len(metadata) > 0 {
		filter := url.Values{}
		for key, value := range metadata {
			filter.Set(key, value)
		}
		values.Set("metadata", filter.Encode())
	}
	var out []sandbox
	err := c.do(ctx, http.MethodGet, c.apiURL("/v2/sandboxes?"+values.Encode()), c.apiKey(), nil, &out)
	return out, err
}

func (c *restClient) GetSandbox(ctx context.Context, id string) (sandbox, bool, error) {
	var out sandbox
	err := c.do(ctx, http.MethodGet, c.apiURL("/sandboxes/"+url.PathEscape(id)), c.apiKey(), nil, &out)
	if isNotFound(err) {
		return sandbox{}, false, nil
	}
	return out, err == nil, err
}

func (c *restClient) DeleteSandbox(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, c.apiURL("/sandboxes/"+url.PathEscape(id)), c.apiKey(), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

func (c *restClient) ExtendTimeout(ctx context.Context, id string, seconds int) error {
	return c.do(
		ctx,
		http.MethodPost,
		c.apiURL("/sandboxes/"+url.PathEscape(id)+"/timeout"),
		c.apiKey(),
		map[string]int{"timeout": seconds},
		nil,
	)
}

func (c *restClient) ListProcesses(ctx context.Context, target sandbox) ([]process, error) {
	var out struct {
		Processes []process `json:"processes"`
	}
	err := c.do(
		ctx,
		http.MethodPost,
		c.envdURL(target, "/process.Process/List"),
		envdHeaders(target),
		map[string]any{},
		&out,
	)
	return out.Processes, err
}

func (c *restClient) StartProcess(ctx context.Context, target sandbox, input startProcessRequest) error {
	message, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode e2b process start: %w", err)
	}
	body := make([]byte, 5, 5+len(message))
	binary.BigEndian.PutUint32(body[1:], uint32(len(message)))
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.envdURL(target, "/process.Process/Start"),
		bytes.NewReader(append(body, message...)),
	)
	if err != nil {
		return fmt.Errorf("build e2b process start: %w", err)
	}
	request.Header.Set("Content-Type", "application/connect+json")
	for key, value := range envdHeaders(target) {
		request.Header.Set(key, value)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("e2b process start failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providers.WithResponse(
			apiError{StatusCode: response.StatusCode},
			providers.HTTPResponse{StatusCode: response.StatusCode, Header: response.Header},
		)
	}
	return readStartEvent(response.Body)
}

func readStartEvent(stream io.Reader) error {
	for {
		var header [5]byte
		if _, err := io.ReadFull(stream, header[:]); err != nil {
			return fmt.Errorf("e2b process start ended before the process started: %w", err)
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length > maxStartFrameBytes {
			return fmt.Errorf("e2b process start frame of %d bytes exceeds %d", length, maxStartFrameBytes)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(stream, payload); err != nil {
			return fmt.Errorf("e2b process start frame is truncated: %w", err)
		}
		if header[0]&connectEndStreamFlag != 0 {
			return errors.New("e2b process start ended before the process started")
		}
		var frame struct {
			Event struct {
				Start *struct {
					Pid uint32 `json:"pid"`
				} `json:"start"`
			} `json:"event"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			return fmt.Errorf("decode e2b process start frame: %w", err)
		}
		if frame.Event.Start != nil {
			return nil
		}
	}
}

func (c *restClient) do(
	ctx context.Context,
	method, requestURL string,
	headers map[string]string,
	body, out any,
) error {
	response, err := providers.DoHTTPResponse(ctx, c.httpClient, providers.E2B, method, requestURL, headers, body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return providers.WithResponse(apiError{StatusCode: response.StatusCode}, response)
	}
	if out == nil || len(response.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(response.Body, out); err != nil {
		return fmt.Errorf("decode e2b response: %w", err)
	}
	return nil
}

func (c *restClient) apiKey() map[string]string {
	return map[string]string{"X-Api-Key": c.token}
}

func (c *restClient) apiURL(path string) string {
	return "https://api." + c.domain + path
}

func (c *restClient) envdURL(target sandbox, path string) string {
	domain := target.Domain
	if domain == "" {
		domain = c.domain
	}
	return "https://" + strconv.Itoa(envdPort) + "-" + target.SandboxID + "." + domain + path
}

func envdHeaders(target sandbox) map[string]string {
	return map[string]string{"X-Access-Token": target.EnvdAccessToken, "Connect-Protocol-Version": "1"}
}

type apiError struct {
	StatusCode int
}

func (e apiError) Error() string {
	return fmt.Sprintf("e2b API returned HTTP %d", e.StatusCode)
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

func transientAPIError(err error) bool {
	status := apiStatusCode(err)
	return status == 0 || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func isDialError(err error) bool {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	return errors.As(err, &dnsErr) || errors.As(err, &opErr) && opErr.Op == "dial"
}
