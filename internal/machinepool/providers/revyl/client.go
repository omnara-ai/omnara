package revyl

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	computerOSMacOS = "macos"

	commandKindExec      = "exec"
	commandKindFileWrite = "file_write"

	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"

	evidencePending = "pending"

	errorCodeExecNonzero = "exec_nonzero"
)

var operationPollInterval = time.Second

type apiClient interface {
	CreateComputer(context.Context, createComputerRequest) (computer, error)
	GetComputer(context.Context, uuid.UUID) (computer, bool, error)
	StopComputer(context.Context, uuid.UUID) (computer, bool, error)
	ListComputers(context.Context, int) error
	WriteFile(context.Context, uuid.UUID, string, []byte) error
	Exec(context.Context, uuid.UUID, execRequest) (execResult, error)
}

type createComputerRequest struct {
	ID             uuid.UUID `json:"computer_session_id"`
	OS             string    `json:"os"`
	TimeoutSeconds int       `json:"timeout_seconds,omitempty"`
}

type computer struct {
	ID            uuid.UUID     `json:"computer_session_id"`
	OS            string        `json:"os"`
	State         computerState `json:"state"`
	Release       string        `json:"release"`
	FailureReason *string       `json:"failure_reason"`
}

type execRequest struct {
	Argv      []string
	Stdin     []byte
	TimeoutMS int
}

type execResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

type operationRequest struct {
	ComputerID uuid.UUID `json:"computer_session_id"`
	RequestID  uuid.UUID `json:"request_id"`
	Command    any       `json:"command"`
}

type execCommand struct {
	Kind      string   `json:"kind"`
	ArgvB64   []string `json:"argv_b64"`
	Stdin     string   `json:"stdin,omitempty"`
	TimeoutMS int      `json:"timeout_ms"`
}

type fileWriteCommand struct {
	Kind      string `json:"kind"`
	PathB64   string `json:"path_b64"`
	Data      string `json:"data"`
	TimeoutMS int    `json:"timeout_ms"`
}

type operationResponse struct {
	ComputerID  uuid.UUID `json:"computer_session_id"`
	OperationID uuid.UUID `json:"operation_id"`
	Evidence    string    `json:"evidence"`
	Execution   struct {
		Outcome   string           `json:"outcome"`
		ErrorCode string           `json:"error_code"`
		Result    *operationResult `json:"result"`
	} `json:"execution"`
}

