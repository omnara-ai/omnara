package novita

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	envdPort             = 49983
	maxStartFrameBytes   = 64 * 1024
	connectEndStreamFlag = 0x02
	sandboxStateRunning  = "running"
	sandboxStatePaused   = "paused"
	defaultEnvdUser      = "user"
	buildStatusReady     = "ready"
)

type apiClient interface {
	ResolveTemplateAlias(ctx context.Context, alias string) (string, error)
	GetTemplate(ctx context.Context, templateID string) (template, error)
	CreateSandbox(context.Context, createSandboxRequest) (sandbox, error)
	FindSandboxes(ctx context.Context, metadata map[string]string) ([]sandbox, error)
	GetSandbox(ctx context.Context, id string) (sandbox, bool, error)
	DeleteSandbox(ctx context.Context, id string) error
	ExtendTimeout(ctx context.Context, id string, seconds int) error
	ListProcesses(context.Context, sandbox) ([]process, error)
	StartProcess(context.Context, sandbox, startProcessRequest) error
}

// template is the subset of the Novita template detail the provider needs.
// The size comes from the template itself when the API reports it there and
// from the most recently finished ready build otherwise, which is the build a
// new sandbox starts from.
type template struct {
	TemplateID string          `json:"templateID"`
	Names      []string        `json:"names"`
	CPUCount   int             `json:"cpuCount"`
	MemoryMB   int             `json:"memoryMB"`
	Builds     []templateBuild `json:"builds,omitempty"`
}

type templateBuild struct {
	Status     string    `json:"status"`
	CPUCount   int       `json:"cpuCount"`
	MemoryMB   int       `json:"memoryMB"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// size returns the CPU and memory a sandbox created from the template gets.
// The API serves builds newest first, but the spec does not document that
// order, so the newest ready build is chosen by its timestamps rather than by
// position.
func (t template) size() (int, int) {
	if t.CPUCount > 0 && t.MemoryMB > 0 {
		return t.CPUCount, t.MemoryMB
	}
	var latest *templateBuild
	for index := range t.Builds {
		build := &t.Builds[index]
		if build.Status != buildStatusReady || build.CPUCount <= 0 || build.MemoryMB <= 0 {
			continue
		}
		if latest == nil || build.newerThan(*latest) {
			latest = build
		}
	}
	if latest == nil {
		return t.CPUCount, t.MemoryMB
	}
	return latest.CPUCount, latest.MemoryMB
}

func (b templateBuild) newerThan(other templateBuild) bool {
	if !b.FinishedAt.Equal(other.FinishedAt) {
		return b.FinishedAt.After(other.FinishedAt)
	}
	return b.CreatedAt.After(other.CreatedAt)
}

type sandbox struct {
	SandboxID       string            `json:"sandboxID"`
	State           string            `json:"state"`
	CPUCount        int               `json:"cpuCount"`
	MemoryMB        int               `json:"memoryMB"`
	Metadata        map[string]string `json:"metadata"`
	EnvdAccessToken string            `json:"envdAccessToken"`
	EnvdVersion     string            `json:"envdVersion"`
	Domain          string            `json:"domain"`
}

type createSandboxRequest struct {
	TemplateID string            `json:"templateID"`
	Timeout    int               `json:"timeout"`
	Secure     bool              `json:"secure"`
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
	return &restClient{domain: domain, token: token, httpClient: client}
}

func (c *restClient) ResolveTemplateAlias(ctx context.Context, alias string) (string, error) {
	var out struct {
		TemplateID string `json:"templateID"`
	}
	err := c.do(ctx, http.MethodGet, c.apiURL("/templates/aliases/"+url.PathEscape(alias)), c.apiKey(), nil, &out)
	return out.TemplateID, err
}

func (c *restClient) GetTemplate(ctx context.Context, templateID string) (template, error) {
	var out template
	err := c.do(ctx, http.MethodGet, c.apiURL("/templates/"+url.PathEscape(templateID)), c.apiKey(), nil, &out)
	return out, err
}

func (c *restClient) CreateSandbox(ctx context.Context, input createSandboxRequest) (sandbox, error) {
	var out sandbox
	err := c.do(ctx, http.MethodPost, c.apiURL("/sandboxes"), c.apiKey(), input, &out)
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
		return fmt.Errorf("encode novita process start: %w", err)
	}
	var header [5]byte
	binary.BigEndian.PutUint32(header[1:], uint32(len(message)))
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.envdURL(target, "/process.Process/Start"),
		bytes.NewReader(append(header[:], message...)),
	)
	if err != nil {
		return fmt.Errorf("build novita process start: %w", err)
	}
	request.Header.Set("Content-Type", "application/connect+json")
	for key, value := range envdHeaders(target) {
		request.Header.Set(key, value)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("novita process start failed: %w", err)
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

// readStartEvent consumes Connect streaming envelopes until the sandbox
// reports the started process. Each envelope is a flag byte, a big-endian
// uint32 length and a JSON payload; an end-stream envelope before the start
// event means the process never started.
func readStartEvent(stream io.Reader) error {
	for {
		var header [5]byte
		if _, err := io.ReadFull(stream, header[:]); err != nil {
			return fmt.Errorf("novita process start ended before the process started: %w", err)
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length > maxStartFrameBytes {
			return fmt.Errorf("novita process start frame of %d bytes exceeds %d", length, maxStartFrameBytes)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(stream, payload); err != nil {
			return fmt.Errorf("novita process start frame is truncated: %w", err)
		}
		if header[0]&connectEndStreamFlag != 0 {
			return errors.New("novita process start ended before the process started")
		}
		var frame struct {
			Event struct {
				Start *struct {
					Pid uint32 `json:"pid"`
				} `json:"start"`
			} `json:"event"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			return fmt.Errorf("decode novita process start frame: %w", err)
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
	response, err := providers.DoHTTPResponse(ctx, c.httpClient, providers.Novita, method, requestURL, headers, body)
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
		return fmt.Errorf("decode novita response: %w", err)
	}
	return nil
}

func (c *restClient) apiKey() map[string]string {
	return map[string]string{"X-API-Key": c.token}
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

// envdHeaders returns the headers every sandbox daemon request carries. Daemons
// older than 0.4 identify the OS user through HTTP basic auth, so the default
// user is added for them on every call, not only on process start.
func envdHeaders(target sandbox) map[string]string {
	headers := map[string]string{"X-Access-Token": target.EnvdAccessToken, "Connect-Protocol-Version": "1"}
	if envdNeedsDefaultUser(target.EnvdVersion) {
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(defaultEnvdUser+":"))
	}
	return headers
}

// envdNeedsDefaultUser reports whether the sandbox daemon predates access
// tokens and still expects the default basic-auth user.
func envdNeedsDefaultUser(version string) bool {
	var major, minor int
	_, err := fmt.Sscanf(version, "%d.%d", &major, &minor)
	return err == nil && major == 0 && minor < 4
}

type apiError struct {
	StatusCode int
}

func (e apiError) Error() string {
	return fmt.Sprintf("novita API returned HTTP %d", e.StatusCode)
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
