package machinedaemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	programToolProcessIDEnv  = "OMNARA_PROCESS_ID"
	programToolEndpointEnv   = "OMNARA_TOOL_ENDPOINT"
	programToolCapabilityEnv = "OMNARA_TOOL_CAPABILITY"
	programToolBinEnv        = "OMNARA_BIN"

	programToolCallMaxRequestBytes  = 1024 * 1024
	programToolCallMaxResponseBytes = 16 * 1024 * 1024
	programToolCallMaxIDBytes       = 256
	programToolCallProcessLimit     = 16
	programToolCallServerLimit      = 128
	programToolCallSubmitTimeout    = 30 * time.Second
	// programToolCallForwardMargin is added to a poll's wait for backend latency.
	programToolCallForwardMargin = 10 * time.Second
)

type programToolHelper struct {
	Executable string `json:"executable"`
	Endpoint   string `json:"endpoint"`
	Capability string `json:"capability"`
}

func programToolCallCapability(supervisorToken, processID string) string {
	mac := hmac.New(sha256.New, []byte(supervisorToken))
	_, _ = mac.Write([]byte("omnara-program-tool-call\x00" + processID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) programToolCallEndpoint() (string, error) {
	machine, err := c.machineStore()
	if err != nil {
		return "", err
	}
	return filepath.Join(machine.RunDir(), "tool-calls.sock"), nil
}

// prepareProgramToolCalls never fails preparation: the feature is optional, and
// `omnarad call` explains itself when its environment is missing.
func (c *Client) prepareProgramToolCalls(assignment *ProcessAssignment, supervisorToken string) {
	assignment.ProgramToolHelper = nil
	if assignment.Process.ExecutionSpec.Kind != processcmd.KindShell || !assignment.ProgrammaticToolCalls ||
		assignment.PreparationError != "" || c.programToolCallsUnavailable {
		assignment.ProgrammaticToolCalls = false
		return
	}
	endpoint, err := c.programToolCallEndpoint()
	if err != nil {
		c.log.Warn("program tool call endpoint unavailable", "error", err)
		assignment.ProgrammaticToolCalls = false
		return
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.Abs(executable)
	}
	if err != nil {
		c.log.Warn("program tool call executable unavailable", "error", err)
		assignment.ProgrammaticToolCalls = false
		return
	}
	assignment.ProgramToolHelper = &programToolHelper{
		Executable: executable,
		Endpoint:   endpoint,
		Capability: programToolCallCapability(supervisorToken, assignment.ID),
	}
}

func programToolCallEnvironment(env []string, processID string, helper *programToolHelper) []string {
	result := make([]string, 0, len(env)+4)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case programToolProcessIDEnv, programToolEndpointEnv, programToolCapabilityEnv, programToolBinEnv:
		default:
			result = append(result, entry)
		}
	}
	return append(result,
		programToolProcessIDEnv+"="+processID,
		programToolEndpointEnv+"="+helper.Endpoint,
		programToolCapabilityEnv+"="+helper.Capability,
		programToolBinEnv+"="+helper.Executable,
	)
}

func (c *Client) startProgramToolCallServer(ctx context.Context) (func(), error) {
	endpoint, err := c.programToolCallEndpoint()
	if err != nil {
		return nil, err
	}
	if err := localipc.Cleanup(endpoint); err != nil {
		return nil, err
	}
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return serverCtx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      time.Duration(daemonprotocol.ProgramToolCallMaxWaitMs)*time.Millisecond + 20*time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
		Handler:           c.newProgramToolCallHandler(),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("program tool call service stopped", "error", err)
		}
	}()
	return func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		<-done
		_ = localipc.Cleanup(endpoint)
	}, nil
}

type programToolCallHandler struct {
	client   *Client
	requests chan struct{}

	mu       sync.Mutex
	inFlight map[string]int
}

func (c *Client) newProgramToolCallHandler() *programToolCallHandler {
	return &programToolCallHandler{
		client:   c,
		requests: make(chan struct{}, programToolCallServerLimit),
		inFlight: make(map[string]int),
	}
}

