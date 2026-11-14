package arker

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
		"region":         json.RawMessage(`"aws-us-west-2"`),
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
	vmName             string
	vmState            string
	sessionError       bool
	runState           string
	existingRunState   string
	existingRunCommand string
	sourceRules        string

	mu            sync.Mutex
	forkBodies    map[string]string
	vmPolicies    string
	lookup        string
	keys          []string
	forkBody      map[string]any
	sessionEnv    map[string]string
	runCommand    string
	runSessionID  string
	runSessionIdx *int

	forks    atomic.Int64
	runs     atomic.Int64
	sessions atomic.Int64
	deletes  atomic.Int64
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
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		key := r.Header.Get("Idempotency-Key")
		conflict := false
		f.record(func() {
			f.keys = append(f.keys, key)
			f.forkBody = body
			if previous, ok := f.forkBodies[key]; ok && previous != string(raw) {
				conflict = true
				return
			}
			if f.forkBodies == nil {
				f.forkBodies = map[string]string{}
			}
			if _, replay := f.forkBodies[key]; !replay {
				f.vmPolicies = forkedPolicies(t, f.sourceRules, body["policies"])
			}
			f.forkBodies[key] = string(raw)
		})
		if conflict {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"code":"idempotency_conflict","message":"key reused for a different request"}}`)
			return
		}
		vmName := f.vmName
		if vmName == "" {
			vmName, _ = body["name"].(string)
		}
		writeVM(w, vmName, "running", f.policies())
	})
	mux.HandleFunc("/v1/vms/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodDelete:
			f.deletes.Add(1)
			fmt.Fprint(w, `{"deleted":true}`)
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
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/runs"):
			if f.existingRunState != "" {
				command := f.existingRunCommand
				if command == "" {
					command = daemonLauncherCommand
				}
				fmt.Fprintf(w, `{"runs":[{"run_id":"run_0","state":%q,"command":%q}]}`,
					f.existingRunState, command)
				return
			}
			fmt.Fprint(w, `{"runs":[]}`)
		case strings.HasSuffix(path, "/runs"):
			f.runs.Add(1)
			var body runRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.record(func() {
				f.runCommand = body.Command
				f.runSessionID = body.SessionID
				f.runSessionIdx = body.SessionIdx
			})
			if f.runState == "" {
				w.WriteHeader(http.StatusAccepted)
				fmt.Fprint(w, `{"run_id":"run_1","state":"running"}`)
				return
			}
			fmt.Fprintf(w, `{"run_id":"run_1","state":%q}`, f.runState)
		case f.source() != "" && strings.HasSuffix(path, "/ubuntu-base"):
			fmt.Fprintf(w, `{"vm_id":"vm_source","name":"ubuntu-base","state":"running","policies":{"rules":%s}}`, f.source())
		default:
			f.record(func() { f.lookup = path })
			vmName := f.vmName
			if vmName == "" {
				vmName = name
			}
			vmState := f.vmState
			if vmState == "" {
				vmState = "running"
			}
			writeVM(w, vmName, vmState, f.policies())
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func forkedPolicies(t *testing.T, sourceRules string, requested any) string {
	rules := map[string]any{}
	_ = json.Unmarshal([]byte(sourceRules), &rules)
	if policies, ok := requested.(map[string]any); ok {
		if requestedRules, ok := policies["rules"].(map[string]any); ok {
			maps.Copy(rules, requestedRules)
		}
	}
	merged, err := json.Marshal(map[string]any{"rules": rules})
	if err != nil {
		t.Errorf("merge fork policies: %v", err)
	}
	return string(merged)
}

func (f *fakeArker) source() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourceRules
}

func (f *fakeArker) policies() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vmPolicies
}

func writeVM(w http.ResponseWriter, name, state, policies string) {
	if policies == "" {
		policies = "null"
	}
	fmt.Fprintf(w, `{"vm_id":%q,"name":%q,"state":%q,"policies":%s}`, testVMID, name, state, policies)
}

func newTestProvider(baseURL string) *provider {
	return &provider{
		api: &restClient{
			baseURL:    baseURL,
			apiToken:   "ark_test",
			httpClient: http.DefaultClient,
		},
		apiBaseURL:   baseURL,
		omnaraAPIURL: "https://omnara.example",
	}
}
