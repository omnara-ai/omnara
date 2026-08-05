package unikraft

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/ssrf"
)

func sleepEnabledOverride(sleepAfterMS int) map[string]any {
	return map[string]any{"provider_options": map[string]any{"sleep_after_ms": sleepAfterMS}}
}

func instanceWithFQDN(uuid, name, fqdn string) instance {
	return instance{
		UUID:         uuid,
		Name:         name,
		ServiceGroup: &serviceGroup{Domains: []domain{{FQDN: fqdn}}},
	}
}

func TestProvisionWithSleepEnabled(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	name := mustInstanceName(t, machineID)
	api := &fakeAPI{
		instancesByName: map[string]instance{},
		instancesByUUID: map[string]instance{
			"uuid-" + name: instanceWithFQDN(
				"uuid-"+name,
				name,
				"sleepy-machine.fra0.unikraft.app.",
			),
		},
	}
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, sleepEnabledOverride(45_000)),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision sleep-enabled machine: %v", err)
	}
	if result.SandboxURL != "https://sleepy-machine.fra0.unikraft.app/" {
		t.Fatalf("sandbox url = %q", result.SandboxURL)
	}
	if len(api.createRequests) != 1 {
		t.Fatalf("create requests = %d, want 1", len(api.createRequests))
	}
	create := api.createRequests[0]
	if create.ScaleToZero == nil || create.ScaleToZero.Policy != "on" ||
		!create.ScaleToZero.Stateful ||
		create.ScaleToZero.CooldownTimeMS != scaleToZeroCooldownMS {
		t.Fatalf("scale_to_zero = %+v", create.ScaleToZero)
	}
	if create.ServiceGroup == nil || len(create.ServiceGroup.Services) != 1 {
		t.Fatalf("service_group = %+v", create.ServiceGroup)
	}
	service := create.ServiceGroup.Services[0]
	if service.Port != 443 || service.DestinationPort != daemonprotocol.WakeListenerPort ||
		len(service.Handlers) != 2 || service.Handlers[0] != "http" ||
		service.Handlers[1] != "tls" {
		t.Fatalf("wake service = %+v", service)
	}
	if create.Env[daemonprotocol.SleepAfterEnvVar] != "45000" ||
		create.Env[daemonprotocol.WakeListenAddrEnvVar] != ":"+
			strconv.Itoa(daemonprotocol.WakeListenerPort) ||
		create.Env[daemonprotocol.SleepPlatformEnvVar] != daemonprotocol.SleepPlatformUnikraft {
		t.Fatalf("sleep env = %+v", create.Env)
	}
	if len(create.Args) != 3 || create.Args[0] != "/bin/sh" || create.Args[1] != "-c" {
		t.Fatalf("create args = %#v, want shell command", create.Args)
	}
	command := create.Args[2]
	if strings.ContainsAny(command, " \t\r\n") {
		t.Fatalf("sleep-enabled launch command contains literal whitespace: %q", command)
	}
	if !strings.HasPrefix(command, unikraftScaleToZeroBootGuardScript()) {
		t.Fatalf("launch command missing leading scale-to-zero guard: %s", command)
	}
	if output, err := exec.Command("/bin/sh", "-n", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("invalid sleep-enabled launch command: %v\n%s", err, output)
	}
}

func TestProvisionWithSleepDisabledOmitsSleepSurface(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-000000000014")
	api := &fakeAPI{instancesByName: map[string]instance{}}
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, nil),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision sleep-disabled machine: %v", err)
	}
	if result.SandboxURL != "" {
		t.Fatalf("sandbox url = %q, want empty", result.SandboxURL)
	}
	create := api.createRequests[0]
	if create.ScaleToZero != nil || create.ServiceGroup != nil {
		t.Fatalf("sleep surface = %+v/%+v", create.ScaleToZero, create.ServiceGroup)
	}
	if strings.Contains(create.Args[2], daemonprotocol.UnikraftScaleToZeroControlFilePath) {
		t.Fatalf("sleep-disabled launch command contains scale-to-zero guard: %s", create.Args[2])
	}
	for _, key := range []string{
		daemonprotocol.SleepAfterEnvVar,
		daemonprotocol.WakeListenAddrEnvVar,
		daemonprotocol.SleepPlatformEnvVar,
	} {
		if _, ok := create.Env[key]; ok {
			t.Fatalf("env %s must be omitted: %+v", key, create.Env)
		}
	}
}