type operationResult struct {
	Kind      string `json:"kind"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ByteCount int    `json:"byte_count"`
}

type restClient struct {
	apiBaseURL string
	apiToken   string
	httpClient *http.Client
}

func newRESTClient(baseURL, token string, httpClient *http.Client) *restClient {
	return &restClient{
		apiBaseURL: strings.TrimRight(baseURL, "/"),
		apiToken:   token,
		httpClient: httpClient,
	}
}

func (c *restClient) CreateComputer(ctx context.Context, request createComputerRequest) (computer, error) {
	var response computer
	_, err := c.doRequest(ctx, http.MethodPost, "/api/v1/computer-runs", request, nil, &response)
	return response, err
}

func (c *restClient) GetComputer(ctx context.Context, id uuid.UUID) (computer, bool, error) {
	var response computer
	_, err := c.doRequest(ctx, http.MethodGet, "/api/v1/computer-runs/"+id.String(), nil, nil, &response)
	if isNotFound(err) {
		return computer{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) StopComputer(ctx context.Context, id uuid.UUID) (computer, bool, error) {
	var response computer
	_, err := c.doRequest(
		ctx,
		http.MethodPost,
		"/api/v1/computer-runs/"+id.String()+"/stop",
		nil,
		nil,
		&response,
	)
	if isNotFound(err) {
		return computer{}, false, nil
	}
	return response, err == nil, err
}

func (c *restClient) ListComputers(ctx context.Context, limit int) error {
	_, err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/api/v1/computer-runs?limit=%d", limit), nil, nil, nil)
	return err
}

func (c *restClient) WriteFile(ctx context.Context, id uuid.UUID, path string, data []byte) error {
	result, err := c.runOperation(ctx, id, fileWriteCommand{
		Kind:      commandKindFileWrite,
		PathB64:   encodeBytes([]byte(path)),
		Data:      encodeBytes(data),
		TimeoutMS: 30_000,
	})
	if err != nil {
		return fmt.Errorf("write revyl computer file %q: %w", path, err)
	}
	if result.Execution.Outcome != outcomeSucceeded || result.Execution.Result == nil ||
		result.Execution.Result.Kind != commandKindFileWrite || result.Execution.Result.ByteCount != len(data) {
		return fmt.Errorf(
			"write revyl computer file %q: outcome %q error %q",
			path,
			result.Execution.Outcome,
			result.Execution.ErrorCode,
		)
	}
	return nil
}

func (c *restClient) Exec(ctx context.Context, id uuid.UUID, request execRequest) (execResult, error) {
	argv := make([]string, len(request.Argv))
	for index, argument := range request.Argv {
		argv[index] = encodeBytes([]byte(argument))
	}
	command := execCommand{Kind: commandKindExec, ArgvB64: argv, TimeoutMS: request.TimeoutMS}
	if len(request.Stdin) > 0 {
		command.Stdin = encodeBytes(request.Stdin)
	}
	response, err := c.runOperation(ctx, id, command)
	if err != nil {
		return execResult{}, fmt.Errorf("run revyl computer command: %w", err)
	}
	execution := response.Execution
	exited := execution.Outcome == outcomeSucceeded ||
		execution.Outcome == outcomeFailed && execution.ErrorCode == errorCodeExecNonzero
	if !exited || execution.Result == nil || execution.Result.Kind != commandKindExec {
		return execResult{}, fmt.Errorf(
			"revyl computer command did not finish: outcome %q error %q",
			execution.Outcome,
			execution.ErrorCode,
		)
	}
	stdout, err := decodeBytes(execution.Result.Stdout)
	if err != nil {
		return execResult{}, fmt.Errorf("decode revyl command stdout: %w", err)
	}
	stderr, err := decodeBytes(execution.Result.Stderr)
	if err != nil {
		return execResult{}, fmt.Errorf("decode revyl command stderr: %w", err)
	}
	return execResult{ExitCode: execution.Result.ExitCode, Stdout: stdout, Stderr: stderr}, nil
}

func (c *restClient) runOperation(ctx context.Context, id uuid.UUID, command any) (operationResponse, error) {
	requestID := uuid.New()
	operationID := uuid.NewSHA1(id, []byte(requestID.String()))
	var response operationResponse
	status, err := c.doRequest(
		ctx,
		http.MethodPost,
		"/api/v1/computer-runs/"+id.String()+"/operations",
		operationRequest{ComputerID: id, RequestID: requestID, Command: command},
		map[string]string{"Prefer": "respond-async"},
		&response,
	)
	if err != nil {
		return operationResponse{}, err
	}
	for status == http.StatusAccepted || response.Evidence == evidencePending {
		select {
		case <-ctx.Done():
			return operationResponse{}, fmt.Errorf("wait for revyl operation %s: %w", operationID, ctx.Err())
		case <-time.After(operationPollInterval):
		}
		response = operationResponse{}
		status, err = c.doRequest(
			ctx,
			http.MethodGet,
			"/api/v1/computer-runs/"+id.String()+"/operations/"+operationID.String(),
			nil,
			nil,
			&response,
		)
		if err != nil {
			return operationResponse{}, err
		}
	}
	if response.ComputerID != id || response.OperationID != operationID {
		return operationResponse{}, fmt.Errorf("revyl operation %s returned a mismatched identity", operationID)
	}
	return response, nil
}

func (c *restClient) doRequest(
	ctx context.Context,
	method, path string,
	body any,
	headers map[string]string,
	out any,
) (int, error) {
	requestHeaders := map[string]string{"Authorization": "Bearer " + c.apiToken}
	for key, value := range headers {
		requestHeaders[key] = value
	}
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.Revyl,
		method,
		c.apiBaseURL+path,
		requestHeaders,
		body,
	)
	if err != nil {
		return 0, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var detail struct {
			Detail any `json:"detail"`
		}
		_ = json.Unmarshal(response.Body, &detail)
		code, _ := detail.Detail.(string)
		return response.StatusCode, providers.WithResponse(
			apiError{StatusCode: response.StatusCode, Code: code},
			response,
		)
	}
	trimmed := bytes.TrimSpace(response.Body)
	if out == nil || len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return response.StatusCode, nil
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return response.StatusCode, fmt.Errorf("decode revyl response: %w", err)
	}
	return response.StatusCode, nil
}

func encodeBytes(data []byte) string {
	return base64.URLEncoding.EncodeToString(data)
}

func decodeBytes(value string) ([]byte, error) {
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}

type apiError struct {
	StatusCode int
	Code       string
}

func (e apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("revyl API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("revyl API returned HTTP %d: %s", e.StatusCode, e.Code)
}

func apiErrorOf(err error) (apiError, bool) {
	var apiErr apiError
	ok := errors.As(err, &apiErr)
	return apiErr, ok
}

func isNotFound(err error) bool {
	apiErr, ok := apiErrorOf(err)
	return ok && apiErr.StatusCode == http.StatusNotFound
}
