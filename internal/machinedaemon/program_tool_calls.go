package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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

	programToolCallProcessLimit = 16
	programToolCallServerLimit  = 128
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
	c.programToolCalls = c.newProgramToolCallHandler()
	// A call is held open until its result arrives, so writes have no deadline.
	return c.serveProcessService(ctx, programToolCallService, &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
		Handler:           c.programToolCalls,
	})
}

// programToolCallHandler serves `omnarad call`: it offers each call to the
// backend on the daemon websocket and streams the backend's reports back as
// JSON lines until the call completes or is rejected.
type programToolCallHandler struct {
	client   *Client
	requests chan struct{}

	mu       sync.Mutex
	inFlight map[string]int
	calls    map[string]*programToolCall
}

// programToolCall is an offered call waiting for reports. Reports only move a
// call forward, so a waiter needs just the latest.
type programToolCall struct {
	offer   daemonprotocol.ToolCallOffer
	report  *daemonprotocol.ToolCallReport
	changed chan struct{}
}

func (c *Client) newProgramToolCallHandler() *programToolCallHandler {
	return &programToolCallHandler{
		client:   c,
		requests: make(chan struct{}, programToolCallServerLimit),
		inFlight: make(map[string]int),
		calls:    make(map[string]*programToolCall),
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
	processID, ok := strings.CutPrefix(r.URL.Path, "/tool-calls/")
	if _, err := publicid.Decode(publicid.KindProcess, processID); !ok || err != nil {
		h.writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
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
	h.call(w, r, processID)
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

func (h *programToolCallHandler) call(w http.ResponseWriter, r *http.Request, processID string) {
	// The backend validates the request beyond its size and key.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, daemonprotocol.ProgramToolCallMaxInputBytes+4096))
	offer := daemonprotocol.ToolCallOffer{ProcessID: processID}
	if err == nil {
		err = json.Unmarshal(body, &offer.ProgramToolCallRequest)
	}
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || len(offer.Input) > daemonprotocol.ProgramToolCallMaxInputBytes:
		h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "tool call input is too large")
		return
	case err != nil || offer.Key == "":
		h.writeError(w, http.StatusBadRequest, "invalid_request", "tool call request could not be read")
		return
	}
	id := processID + "\x00" + offer.Key
	call := &programToolCall{offer: offer, changed: make(chan struct{}, 1)}
	h.mu.Lock()
	_, duplicate := h.calls[id]
	if !duplicate {
		h.calls[id] = call
	}
	h.mu.Unlock()
	if duplicate {
		// An earlier request for this call is still being torn down.
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable", "tool call is already in flight")
		return
	}
	defer func() {
		h.mu.Lock()
		delete(h.calls, id)
		h.mu.Unlock()
	}()
	// With no websocket the offer goes out when the daemon next connects.
	h.client.sendToolCallOffer(offer)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)
	_ = flusher.Flush()
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-call.changed:
		}
		h.mu.Lock()
		report := *call.report
		h.mu.Unlock()
		if err := encoder.Encode(report); err != nil {
			return
		}
		if err := flusher.Flush(); err != nil {
			return
		}
		if report.AckStatus != "" || report.State == daemonprotocol.ProgramToolCallStateCompleted {
			return
		}
	}
}

// report hands the backend's report to the request waiting on the call.
func (h *programToolCallHandler) report(report daemonprotocol.ToolCallReport) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	call, ok := h.calls[report.ProcessID+"\x00"+report.Key]
	if !ok {
		return
	}
	call.report = &report
	select {
	case call.changed <- struct{}{}:
	default:
	}
}

// offers lists the calls in flight, for a new websocket to offer again.
func (h *programToolCallHandler) offers() []daemonprotocol.ToolCallOffer {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	offers := make([]daemonprotocol.ToolCallOffer, 0, len(h.calls))
	for _, call := range h.calls {
		offers = append(offers, call.offer)
	}
	return offers
}

// sendToolCallOffer offers a call on the current websocket, if there is one.
func (c *Client) sendToolCallOffer(offer daemonprotocol.ToolCallOffer) {
	c.transportMu.RLock()
	transport, connected := c.transport.(*daemonSocketTransport)
	c.transportMu.RUnlock()
	if connected {
		transport.offerToolCall(offer)
	}
}

func (t *daemonSocketTransport) offerToolCall(offer daemonprotocol.ToolCallOffer) {
	if !t.enqueue(daemonprotocol.Message{Type: daemonprotocol.MessageToolCallOffer, ToolCallOffer: &offer}) {
		t.fail(errors.New("daemon websocket send queue is full"))
	}
}

type programToolCallError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// writeError uses the backend's API error shape, as `omnarad call` expects.
func (h *programToolCallHandler) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(programToolCallError{Error: message, Code: code}); err != nil {
		h.client.log.Debug("program tool call error could not be written")
	}
}
