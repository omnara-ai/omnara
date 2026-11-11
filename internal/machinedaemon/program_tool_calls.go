package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

	programToolCallProcessLimit  = 128
	programToolCallServerLimit   = 128
	programToolCallCancelTimeout = 10 * time.Second
)

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
	bin := filepath.Dir(helper.Executable)
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case programToolProcessIDEnv, programToolEndpointEnv, programToolCapabilityEnv, programToolBinEnv:
		case "PATH":
			if value != bin && !strings.HasPrefix(value, bin+string(os.PathListSeparator)) {
				value = bin + string(os.PathListSeparator) + value
			}
			result = append(result, name+"="+value)
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
	stopping := make(chan struct{})
	c.programToolCalls.stopping = stopping
	stop, err := c.serveProcessService(ctx, programToolCallService, &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8192,
		Handler:           c.programToolCalls,
	})
	if err != nil {
		return nil, err
	}
	return func() {
		close(stopping)
		stop()
	}, nil
}

type programToolCallHandler struct {
	client   *Client
	requests chan struct{}
	stopping <-chan struct{}

	mu        sync.Mutex
	inFlight  map[string]int
	calls     map[string]*programToolCall
	abandoned []daemonprotocol.ToolCallOffer
}

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
		w.Header().Set("Retry-After", "1")
		h.writeError(w, http.StatusServiceUnavailable, "service_unavailable", "tool call is already in flight")
		return
	}
	defer func() {
		h.mu.Lock()
		delete(h.calls, id)
		h.mu.Unlock()
	}()
	h.client.sendToolCallOffer(r.Context(), offer)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)
	_ = flusher.Flush()
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			h.abandon(r.Context(), offer)
			return
		case <-call.changed:
		}
		h.mu.Lock()
		report := *call.report
		h.mu.Unlock()
		over := report.AckStatus != "" || report.State == daemonprotocol.ProgramToolCallStateCompleted
		err := encoder.Encode(report)
		if err == nil {
			err = flusher.Flush()
		}
		if err != nil && !over {
			h.abandon(r.Context(), offer)
		}
		if err != nil || over {
			return
		}
	}
}

func (h *programToolCallHandler) abandon(ctx context.Context, offer daemonprotocol.ToolCallOffer) {
	select {
	case <-h.stopping:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), programToolCallCancelTimeout)
	defer cancel()
	if h.client.sendToolCallCancel(ctx, offer) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.abandoned) >= programToolCallServerLimit {
		h.abandoned = h.abandoned[1:]
	}
	h.abandoned = append(h.abandoned, offer)
}

func (h *programToolCallHandler) resume(ctx context.Context, transport *daemonSocketTransport) {
	if h == nil {
		return
	}
	for _, offer := range h.offers() {
		if !transport.offerToolCall(ctx, offer) {
			return
		}
	}
	h.mu.Lock()
	abandoned := h.abandoned
	h.abandoned = nil
	h.mu.Unlock()
	for i, offer := range abandoned {
		if !transport.offerToolCall(ctx, offer) || !transport.cancelToolCall(ctx, offer) {
			h.mu.Lock()
			h.abandoned = append(abandoned[i:], h.abandoned...)
			h.mu.Unlock()
			return
		}
	}
}

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

func (c *Client) socketTransport() (*daemonSocketTransport, bool) {
	c.transportMu.RLock()
	defer c.transportMu.RUnlock()
	transport, connected := c.transport.(*daemonSocketTransport)
	return transport, connected && transport != nil
}

func (c *Client) sendToolCallOffer(ctx context.Context, offer daemonprotocol.ToolCallOffer) {
	if transport, connected := c.socketTransport(); connected {
		transport.offerToolCall(ctx, offer)
	}
}

func (c *Client) sendToolCallCancel(ctx context.Context, offer daemonprotocol.ToolCallOffer) bool {
	transport, connected := c.socketTransport()
	return connected && transport.cancelToolCall(ctx, offer)
}

func (t *daemonSocketTransport) offerToolCall(ctx context.Context, offer daemonprotocol.ToolCallOffer) bool {
	return t.enqueueWait(ctx, daemonprotocol.Message{Type: daemonprotocol.MessageToolCallOffer, ToolCallOffer: &offer})
}

func (t *daemonSocketTransport) cancelToolCall(ctx context.Context, offer daemonprotocol.ToolCallOffer) bool {
	return t.enqueueWait(ctx, daemonprotocol.Message{Type: daemonprotocol.MessageToolCallCancel,
		ToolCallCancel: &daemonprotocol.ToolCallCancel{ProcessID: offer.ProcessID, Key: offer.Key}})
}

func (t *daemonSocketTransport) enqueueWait(ctx context.Context, message daemonprotocol.Message) bool {
	select {
	case t.send <- message:
		return true
	case <-t.done:
		return false
	case <-ctx.Done():
		return false
	}
}

type programToolCallError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func (h *programToolCallHandler) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(programToolCallError{Error: message, Code: code}); err != nil {
		h.client.log.Debug("program tool call error could not be written")
	}
}
