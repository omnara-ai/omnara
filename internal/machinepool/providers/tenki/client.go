package tenki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	apiBaseURL   = "https://api.tenki.cloud"
	servicePath  = "/tenki.sandbox.v1.SandboxService/"
	listPageSize = 100
)

const (
	sessionStateCreating     = "CREATING"
	sessionStateRunning      = "RUNNING"
	sessionStatePausing      = "PAUSING"
	sessionStatePaused       = "PAUSED"
	sessionStateResuming     = "RESUMING"
	sessionStateUserShutdown = "USER_SHUTDOWN"
	sessionStateTerminating  = "TERMINATING"
	sessionStateTerminated   = "TERMINATED"
)

type session struct {
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Metadata map[string]string `json:"metadata"`
	Sticky   bool              `json:"sticky"`
}

type apiClient interface {
	Create(context.Context, createRequest) (session, error)
	List(context.Context) ([]session, error)
	Get(context.Context, string) (session, bool, error)
	Delete(context.Context, string) error
}

type createRequest struct {
	OwnerID       string            `json:"ownerId"`
	OwnerType     string            `json:"ownerType"`
	Name          string            `json:"name"`
	Metadata      map[string]string `json:"metadata"`
	Tags          []string          `json:"tags"`
	AllowInbound  bool              `json:"allowInbound"`
	AllowOutbound bool              `json:"allowOutbound"`
	CPUCores      int               `json:"cpuCores"`
	MemoryMB      int               `json:"memoryMb"`
	DiskSizeGB    int32             `json:"diskSizeGb,omitempty"`
	RegistryRef   string            `json:"registryRef,omitempty"`
	Sticky        bool              `json:"sticky"`
	Runtime       bootRuntime       `json:"runtime"`
}

type bootRuntime struct {
	Env           map[string]string `json:"env"`
	RunAt         string            `json:"runAt"`
	Start         startCommand      `json:"start"`
	RestartPolicy string            `json:"restartPolicy"`
}

type startCommand struct {
	Argv []string `json:"argv"`
}

type sessionResponse struct {
	Session session `json:"session"`
}

type restClient struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

func newRESTClient(baseURL, token string, httpClient *http.Client) *restClient {
	return &restClient{baseURL: baseURL, apiToken: token, httpClient: httpClient}
}

func (c *restClient) Create(ctx context.Context, request createRequest) (session, error) {
	var response sessionResponse
	if err := c.call(ctx, "CreateSession", request, &response); err != nil {
		return session{}, err
	}
	return response.Session.normalized(), nil
}

func (c *restClient) Get(ctx context.Context, id string) (session, bool, error) {
	var response sessionResponse
	err := c.call(ctx, "GetSession", sessionIDRequest{SessionID: id}, &response)
	if isNotFound(err) {
		return session{}, false, nil
	}
	if err != nil {
		return session{}, false, err
	}
	return response.Session.normalized(), true, nil
}

func (c *restClient) List(ctx context.Context) ([]session, error) {
	var sessions []session
	seenTokens := map[string]struct{}{}
	token := ""
	for {
		var response struct {
			Sessions      []session `json:"sessions"`
			NextPageToken string    `json:"nextPageToken"`
		}
		err := c.call(ctx, "ListWorkspaceSandboxes", listRequest{
			PageSize:  listPageSize,
			PageToken: token,
			Tags:      []string{managedTag},
		}, &response)
		if err != nil {
			return nil, err
		}
		for _, item := range response.Sessions {
			sessions = append(sessions, item.normalized())
		}
		if response.NextPageToken == "" {
			return sessions, nil
		}
		if _, repeated := seenTokens[response.NextPageToken]; repeated {
			return nil, errors.New("tenki session list repeated a page token")
		}
		seenTokens[response.NextPageToken] = struct{}{}
		token = response.NextPageToken
	}
}

func (c *restClient) Delete(ctx context.Context, id string) error {
	err := c.call(ctx, "TerminateSession", sessionIDRequest{SessionID: id}, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

type sessionIDRequest struct {
	SessionID string `json:"sessionId"`
}

type listRequest struct {
	PageSize  int      `json:"pageSize"`
	PageToken string   `json:"pageToken,omitempty"`
	Tags      []string `json:"tags"`
}

func (c *restClient) call(ctx context.Context, method string, body, out any) error {
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.Tenki,
		http.MethodPost,
		c.baseURL+servicePath+method,
		map[string]string{
			"Authorization":            "Bearer " + c.apiToken,
			"Connect-Protocol-Version": "1",
		},
		body,
	)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var connectError struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(response.Body, &connectError)
		return providers.WithRetryAfter(
			apiError{StatusCode: response.StatusCode, Code: connectError.Code},
			response.Header,
		)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(response.Body, out); err != nil {
		return fmt.Errorf("decode tenki response: %w", err)
	}
	return nil
}

func (s session) normalized() session {
	s.State = strings.TrimPrefix(s.State, "SESSION_STATE_")
	return s
}

type apiError struct {
	StatusCode int
	Code       string
}

func (e apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("tenki API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("tenki API returned HTTP %d (%s)", e.StatusCode, e.Code)
}

func isNotFound(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func isTooManyRequests(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests
}

func rejectedBeforeCreate(err error) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 &&
		apiErr.StatusCode != http.StatusRequestTimeout && apiErr.StatusCode != 499
}
