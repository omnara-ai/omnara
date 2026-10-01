package freestyle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestProviderProvisionCreatesResizesAndStartsManagedDaemon(t *testing.T) {
	exitCode := 0
	created := ownedVM(t, vmStateRunning, 1, 2048)
	api := &fakeAPI{
		createResult: created,
		execResponse: execVMResponse{StatusCode: &exitCode},
	}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		map[string]string{"APP_ENV": "production"},
		true,
	)
	if err != nil {
		t.Fatalf("provision freestyle VM: %v", err)
	}
	if result.ProviderResourceID != created.ID {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, created.ID)
	}
	name, err := machineName()
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	request := api.createRequest
	if request.SnapshotID != "ubuntu-24.04" || request.Slug != name || request.DisplayName != name ||
		request.AutoDeleteSeconds != autoDeleteNever || !request.AutomaticRestart {
		t.Fatalf("create request = %+v", request)
	}
	if len(request.Metadata) != 2 ||
		request.Metadata[installationMarkerKey] != "inst_caaaaaaaaaaaaaaaaaaaaaaaae" ||
		request.Metadata[machineMarkerKey] != "mch_eaaaaaaaaaaaaaaaaaaaaaaaai" {
		t.Fatalf("create ownership metadata = %#v", request.Metadata)
	}
	if len(request.Firewall.Rules) != 1 || request.Firewall.Rules[0].Action != "allow" ||
		!request.Firewall.Rules[0].Destination.Public {
		t.Fatalf("create firewall = %+v", request.Firewall)
	}
	if request.IdleTimeoutSeconds != 0 || request.TLS != nil || result.SandboxURL != "" {
		t.Fatalf("sleep must be disabled by default: request %+v, sandbox url %q", request, result.SandboxURL)
	}
	if api.resizeRequest.CPU != 2 || api.resizeRequest.MemoryMB != 4096 {
		t.Fatalf("resize request = %+v", api.resizeRequest)
	}
	if api.execCalls != 1 || api.execResourceID != created.ID ||
		api.execRequest.LinuxUser != "root" || api.execRequest.TimeoutMS != daemonInstallTimeoutMS {
		t.Fatalf("exec request = resource %q request %+v", api.execResourceID, api.execRequest)
	}
	if api.execRequest.Command != daemonInstallScript {
		t.Fatalf("managed daemon install command must not carry machine env: %q", api.execRequest.Command)
	}
	for _, want := range []string{
		"\nUser=root\n",
		"\nKillMode=process\n",
		"\nif cmp -s /etc/omnara/managed-daemon.sh.new /etc/omnara/managed-daemon.sh &&\n",
		"\nsystemctl restart omnara-daemon.service\n",
	} {
		if !strings.Contains(daemonInstallScript, want) {
			t.Fatalf("managed daemon install missing %q: %s", want, daemonInstallScript)
		}
	}
	rawStartScript, err := base64.StdEncoding.DecodeString(api.execRequest.Stdin)
	if err != nil {
		t.Fatalf("decode start script stdin: %v", err)
	}
	startScript := string(rawStartScript)
	for _, want := range []string{
		"APP_ENV=production",
		"OMNARA_API_URL=https://api.omnara.test/v1",
		"OMNARA_MACHINE_TOKEN=machine-token",
		providers.ManagedBootstrapScriptEnvVar + "=",
	} {
		if !strings.Contains(startScript, want) {
			t.Fatalf("managed daemon start script missing %q: %s", want, startScript)
		}
	}
	if strings.Contains(startScript, daemonprotocol.SleepAfterEnvVar) ||
		strings.Contains(startScript, bootstrapKeepAwakeScript) {
		t.Fatalf("managed daemon start script must not enable sleep by default: %s", startScript)
	}
}

func TestProviderProvisionEnablesSleep(t *testing.T) {
	exitCode := 0
	api := &fakeAPI{
		createResult: ownedVM(t, vmStateRunning, 2, 4096),
		execResponse: execVMResponse{StatusCode: &exitCode},
	}
	provisioning := testProvisioning(t)
	provisioning.ProviderOptions["sleep_after_ms"] = rawJSON(t, 60000)

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		provisioning,
		"machine-token",
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("provision sleeping freestyle VM: %v", err)
	}
	name, err := machineName()
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	if result.SandboxURL != "https://"+name+".style.dev/" {
		t.Fatalf("sandbox url = %q", result.SandboxURL)
	}
	rawRequest, err := json.Marshal(api.createRequest)
	if err != nil {
		t.Fatalf("encode create request: %v", err)
	}
	for _, want := range []string{
		`"idleTimeoutSeconds":60`,
		`"tls":{"rules":[{"action":"allow","domain":"` + name + `.style.dev","protocol":"http",` +
			`"source":{"public":true},"destination":{"port":8377}}]}`,
	} {
		if !strings.Contains(string(rawRequest), want) {
			t.Fatalf("create request missing %s: %s", want, rawRequest)
		}
	}
	startScript, err := base64.StdEncoding.DecodeString(api.execRequest.Stdin)
	if err != nil {
		t.Fatalf("decode start script stdin: %v", err)
	}
	for _, want := range []string{
		daemonprotocol.SleepAfterEnvVar + "=60000",
		daemonprotocol.WakeListenAddrEnvVar + "=:8377",
		" /bin/sh -c '" + bootstrapKeepAwakeScript,
	} {
		if !strings.Contains(string(startScript), want) {
			t.Fatalf("managed daemon start script missing %q: %s", want, startScript)
		}
	}
}

