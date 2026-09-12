package arker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const testVMID = "vmh-abc123-01TESTVM"

var (
	testInstallationID = uuid.MustParse("3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	testMachineID      = uuid.MustParse("3f2504e0-4f89-11d3-9a0c-0305e82c3302")
)

func ptr[T any](value T) *T { return &value }

func testAllocationName(t *testing.T) string {
	t.Helper()
	name, err := providers.MachineAllocationName(testInstallationID, testMachineID)
	if err != nil {
		t.Fatalf("build allocation name: %v", err)
	}
	return name
}

func testProvisioning(options map[string]json.RawMessage) executionstore.MachineProvisioningConfig {
	return executionstore.MachineProvisioningConfig{
		CPU:             ptr(2),
		MemoryMB:        ptr(4096),
		ProviderOptions: options,
	}
}

func testOptions() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"source":         json.RawMessage(`"ubuntu-base"`),
		"provider":       json.RawMessage(`"aws"`),
		"region":         json.RawMessage(`"us-west-2"`),
		"startup_script": json.RawMessage(`""`),
	}
}

func testPolicy(
	config json.RawMessage,
	defaults map[string]json.RawMessage,
) executionstore.MachinePoolProviderPolicy {
	return executionstore.MachinePoolProviderPolicy{
		ProviderConfig: config,
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        ptr(64),
			MaxTotalMemoryMB:   ptr(131072),
			MinMachineCPU:      ptr(1),
			MinMachineMemoryMB: ptr(512),
			MaxMachineCPU:      ptr(8),
			MaxMachineMemoryMB: ptr(16384),
		},
		DefaultProvisioning: executionstore.MachineProvisioningConfig{
			CPU:             ptr(2),
			MemoryMB:        ptr(4096),
			ProviderOptions: defaults,
		},
	}
}

type fakeArker struct {
	vmName        string
	forkStatus    int
	sessionError  bool
	writeStatus   int
	daemonExit    *int
	daemonState   string
	daemonAliveIn string

	mu            sync.Mutex
	keys          []string
	forkBody      map[string]any
	sessionEnv    map[string]string
	runCommand    string
	runSessionIdx int

	forks       atomic.Int64
	writes      atomic.Int64
	runListings atomic.Int64
	runs        atomic.Int64
	sessions    atomic.Int64
	deletes     atomic.Int64
}

func (f *fakeArker) record(mutate func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mutate()
}

func (f *fakeArker) snapshot() (keys []string, forkBody map[string]any, env map[string]string, command string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys...), f.forkBody, f.sessionEnv, f.runCommand
}

func (f *fakeArker) start(t *testing.T, name string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/fork", func(w http.ResponseWriter, r *http.Request) {
		f.forks.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.record(func() {
			f.keys = append(f.keys, r.Header.Get("Idempotency-Key"))
			f.forkBody = body
		})
		if f.forkStatus != 0 {
			w.WriteHeader(f.forkStatus)
			fmt.Fprint(w, `{"error":{"code":"refused","message":"fork refused"}}`)
			return
		}
		vmName := f.vmName
		if vmName == "" {
			vmName, _ = body["name"].(string)
		}
		writeVM(w, vmName)
	})
	mux.HandleFunc("/v1/vms/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodDelete && strings.Count(path, "/") == 3:
			f.deletes.Add(1)
			fmt.Fprint(w, `{"deleted":true}`)
		case strings.HasSuffix(path, "/sync-stream"):
			f.writes.Add(1)
			if f.writeStatus != 0 {
				w.WriteHeader(f.writeStatus)
				fmt.Fprint(w, `{"error":{"code":"refused","message":"write refused"}}`)
				return
			}
			fmt.Fprint(w, `{"status":"ok"}`)
		case strings.HasSuffix(path, "/sessions"):
			f.sessions.Add(1)
			if f.sessionError {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"code":"refused","message":"session refused"}}`)
				return
			}
			var body struct {
				Env map[string]string `json:"env"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.record(func() { f.sessionEnv = body.Env })
			fmt.Fprint(w, `{"session_id":"sess_1"}`)
		case strings.Contains(path, "/runs/"):
			if f.daemonState == "" {
				fmt.Fprint(w, `{"run_id":"run_1","state":"running"}`)
				return
			}
			if f.daemonExit == nil {
				fmt.Fprintf(w, `{"run_id":"run_1","state":%q}`, f.daemonState)
				return
			}
			fmt.Fprintf(w, `{"run_id":"run_1","state":%q,"exit_code":%d}`,
				f.daemonState, *f.daemonExit)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/runs"):
			f.runListings.Add(1)
			if f.daemonAliveIn != "" && r.URL.Query().Get("state") == f.daemonAliveIn {
				fmt.Fprintf(w, `{"runs":[{"run_id":"run_0","state":%q,"command":%q}]}`,
					f.daemonAliveIn, daemonCommand)
				return
			}
			fmt.Fprint(w, `{"runs":[]}`)
		case strings.HasSuffix(path, "/runs"):
			f.runs.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			command, _ := body["command"].(string)
			idx, _ := body["session_idx"].(float64)
			f.record(func() {
				f.runCommand = command
				f.runSessionIdx = int(idx)
			})
			if ttb, ok := body["time_to_background"].(float64); ok && ttb == 0 {
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprint(w, `{"run_id":"run_1","state":"running"}`)
				return
			}
			fmt.Fprint(w, `{"run_id":"run_1","state":"completed","exit_code":0}`)
		default:
			vmName := f.vmName
			if vmName == "" {
				vmName = name
			}
			writeVM(w, vmName)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func writeVM(w http.ResponseWriter, name string) {
	fmt.Fprintf(
		w,
		`{"vm_id":%q,"name":%q,"state":"running","provider":"aws","region":"us-west-2"}`,
		testVMID,
		name,
	)
}

func newTestProvider(baseURL string) *provider {
	return &provider{
		apiKey:       "ark_test",
		baseURL:      baseURL,
		omnaraAPIURL: "https://omnara.example",
	}
}

const liveTestSettleWindow = 10 * time.Millisecond

func init() {
	daemonSettleWindow = liveTestSettleWindow
}
