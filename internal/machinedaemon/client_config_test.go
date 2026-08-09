package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRejectsAllRedirectsWithoutMutatingProvidedClient(t *testing.T) {
	t.Parallel()

	provided := &http.Client{}
	client := New(Config{}, provided, nil)
	request := httptest.NewRequest(http.MethodGet, "https://api.example.test/redirect", nil)
	if err := client.http.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v, want ErrUseLastResponse", err)
	}
	if provided.CheckRedirect != nil {
		t.Fatal("New mutated the provided HTTP client")
	}
}

func TestRegisterSendsEmptyProcessInventoryAsArray(t *testing.T) {
	t.Parallel()

	requestBodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		requestBodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			w,
			`{"runtime":{"id":"drt_test","next_heartbeat_after_ms":1000},"reconciliation":{"processes":[]}}`,
		)
	}))
	t.Cleanup(server.Close)

	client := New(Config{APIURL: server.URL}, server.Client(), nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_empty_registration",
		MachineID:      "mch_empty_registration",
	}
	if _, _, err := client.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	var request struct {
		Processes json.RawMessage `json:"processes"`
	}
	if err := json.Unmarshal(<-requestBodies, &request); err != nil {
		t.Fatal(err)
	}
	var processes []ProcessReconciliationClaim
	if err := json.Unmarshal(request.Processes, &processes); err != nil {
		t.Fatalf("registration processes are not an array: %s", request.Processes)
	}
	if processes == nil || len(processes) != 0 {
		t.Fatalf("registration processes = %#v, want an empty array", processes)
	}
}

func TestSanitizedRunnerEnvDoesNotPropagateDaemonCredentials(t *testing.T) {
	t.Parallel()

	env := sanitizedRunnerEnv([]string{
		"PATH=/bin",
		"Path=/windows/bin",
		"HOME=/tmp/home",
		"ComSpec=C:\\Windows\\System32\\cmd.exe",
		"OMNARA_HOME=/tmp/home/.omnara",
		"OMNARA_MACHINE_TOKEN=secret",
		"OMNARA_API_URL=https://app.omnara.com",
		"UNRELATED_SECRET=value",
	}, "/runner/bin:/usr/bin")
	got := strings.Join(env, "\n")
	for _, blocked := range []string{"OMNARA_MACHINE_TOKEN", "OMNARA_API_URL", "UNRELATED_SECRET"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("sanitized env leaked %s in %q", blocked, got)
		}
	}
	if strings.Contains(got, "PATH=/bin") || strings.Contains(got, "Path=/windows/bin") {
		t.Fatalf("sanitized env retained ambient PATH: %q", got)
	}
	if !strings.Contains(got, "PATH=/runner/bin:/usr/bin") || !strings.Contains(got, "HOME=/tmp/home") ||
		!strings.Contains(got, "ComSpec=") || !strings.Contains(got, "OMNARA_HOME=/tmp/home/.omnara") {
		t.Fatalf("sanitized env dropped expected baseline vars: %q", got)
	}
	pathCount := 0
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PATH") {
			pathCount++
		}
	}
	if pathCount != 1 {
		t.Fatalf("sanitized env has %d PATH entries: %q", pathCount, env)
	}
}

func TestSanitizedRunnerEnvLeavesPathUnsetWhenConfiguredPathIsEmpty(t *testing.T) {
	t.Parallel()

	env := sanitizedRunnerEnv([]string{"PATH=/bin", "Path=/windows/bin", "HOME=/tmp/home"}, "")
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "PATH") {
			t.Fatalf("sanitized env retained PATH: %q", env)
		}
	}
}

func TestProcessRunnerEnvAppliesConfiguredEnvironment(t *testing.T) {
	t.Parallel()

	env := processRunnerEnv(
		[]string{"Path=/windows/bin", "PATH=/bin", "HOME=/tmp/home", "UNRELATED_SECRET=value"},
		"/bin",
		map[string]string{"path": "/custom/bin", "APP_ENV": "test", "OMNARA_HOME": "/tmp/home/.omnara"},
	)
	const want = "APP_ENV=test\nHOME=/tmp/home\nOMNARA_HOME=/tmp/home/.omnara\nPATH=/custom/bin"
	if got := strings.Join(env, "\n"); got != want {
		t.Fatalf("process runner env = %q, want %q", got, want)
	}
}

func TestWorkloadProcessEnvRemovesAllOmnaraVariables(t *testing.T) {
	t.Parallel()

	env := workloadProcessEnv(
		[]string{
			"HOME=/tmp/home",
			"OMNARA_HOME=/tmp/home/.omnara",
			"omnara_machine_token=ambient-secret",
		},
		"/bin",
		map[string]string{
			"APP_ENV":            "test",
			"OmNaRa_Api_Url":     "https://forbidden.example",
			"OMNARA_TEST_SECRET": "explicit-secret",
		},
	)
	got := strings.Join(env, "\n")
	if strings.Contains(strings.ToUpper(got), "OMNARA_") {
		t.Fatalf("workload environment contains reserved Omnara variables: %q", got)
	}
	if !strings.Contains(got, "APP_ENV=test") || !strings.Contains(got, "HOME=/tmp/home") {
		t.Fatalf("workload environment dropped ordinary variables: %q", got)
	}
}