func TestProvisionSleepRecoversSandboxURLForExistingInstance(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-000000000017")
	name := mustInstanceName(t, machineID)
	api := &fakeAPI{
		instancesByName: map[string]instance{
			name: {UUID: "uuid-existing", Name: name},
		},
		instancesByUUID: map[string]instance{
			"uuid-existing": instanceWithFQDN(
				"uuid-existing",
				name,
				"recovered.fra0.unikraft.app",
			),
		},
	}
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, sleepEnabledOverride(30_000)),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision existing sleep-enabled machine: %v", err)
	}
	if len(api.createRequests) != 0 {
		t.Fatalf("create requests = %d, want 0", len(api.createRequests))
	}
	if result.ProviderResourceID != "uuid-existing" ||
		result.SandboxURL != "https://recovered.fra0.unikraft.app/" {
		t.Fatalf("result = %+v", result)
	}
}

func TestProvisionSleepFailsWithoutFQDN(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-00000000001a")
	name := mustInstanceName(t, machineID)
	api := &fakeAPI{
		instancesByName: map[string]instance{},
		instancesByUUID: map[string]instance{
			"uuid-" + name: {UUID: "uuid-" + name, Name: name},
		},
	}
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, sleepEnabledOverride(30_000)),
		"machine-token",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "fqdn") {
		t.Fatalf("missing fqdn error = %v", err)
	}
	if result.ProviderResourceID != "uuid-"+name {
		t.Fatalf("provider resource id = %q, want %q", result.ProviderResourceID, "uuid-"+name)
	}
}

func TestSleepAfterBounds(t *testing.T) {
	if _, err := parseProviderOptions(
		testMachineProvisioning(t, sleepEnabledOverride(5_000)).ProviderOptions,
	); err == nil || !strings.Contains(err.Error(), "sleep_after_ms") {
		t.Fatalf("sleep floor error = %v", err)
	}
	if _, err := parseProviderOptions(
		testMachineProvisioning(t, sleepEnabledOverride(30_000)).ProviderOptions,
	); err != nil {
		t.Fatalf("minimum sleep_after_ms rejected: %v", err)
	}
	if _, err := parseProviderOptions(
		testMachineProvisioning(
			t,
			sleepEnabledOverride(int(daemonprotocol.MaximumSleepAfterMS)),
		).ProviderOptions,
	); err != nil {
		t.Fatalf("maximum sleep_after_ms rejected: %v", err)
	}
	if _, err := parseProviderOptions(
		testMachineProvisioning(
			t,
			sleepEnabledOverride(int(daemonprotocol.MaximumSleepAfterMS+1)),
		).ProviderOptions,
	); err == nil || !strings.Contains(err.Error(), "sleep_after_ms must be at most") {
		t.Fatalf("sleep ceiling error = %v", err)
	}
}

func TestProvisionSleepRecoversSandboxURLAfterCreateError(t *testing.T) {
	machineID := uuid.MustParse("00000000-0000-0000-0000-00000000001d")
	name := mustInstanceName(t, machineID)
	api := &fakeAPI{
		instancesByName: map[string]instance{},
		instancesByUUID: map[string]instance{
			"uuid-" + name: instanceWithFQDN(
				"uuid-"+name,
				name,
				"raced.fra0.unikraft.app.",
			),
		},
		createErr:                   errors.New("simulated create conflict"),
		createErrStillCreatesRecord: true,
	}
	provider := newTestProvider(api)

	result, err := provider.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testMachineProvisioning(t, sleepEnabledOverride(30_000)),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision after create error: %v", err)
	}
	if result.ProviderResourceID != "uuid-"+name ||
		result.SandboxURL != "https://raced.fra0.unikraft.app/" {
		t.Fatalf("result = %+v", result)
	}
}

func TestWakeMachineRejectsRedirectWithoutFollowingIt(t *testing.T) {
	methods := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods <- r.Method
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()

	err := (&provider{wakeTransport: server.Client().Transport}).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{SandboxURL: server.URL},
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected HTTP status 302") {
		t.Fatalf("wake machine error = %v, want unexpected HTTP status 302", err)
	}
	if method := <-methods; method != http.MethodGet {
		t.Fatalf("method = %s, want GET", method)
	}
	if requests := len(methods); requests != 0 {
		t.Fatalf("follow-up requests = %d, want 0", requests)
	}
}

func TestWakeMachineAcceptsNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := (&provider{wakeTransport: server.Client().Transport}).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{SandboxURL: server.URL},
	); err != nil {
		t.Fatalf("wake machine: %v", err)
	}
}

func TestWakeMachineRejectsLoopback(t *testing.T) {
	err := (&provider{}).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{SandboxURL: "http://127.0.0.1"},
	)
	if !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Fatalf("wake machine error = %v, want ErrBlockedAddress", err)
	}
}

func TestWakeMachineRequiresSandboxURL(t *testing.T) {
	err := (&provider{}).WakeMachine(context.Background(), providers.WakeMachineInput{})
	if err == nil {
		t.Fatal("missing sandbox url must fail")
	}
}
