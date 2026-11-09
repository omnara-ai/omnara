package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	programToolProcessIDEnv  = "OMNARA_PROCESS_ID"
	programToolEndpointEnv   = "OMNARA_TOOL_ENDPOINT"
	programToolCapabilityEnv = "OMNARA_TOOL_CAPABILITY"
	programToolBinEnv        = "OMNARA_BIN"

	programToolCallMaxResponseBytes = 16 * 1024 * 1024
	programToolCallProcessLimit     = 16
	programToolCallServerLimit      = 128
	programToolCallSubmitTimeout    = 30 * time.Second
	// programToolCallForwardMargin is added to a poll's wait for backend latency.
	programToolCallForwardMargin = 10 * time.Second
)

// prepareProgramToolCalls never fails preparation: the feature is optional, and
// `omnarad call` explains itself when its environment is missing.
func (c *Client) prepareProgramToolCalls(assignment *ProcessAssignment, supervisorToken string) {
	assignment.ProgramToolHelper = nil
	if assignment.Process.ExecutionSpec.Kind != processcmd.KindShell || !assignment.ProgrammaticToolCalls ||
		assignment.PreparationError != "" || c.programToolCallsUnavailable {
		assignment.ProgrammaticToolCalls = false
		return
	}
	helper, err := c.processServiceHelper(programToolCallService, supervisorToken, assignment.ID)
	if err != nil {
		c.log.Warn("program tool call helper unavailable", "error", err)
		assignment.ProgrammaticToolCalls = false
		return
	}
	assignment.ProgramToolHelper = helper
}

func programToolCallEnvironment(env []string, processID string, helper *processServiceHelper) []string {
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
	return c.serveProcessService(ctx, programToolCallService, &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      time.Duration(daemonprotocol.ProgramToolCallMaxWaitMs)*time.Millisecond + 20*time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
		Handler:           c.newProgramToolCallHandler(),
	})
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
	switch h.client.authorizeProcessService(ctx, programToolCallService, r, processID) {
	case 0:
		return 0, "", ""
	case http.StatusForbidden:
		return http.StatusForbidden, "forbidden", "tool call request denied"
	default:
		return http.StatusServiceUnavailable, "service_unavailable", "tool call service unavailable"
	}
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, daemonprotocol.ProgramToolCallMaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				"tool call input exceeds 1 MiB")
			return
		}
		h.writeError(w, http.StatusBadRequest, "invalid_request", "tool call request could not be read")
		return
	}
	// The backend validates the request.
	h.forward(w, r, http.MethodPost, daemonAPIPath+"/processes/"+processID+"/tool-calls", body,
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
	response, err := h.client.toolCallHTTP.Do(request)
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

// validProgramToolCallID keeps a poll's tool call ID to one path segment;
// the backend decides whether it names a call.
func validProgramToolCallID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r < ' ' || r == '/' || r == '\\' || r == 0x7f {
			return false
		}
	}
	return true
}
