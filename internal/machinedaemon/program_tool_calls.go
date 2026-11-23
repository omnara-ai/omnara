package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	programToolProcessIDEnv = "OMNARA_PROCESS_ID"
	programToolEndpointEnv  = "OMNARA_TOOL_ENDPOINT"
	programToolBinEnv       = "OMNARA_BIN"
	programToolBinDirName   = "tool-bin"

	programToolCallProcessLimit  = 128
	programToolCallServerLimit   = 128
	programToolCallCancelTimeout = 10 * time.Second
	programToolCallCancelLimit   = 4096
)

func (c *Client) prepareProgramToolCalls(assignment *ProcessAssignment) {
	assignment.ProgramToolHelper = nil
	if assignment.Process.ExecutionSpec.Kind != processcmd.KindShell || !assignment.ProgrammaticToolCalls ||
		assignment.PreparationError != "" || c.programToolCallsUnavailable {
		assignment.ProgrammaticToolCalls = false
		return
	}
	helper, err := c.processServiceHelper(programToolCallService)
	if err != nil {
		c.log.Warn("program tool call helper unavailable", "error", err)
		assignment.ProgrammaticToolCalls = false
		return
	}
	if helper.BinDir, err = c.programToolBinDir(helper.Executable); err != nil {
		c.log.Warn("program tool call bin directory unavailable", "error", err)
	}
	assignment.ProgramToolHelper = helper
}

func (c *Client) programToolBinDir(executable string) (string, error) {
	machine, err := c.machineStore()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(machine.RunDir(), programToolBinDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // commands run as other users must traverse it
		return "", err
	}
	link := filepath.Join(dir, "omnarad"+filepath.Ext(executable))
	if target, err := os.Readlink(link); err == nil && target == executable {
		return dir, nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Symlink(executable, link); err != nil {
		if linkErr := os.Link(executable, link); linkErr != nil {
			return "", errors.Join(err, linkErr)
		}
	}
	return dir, nil
}

func programToolCallEnvironment(env []string, processID string, helper *processServiceHelper) []string {
	result := make([]string, 0, len(env)+4)
	bin, hasPath := helper.BinDir, false
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case programToolProcessIDEnv, programToolEndpointEnv, programToolBinEnv:
		case "PATH":
			hasPath = true
			if bin != "" && value != bin && !strings.HasPrefix(value, bin+string(os.PathListSeparator)) {
				value = bin + string(os.PathListSeparator) + value
			}
			result = append(result, name+"="+value)
		default:
			result = append(result, entry)
		}
	}
	if bin != "" && !hasPath {
		result = append(result, "PATH="+bin)
	}
	return append(result,
		programToolProcessIDEnv+"="+processID,
		programToolEndpointEnv+"="+helper.Endpoint,
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

	mu       sync.Mutex
	inFlight map[string]int
	calls    map[string]*programToolCall
	cancels  []daemonprotocol.ToolCallCancel
}

type programToolCall struct {
	offer   daemonprotocol.ToolCallOffer
	report  *daemonprotocol.ToolCallReport
	waiters map[chan struct{}]struct{}
	keep    bool
}

func programToolCallOver(report *daemonprotocol.ToolCallReport) bool {
	return report != nil && (report.AckStatus != "" || report.State == daemonprotocol.ProgramToolCallStateCompleted)
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
	if status, code, message := h.processOpen(r, processID); status != 0 {
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

func (h *programToolCallHandler) processOpen(r *http.Request, processID string) (int, string, string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	unavailable := func() (int, string, string) {
		return http.StatusServiceUnavailable, "service_unavailable", "tool call service unavailable"
	}
	state, err := h.client.stateStore(ctx)
	if err != nil {
		return unavailable()
	}
	process, found, err := state.Process(ctx, processID)
	if err != nil {
		return unavailable()
	}
	if !found || process.LocalClosed {
		return http.StatusNotFound, "not_found", "no running process can call tools here"
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
	case len(offer.Name) > daemonprotocol.ProgramToolCallMaxNameBytes:
		h.writeError(w, http.StatusBadRequest, "invalid_request", "tool name is too long")
		return
	}
	offer.OfferID = uuid.NewString()
	if encoded, err := json.Marshal(&daemonprotocol.Message{
		Type: daemonprotocol.MessageToolCallOffer, ToolCallOffer: &offer,
	}); err != nil || len(encoded) > daemonprotocol.MaxMessageBytes {
		h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "tool call input is too large")
		return
	}
	id := processID + "\x00" + offer.Key
	changed := make(chan struct{}, 1)
	h.mu.Lock()
	call, attached := h.calls[id]
	switch {
	case !attached:
		call = &programToolCall{offer: offer, keep: offer.KeepOnDisconnect,
			waiters: map[chan struct{}]struct{}{changed: {}}}
		h.calls[id] = call
	case call.offer.Name != offer.Name || !sameProgramToolCallInput(call.offer.Input, offer.Input):
		h.mu.Unlock()
		h.writeError(w, http.StatusConflict, daemonprotocol.ErrorCodeIdempotencyConflict,
			"idempotency key was already used for a different tool or input")
		return
	default:
		call.waiters[changed] = struct{}{}
		call.keep = call.keep || offer.KeepOnDisconnect
		if call.report != nil {
			changed <- struct{}{}
		}
	}
	h.mu.Unlock()
	defer h.leave(r.Context(), id, call, changed)
	if !attached {
		h.client.sendToolCallOffer(r.Context(), offer)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)
	_ = flusher.Flush()
	encoder := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		}
		h.mu.Lock()
		report := *call.report
		h.mu.Unlock()
		err := encoder.Encode(report)
		if err == nil {
			err = flusher.Flush()
		}
		if err != nil || programToolCallOver(&report) {
			return
		}
	}
}