func TestProviderProvisionAdoptsOwnedVMAfterAmbiguousCreate(t *testing.T) {
	exitCode := 0
	existing := ownedVM(t, vmStateRunning, 2, 4096)
	api := &fakeAPI{
		createErr:    errors.New("ambiguous transport failure"),
		execResponse: execVMResponse{StatusCode: &exitCode},
	}
	api.getFunc = func(string) (vm, bool, error) {
		if len(api.getLookups) == 1 {
			return vm{}, false, nil
		}
		return existing, true, nil
	}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		nil,
		true,
	)
	if err != nil || result.ProviderResourceID != existing.ID {
		t.Fatalf("adopt existing VM = %+v, error %v", result, err)
	}
	if len(api.getLookups) != 2 || api.execCalls != 1 {
		t.Fatalf("adoption lookups = %#v, exec calls = %d", api.getLookups, api.execCalls)
	}
}

func TestProviderProvisionRejectsForeignVMBeforeMutation(t *testing.T) {
	foreign := ownedVM(t, vmStateRunning, 2, 4096)
	foreign.Metadata[machineMarkerKey] = "someone-else"
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) { return foreign, true, nil }}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		nil,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "expected ownership metadata") {
		t.Fatalf("foreign VM error = %v", err)
	}
	if result.ProviderResourceID != "" || api.startCalls != 0 || api.execCalls != 0 {
		t.Fatalf("foreign VM result = %+v starts = %d execs = %d", result, api.startCalls, api.execCalls)
	}
}

func TestProviderProvisionRejectsOversizedSnapshotPermanently(t *testing.T) {
	oversized := ownedVM(t, vmStateRunning, 4, 8192)
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) { return oversized, true, nil }}

	result, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		nil,
		true,
	)
	if !errors.Is(err, providers.ErrPermanent) {
		t.Fatalf("oversized snapshot error = %v, want permanent error", err)
	}
	if result.ProviderResourceID != oversized.ID || api.resizeRequest != (resizeVMRequest{}) || api.execCalls != 0 {
		t.Fatalf("oversized result = %+v resize = %+v execs = %d", result, api.resizeRequest, api.execCalls)
	}
}

func TestProviderProvisionStartsPausedVM(t *testing.T) {
	exitCode := 0
	api := &fakeAPI{
		getFunc:      func(string) (vm, bool, error) { return ownedVM(t, vmStatePaused, 2, 4096), true, nil },
		startResult:  ownedVM(t, vmStateRunning, 2, 4096),
		execResponse: execVMResponse{StatusCode: &exitCode},
	}

	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		"machine-token",
		nil,
		true,
	); err != nil {
		t.Fatalf("provision paused VM: %v", err)
	}
	if api.startCalls != 1 || api.execCalls != 1 {
		t.Fatalf("paused VM starts = %d execs = %d, want 1 and 1", api.startCalls, api.execCalls)
	}
}

func TestProviderProvisionReportsFailedDaemonInstall(t *testing.T) {
	failedExit := 1
	for _, test := range []struct {
		name     string
		response execVMResponse
		want     string
	}{
		{name: "non-zero exit", response: execVMResponse{StatusCode: &failedExit}, want: "exited with status 1"},
		{name: "timeout", response: execVMResponse{}, want: "timed out"},
	} {
		t.Run(test.name, func(t *testing.T) {
			running := ownedVM(t, vmStateRunning, 2, 4096)
			api := &fakeAPI{
				getFunc:      func(string) (vm, bool, error) { return running, true, nil },
				execResponse: test.response,
			}
			result, err := newTestProvider(api).ProvisionMachine(
				context.Background(),
				testInstallationID(),
				testMachineID(),
				testProvisioning(t),
				"machine-token",
				nil,
				true,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("daemon install error = %v, want %q", err, test.want)
			}
			if result.ProviderResourceID != running.ID {
				t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, running.ID)
			}
		})
	}
}

func TestProviderWakeRequestsSandboxURL(t *testing.T) {
	for _, test := range []struct {
		status  int
		wantErr bool
	}{
		{status: http.StatusNoContent},
		{status: http.StatusBadGateway, wantErr: true},
	} {
		methods := make(chan string, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			methods <- r.Method
			w.WriteHeader(test.status)
		}))
		err := (&provider{wakeTransport: server.Client().Transport}).WakeMachine(
			context.Background(),
			providers.WakeMachineInput{SandboxURL: server.URL},
		)
		server.Close()
		if (err != nil) != test.wantErr {
			t.Fatalf("wake with HTTP %d error = %v", test.status, err)
		}
		if method := <-methods; method != http.MethodGet {
			t.Fatalf("wake method = %s, want GET", method)
		}
	}
}

func TestProviderDeleteChecksOwnership(t *testing.T) {
	foreign := ownedVM(t, vmStateRunning, 2, 4096)
	foreign.Metadata[installationMarkerKey] = "someone-else"
	api := &fakeAPI{getFunc: func(string) (vm, bool, error) { return foreign, true, nil }}
	err := newTestProvider(api).DeleteMachine(
		context.Background(),
		testInstallationID(),
		testMachineID(),
		testProvisioning(t),
		foreign.ID,
	)
	if err == nil || !strings.Contains(err.Error(), "expected ownership metadata") || api.deleteCalls != 0 {
		t.Fatalf("foreign delete error = %v, delete calls = %d", err, api.deleteCalls)
	}
}
