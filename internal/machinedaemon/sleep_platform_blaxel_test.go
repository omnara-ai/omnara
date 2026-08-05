package machinedaemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestBlaxelSleepPlatformAwakeProcessLifecycle(t *testing.T) {
	supervisorPID := 321
	supervisorStartTime := "987654"
	awakeProcessName := daemonprotocol.BlaxelAwakeProcessName(supervisorPID)
	awakeProcessCommand, err := blaxelAwakeProcessCommand(supervisorPID, supervisorStartTime)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var process *blaxelProcessResponse
	postCalls := 0
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if process == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(process)
		case http.MethodPost:
			var request blaxelProcessRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Name != awakeProcessName || request.Command != awakeProcessCommand ||
				!request.KeepAlive || request.Timeout != 0 || request.WaitForCompletion {
				t.Fatalf("unexpected awake process request: %+v", request)
			}
			postCalls++
			process = &blaxelProcessResponse{Status: "running", KeepAlive: true}
			_ = json.NewEncoder(w).Encode(process)
		case http.MethodDelete:
			deleteCalls++
			if process == nil {
				http.NotFound(w, r)
				return
			}
			process = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	platform := &blaxelSleepPlatform{
		apiURL:              server.URL,
		httpClient:          server.Client(),
		supervisorPID:       supervisorPID,
		supervisorStartTime: supervisorStartTime,
		awakeProcessName:    awakeProcessName,
		awakeProcessCommand: awakeProcessCommand,
		processStartTime:    func(int) (string, error) { return supervisorStartTime, nil },
	}
	if err := platform.preventSleep(); err != nil {
		t.Fatalf("create awake process: %v", err)
	}
	if err := platform.preventSleep(); err != nil {
		t.Fatalf("adopt awake process: %v", err)
	}
	if postCalls != 1 {
		t.Fatalf("awake process posts = %d, want 1", postCalls)
	}
	if err := platform.allowSleep(); err != nil {
		t.Fatalf("stop awake process: %v", err)
	}
	if err := platform.allowSleep(); err != nil {
		t.Fatalf("repeat awake process stop: %v", err)
	}
	if deleteCalls != 2 {
		t.Fatalf("awake process deletes = %d, want 2", deleteCalls)
	}
}

func TestBlaxelSleepPlatformReplacesNonKeepAliveProcess(t *testing.T) {
	supervisorPID := 321
	supervisorStartTime := "987654"
	awakeProcessName := daemonprotocol.BlaxelAwakeProcessName(supervisorPID)
	awakeProcessCommand, _ := blaxelAwakeProcessCommand(supervisorPID, supervisorStartTime)
	process := blaxelProcessResponse{Status: "running", KeepAlive: false}
	deleteCalls := 0
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(process)
		case http.MethodDelete:
			deleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			postCalls++
			process = blaxelProcessResponse{Status: "running", KeepAlive: true}
			_ = json.NewEncoder(w).Encode(process)
		}
	}))
	defer server.Close()
	platform := &blaxelSleepPlatform{
		apiURL:              server.URL,
		httpClient:          server.Client(),
		supervisorPID:       supervisorPID,
		supervisorStartTime: supervisorStartTime,
		awakeProcessName:    awakeProcessName,
		awakeProcessCommand: awakeProcessCommand,
		processStartTime:    func(int) (string, error) { return supervisorStartTime, nil },
	}
	if err := platform.preventSleep(); err != nil {
		t.Fatalf("replace awake process: %v", err)
	}
	if deleteCalls != 1 || postCalls != 1 {
		t.Fatalf("awake process deletes=%d posts=%d, want 1 each", deleteCalls, postCalls)
	}
}

func TestBlaxelSleepPlatformReconcilesAmbiguousAwakeProcessStart(t *testing.T) {
	postCalls := 0
	getCalls := 0
	platform := &blaxelSleepPlatform{
		apiURL:              daemonprotocol.BlaxelLocalAPIURL,
		awakeProcessName:    daemonprotocol.BlaxelAwakeProcessName(321),
		awakeProcessCommand: "awake-process-command",
		httpClient: &http.Client{Transport: blaxelSleepRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.Method {
			case http.MethodPost:
				postCalls++
				return nil, errors.New("connection lost after request")
			case http.MethodGet:
				getCalls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(
						`{"status":"running","keepAlive":true}`,
					)),
				}, nil
			default:
				t.Fatalf("unexpected request: %s", request.Method)
				return nil, errors.New("unexpected request")
			}
		})},
	}
	if err := platform.startAwakeProcess(); err != nil {
		t.Fatalf("reconcile awake process start: %v", err)
	}
	if postCalls != 1 || getCalls != 1 {
		t.Fatalf("awake process posts=%d gets=%d, want 1 each", postCalls, getCalls)
	}
}

func TestBlaxelSleepPlatformRejectsDeadSupervisor(t *testing.T) {
	platform := &blaxelSleepPlatform{
		supervisorPID:       321,
		supervisorStartTime: "987654",
		processStartTime:    func(int) (string, error) { return "different", nil },
	}
	if err := platform.preventSleep(); err == nil {
		t.Fatal("dead awake process supervisor must fail")
	}
}

func TestBlaxelSleepPlatformPreservesSupervisorReadError(t *testing.T) {
	readErr := errors.New("read process stat")
	platform := &blaxelSleepPlatform{
		supervisorPID:       321,
		supervisorStartTime: "987654",
		processStartTime:    func(int) (string, error) { return "", readErr },
	}
	if err := platform.preventSleep(); !errors.Is(err, readErr) {
		t.Fatalf("prevent sleep error = %v, want %v", err, readErr)
	}
}

func TestBlaxelAwakeProcessCommand(t *testing.T) {
	command, err := blaxelAwakeProcessCommand(321, "987654")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"supervisor_pid=321",
		"supervisor_start=987654",
		"/proc/$supervisor_pid/stat",
	} {
		if !strings.Contains(command, value) {
			t.Fatalf("process command %q does not contain %q", command, value)
		}
	}
	if _, err := blaxelAwakeProcessCommand(0, "987654"); err == nil {
		t.Fatal("zero supervisor pid must fail")
	}
	if _, err := blaxelAwakeProcessCommand(321, "not-numeric"); err == nil {
		t.Fatal("non-numeric start time must fail")
	}
}

func TestParseBlaxelProcessStartTime(t *testing.T) {
	stat := "123 (process with spaces) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654 20"
	startTime, err := parseBlaxelProcessStartTime(stat)
	if err != nil {
		t.Fatal(err)
	}
	if startTime != "987654" {
		t.Fatalf("start time = %q, want 987654", startTime)
	}
	if _, err := parseBlaxelProcessStartTime("malformed"); err == nil {
		t.Fatal("malformed process stat must fail")
	}
}

type blaxelSleepRoundTripFunc func(*http.Request) (*http.Response, error)

func (f blaxelSleepRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