func sameProgramToolCallInput(a, b json.RawMessage) bool {
	return bytes.Equal(a, b) || jsoncanonical.Equal(a, b)
}

func (h *programToolCallHandler) leave(ctx context.Context, id string, call *programToolCall, changed chan struct{}) {
	h.mu.Lock()
	delete(call.waiters, changed)
	last := len(call.waiters) == 0
	if last && h.calls[id] == call {
		delete(h.calls, id)
	}
	abandon := last && !call.keep && !programToolCallOver(call.report)
	h.mu.Unlock()
	if abandon {
		h.abandon(ctx, call.offer)
	}
}

func (h *programToolCallHandler) abandon(ctx context.Context, offer daemonprotocol.ToolCallOffer) {
	select {
	case <-h.stopping:
		return
	default:
	}
	cancel := daemonprotocol.ToolCallCancel{ProcessID: offer.ProcessID, Key: offer.Key, OfferID: offer.OfferID}
	h.mu.Lock()
	if len(h.cancels) >= programToolCallCancelLimit {
		h.cancels = h.cancels[1:]
	}
	h.cancels = append(h.cancels, cancel)
	h.mu.Unlock()
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), programToolCallCancelTimeout)
	defer stop()
	if transport, connected := h.client.socketTransport(); connected {
		transport.cancelToolCall(ctx, cancel)
	}
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
	for _, cancel := range h.pendingCancels(ctx) {
		if !transport.cancelToolCall(ctx, cancel) {
			return
		}
	}
}

func (h *programToolCallHandler) pendingCancels(ctx context.Context) []daemonprotocol.ToolCallCancel {
	h.mu.Lock()
	pending := append([]daemonprotocol.ToolCallCancel(nil), h.cancels...)
	h.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	state, err := h.client.stateStore(ctx)
	if err != nil {
		return pending
	}
	ended := map[string]bool{}
	for _, cancel := range pending {
		if _, checked := ended[cancel.ProcessID]; checked {
			continue
		}
		process, found, err := state.Process(ctx, cancel.ProcessID)
		ended[cancel.ProcessID] = err == nil && (!found || process.LocalClosed)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels = slices.DeleteFunc(h.cancels, func(cancel daemonprotocol.ToolCallCancel) bool {
		return ended[cancel.ProcessID]
	})
	return append([]daemonprotocol.ToolCallCancel(nil), h.cancels...)
}

func (h *programToolCallHandler) report(report daemonprotocol.ToolCallReport) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if report.CancelRecorded {
		h.cancels = slices.DeleteFunc(h.cancels, func(cancel daemonprotocol.ToolCallCancel) bool {
			return cancel.ProcessID == report.ProcessID && cancel.Key == report.Key && cancel.OfferID == report.OfferID
		})
		return
	}
	call, ok := h.calls[report.ProcessID+"\x00"+report.Key]
	if !ok || call.offer.OfferID != report.OfferID {
		return
	}
	call.report = &report
	for changed := range call.waiters {
		select {
		case changed <- struct{}{}:
		default:
		}
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
		if !programToolCallOver(call.report) {
			offers = append(offers, call.offer)
		}
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

func (t *daemonSocketTransport) offerToolCall(ctx context.Context, offer daemonprotocol.ToolCallOffer) bool {
	return t.enqueueWait(ctx, daemonprotocol.Message{Type: daemonprotocol.MessageToolCallOffer, ToolCallOffer: &offer})
}

func (t *daemonSocketTransport) cancelToolCall(ctx context.Context, cancel daemonprotocol.ToolCallCancel) bool {
	return t.enqueueWait(ctx, daemonprotocol.Message{Type: daemonprotocol.MessageToolCallCancel, ToolCallCancel: &cancel})
}

func (t *daemonSocketTransport) enqueueWait(ctx context.Context, message daemonprotocol.Message) bool {
	select {
	case t.toolSend <- message:
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