func (h *programToolCallHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	select {
	case h.requests <- struct{}{}:
		defer func() { <-h.requests }()
	default:
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable", "tool call service busy")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/tool-calls/")
	processID, toolCallID, poll := strings.Cut(rest, "/")
	if _, err := publicid.Decode(publicid.KindProcess, processID); !ok || err != nil ||
		(poll && !validProgramToolCallID(toolCallID)) {
		h.writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	method := http.MethodPost
	if poll {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		h.writeError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if status, code, message := h.authorize(r, processID); status != 0 {
		h.writeError(w, status, code, message)
		return
	}
	if !h.acquire(processID) {
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable",
			"too many concurrent tool calls for this process")
		return
	}
	defer h.release(processID)
	if poll {
		h.poll(w, r, processID, toolCallID)
	} else {
		h.submit(w, r, processID)
	}
}

func (h *programToolCallHandler) authorize(r *http.Request, processID string) (int, string, string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	state, err := h.client.stateStore(ctx)
	if err != nil {
		return http.StatusServiceUnavailable, "service_unavailable", "tool call service unavailable"
	}
	process, found, err := state.Process(ctx, processID)
	if err != nil {
		return http.StatusServiceUnavailable, "service_unavailable", "tool call service unavailable"
	}
	capability, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := programToolCallCapability(process.SupervisorToken, processID)
	if !found || process.LocalClosed || !bearer || !hmac.Equal([]byte(capability), []byte(want)) {
		return http.StatusForbidden, "forbidden", "tool call request denied"
	}
	return 0, "", ""
}

func (h *programToolCallHandler) acquire(processID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight[processID] >= programToolCallProcessLimit {
		return false
	}
	h.inFlight[processID]++
	return true
}

func (h *programToolCallHandler) release(processID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight[processID] <= 1 {
		delete(h.inFlight, processID)
		return
	}
	h.inFlight[processID]--
}

func (h *programToolCallHandler) submit(w http.ResponseWriter, r *http.Request, processID string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, programToolCallMaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				"tool call request exceeds 1 MiB")
			return
		}
		h.writeError(w, http.StatusBadRequest, "invalid_request", "tool call request could not be read")
		return
	}
	var request daemonprotocol.ProgramToolCallRequest
	if err := json.Unmarshal(body, &request); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "tool call request is not valid JSON")
		return
	}
	if request.Key == "" || len(request.Key) > programToolCallMaxIDBytes || request.Name == "" ||
		!isJSONObject(request.Input) {
		h.writeError(w, http.StatusBadRequest, "invalid_request",
			"tool call request requires key, name, and an input object")
		return
	}
	payload, err := json.Marshal(request)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "tool call request is not valid JSON")
		return
	}
	h.forward(w, r, http.MethodPost, daemonAPIPath+"/processes/"+processID+"/tool-calls", payload,
		programToolCallSubmitTimeout)
}

func (h *programToolCallHandler) poll(w http.ResponseWriter, r *http.Request, processID, toolCallID string) {
	waitMs := 0
	if raw := r.URL.Query().Get("wait_ms"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			h.writeError(w, http.StatusBadRequest, "invalid_request",
				"wait_ms must be a non-negative integer")
			return
		}
		waitMs = min(value, daemonprotocol.ProgramToolCallMaxWaitMs)
	}
	path := daemonAPIPath + "/processes/" + processID + "/tool-calls/" + url.PathEscape(toolCallID) +
		"?wait_ms=" + strconv.Itoa(waitMs)
	h.forward(w, r, http.MethodGet, path, nil,
		time.Duration(waitMs)*time.Millisecond+programToolCallForwardMargin)
}

// forward relays the backend's status, Retry-After, and body unchanged so
// `omnarad call` sees the backend's own error codes and messages.
func (h *programToolCallHandler) forward(
	w http.ResponseWriter,
	r *http.Request,
	method, path string,
	body []byte,
	timeout time.Duration,
) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, h.client.cfg.APIURL+path, requestBody)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable",
			"tool call service unavailable")
		return
	}
	request.Header.Set("Authorization", "Bearer "+h.client.cfg.MachineToken)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := h.client.toolCallHTTP
	if httpClient == nil {
		httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		h.client.log.Debug("program tool call could not reach Omnara", "error", err)
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable",
			"Omnara is temporarily unreachable")
		return
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, programToolCallMaxResponseBytes+1))
	if err != nil {
		h.client.log.Debug("program tool call response could not be read", "error", err)
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable",
			"Omnara is temporarily unreachable")
		return
	}
	if len(data) > programToolCallMaxResponseBytes {
		h.writeError(w, http.StatusBadGateway, "upstream_unavailable",
			"tool call response exceeds 16 MiB")
		return
	}
	if retryAfter := response.Header.Get("Retry-After"); retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(response.StatusCode)
	if _, err := w.Write(data); err != nil {
		h.client.log.Debug("program tool call response could not be written")
	}
}

type programToolCallError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// writeError uses the backend's API error shape so the CLI reports local and
// relayed failures the same way.
func (h *programToolCallHandler) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(programToolCallError{Error: message, Code: code}); err != nil {
		h.client.log.Debug("program tool call error could not be written")
	}
}

func validProgramToolCallID(id string) bool {
	if id == "" || len(id) > programToolCallMaxIDBytes {
		return false
	}
	for _, r := range id {
		if r < ' ' || r == '/' || r == '\\' || r == 0x7f {
			return false
		}
	}
	return true
}

func isJSONObject(value json.RawMessage) bool {
	trimmed := bytes.TrimLeft(value, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(value)
}
