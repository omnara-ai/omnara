package agent37

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func provisionTestMachine(
	t *testing.T,
	api *fakeAPI,
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
	machineEnv map[string]string,
) (providers.ProvisionMachineResult, error) {
	t.Helper()
	return newTestProvider(api).ProvisionMachine(
		context.Background(), installationID, machineID, machineProvisioning, "machine-token", machineEnv, true,
	)
}

func smallMachine(t *testing.T) executionstore.MachineProvisioningConfig {
	t.Helper()
	return testMachineProvisioning(t, testTemplate, 2, 4096)
}

func provisionLaterAttempt(
	t *testing.T,
	api *fakeAPI,
	installationID, machineID uuid.UUID,
) (providers.ProvisionMachineResult, error) {
	t.Helper()
	return newTestProvider(api).ProvisionMachine(
		context.Background(), installationID, machineID, smallMachine(t),
		"machine-token", nil, false,
	)
}

func TestAgent37PrepareProvisioningKeepsConfiguredResources(t *testing.T) {
	api := newFakeAPI()
	facts, err := newTestProvider(api).PrepareProvisioning(
		context.Background(),
		testMachineProvisioning(t, testTemplate, 4, 16384),
	)
	if err != nil {
		t.Fatalf("prepare provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 4 || facts.MemoryMB == nil || *facts.MemoryMB != 16384 {
		t.Fatalf("resource facts = %+v, want the configured values", facts)
	}
	if _, err := newTestProvider(api).PrepareProvisioning(
		context.Background(),
		testMachineProvisioning(t, testTemplate, 3, 4096),
	); err == nil {
		t.Fatal("prepare provisioning accepted a size Agent37 does not sell")
	}
}

func TestAgent37CheckConnectionChecksAPIAccess(t *testing.T) {
	api := newFakeAPI()
	p := newTestProvider(api)
	if err := p.CheckConnection(context.Background(), executionstore.MachineProvisioningConfig{}); err != nil ||
		api.templatesCalls != 1 {
		t.Fatalf("check connection error = %v template calls = %d, want one template list", err, api.templatesCalls)
	}
	api.templatesErr = apiError{StatusCode: http.StatusUnauthorized}
	if err := p.CheckConnection(context.Background(), executionstore.MachineProvisioningConfig{}); !errors.Is(
		err,
		api.templatesErr,
	) {
		t.Fatalf("check connection with a rejected key = %v", err)
	}
}

func TestAgent37ProvisionMachineDefaultsToTheOmnaraTemplate(t *testing.T) {
	api := newFakeAPI()
	_, err := provisionTestMachine(
		t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", 2, 4096), nil,
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if api.createRequest.Template != defaultTemplate {
		t.Fatalf("create template = %q, want %q", api.createRequest.Template, defaultTemplate)
	}
}

func TestAgent37ProvisionMachineCreatesTheInstanceAndStartsTheDaemon(t *testing.T) {
	api := newFakeAPI()
	api.created.Resources = instanceResources{CPU: 4, MemoryGB: 16}
	installationID := uuid.New()
	machineID := uuid.New()
	provisioning := testMachineProvisioning(t, testTemplate, 4, 16384)
	result, err := provisionTestMachine(
		t, api, installationID, machineID, provisioning,
		map[string]string{"TEAM": "platform", "my-var": "dropped"},
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "a37inst001" || result.SandboxURL != "" {
		t.Fatalf("provision result = %+v, want a37inst001 without a sandbox url", result)
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := ownershipMetadata(installationID, machineID)
	if err != nil {
		t.Fatal(err)
	}
	request := api.createRequest
	if api.createCalls != 1 || request.Name != name || request.Template != testTemplate ||
		request.Resources != (instanceResources{CPU: 4, MemoryGB: 16}) ||
		len(request.Metadata) != 2 || request.Metadata[installationLabel] != metadata[installationLabel] ||
		request.Metadata[machineLabel] != metadata[machineLabel] {
		t.Fatalf("create calls = %d request = %+v", api.createCalls, request)
	}
	if !slices.Equal(api.execIDs, []string{"a37inst001"}) || api.startCalls != 0 || len(api.getLookups) != 0 {
		t.Fatalf("exec ids = %v start calls = %d lookups = %v, want one exec on the running instance",
			api.execIDs, api.startCalls, api.getLookups)
	}
	command := api.execCommands[0]
	for _, want := range []string{
		"export OMNARA_API_URL='https://api.omnara.test/v1'\n",
		"export OMNARA_INSTALLER_URL='https://api.omnara.test/install/omnarad.sh'\n",
		"export OMNARA_MACHINE_TOKEN='machine-token'\n",
		"export " + providers.ManagedBootstrapScriptEnvVar + "='" + providers.ManagedBootScriptPayload() + "'\n",
		"export TEAM='platform'\n",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("launch command %q does not contain %q", command, want)
		}
	}
	if strings.Contains(command, "my-var") {
		t.Fatalf("launch command %q exports a name the shell cannot hold", command)
	}
}

func TestAgent37DaemonLaunchCommandRunsTheDetachedLauncher(t *testing.T) {
	command := daemonLaunchCommand(map[string]string{"B": "it's", "A": "1"})
	wantPrefix := "export A='1'\nexport B='it'\\''s'\nexec /bin/sh -c "
	wantSuffix := " omnara-launch " + shellQuote(providers.ManagedDaemonLauncherArgs()[2])
	if !strings.HasPrefix(command, wantPrefix) || !strings.HasSuffix(command, wantSuffix) {
		t.Fatalf("launch command = %q, want sorted quoted exports and the managed launcher as $1", command)
	}
	if !strings.Contains(command, shellQuote(daemonLauncherScript())) {
		t.Fatal("launch command does not run the launcher script")
	}
	script := daemonLauncherScript()
	for _, want := range []string{
		`kill -0 "$p"`,
		"grep -q omnarad",
		`d=${OMNARA_HOME:-${HOME:-/tmp}}/.omnara-agent37`,
		`if command -v setsid >/dev/null 2>&1; then detach=setsid; fi`,
		`$detach /bin/sh -c 'trap "" HUP; echo $$ >"$1/pid" && exec /bin/sh -c "$2"' omnara-daemon "$d" "$1"`,
		`</dev/null >>"$d/log" 2>&1 &`,
		"omnara daemon exited early",
		`cat "$d/log" >&2`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("launcher script does not contain %q", want)
		}
	}
}

func TestAgent37ValidateMachineConfigBoundsTheLaunchCommand(t *testing.T) {
	p := newTestProvider(newFakeAPI())
	provisioning := smallMachine(t)
	if err := p.ValidateMachineConfig(provisioning, map[string]string{"TEAM": "platform"}); err != nil {
		t.Fatalf("validate machine config: %v", err)
	}
	for name, value := range map[string]string{
		"plain":   strings.Repeat("x", maxExecRequestBytes),
		"escaped": strings.Repeat("\"\n", maxExecRequestBytes/3),
	} {
		err := p.ValidateMachineConfig(provisioning, map[string]string{"BIG": value})
		if err == nil || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("%s: validate oversized machine config = %v, want it refused", name, err)
		}
	}
	if err := p.ValidateMachineConfig(provisioning, map[string]string{"OMNARA_API_URL": "x"}); err == nil {
		t.Fatal("validate machine config accepted a reserved OMNARA_ name")
	}
}

func TestAgent37ProvisionMachineAdoptsAnOwnedInstance(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.instances = []instance{
		{ID: "a37unowned", Status: instanceStatusRunning, Metadata: map[string]any{"team": map[string]any{"nested": true}}},
		ownedInstance(t, "a37other01", instanceStatusRunning, uuid.New(), machineID),
		ownedInstance(t, "a37exists1", instanceStatusRunning, installationID, machineID),
	}
	result, err := provisionLaterAttempt(t, api, installationID, machineID)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "a37exists1" || api.createCalls != 0 ||
		!slices.Equal(api.execIDs, []string{"a37exists1"}) {
		t.Fatalf("result = %+v create calls = %d exec ids = %v, want adoption without create",
			result, api.createCalls, api.execIDs)
	}
}

func TestAgent37LostCreateResponseNeverCreatesADuplicate(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.createErr = errors.New("connection reset")
	api.createErrCreates = true
	p := newTestProvider(api)
	provision := func(firstAttempt bool) (providers.ProvisionMachineResult, error) {
		return p.ProvisionMachine(
			context.Background(), installationID, machineID, smallMachine(t),
			"machine-token", nil, firstAttempt,
		)
	}
	if _, err := provision(true); err == nil || !strings.Contains(err.Error(), "connection reset") || api.listCalls != 0 {
		t.Fatalf("first attempt error = %v list calls = %d, want the create error without a lookup", err, api.listCalls)
	}
	hidden := api.instances
	api.instances = nil
	for _, firstAttempt := range []bool{true, false} {
		if _, err := provision(firstAttempt); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
			t.Fatalf("retry before the instance is listed = %v, want the outcome treated as unknown", err)
		}
	}
	api.instances = hidden
	result, err := provision(true)
	if err != nil || result.ProviderResourceID != "a37inst001" || api.createCalls != 1 || len(api.execIDs) != 1 {
		t.Fatalf("result = %+v error = %v create calls = %d exec ids = %v, want the created instance adopted",
			result, err, api.createCalls, api.execIDs)
	}
}

func TestAgent37ProvisionMachineClassifiesCreateFailures(t *testing.T) {
	for name, test := range map[string]struct {
		err       error
		permanent bool
		recreates bool
	}{
		"rate limited": {err: providers.WithResponse(apiError{StatusCode: http.StatusTooManyRequests}, providers.HTTPResponse{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}}), recreates: true},
		"no capacity":  {err: apiError{StatusCode: http.StatusServiceUnavailable, Code: "no_capacity"}, recreates: true},
		"unavailable":  {err: apiError{StatusCode: http.StatusServiceUnavailable}},
		"unreachable":  {err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, recreates: true},
		"server error": {err: apiError{StatusCode: http.StatusInternalServerError}},
		"transport":    {err: errors.New("connection reset")},
		"bad template": {err: apiError{StatusCode: http.StatusBadRequest, Code: "invalid_request"}, permanent: true},
		"bad api key":  {err: apiError{StatusCode: http.StatusUnauthorized}, permanent: true},
		"unfunded":     {err: apiError{StatusCode: http.StatusPaymentRequired, Code: "insufficient_balance"}, permanent: true},
		"tier limit":   {err: apiError{StatusCode: http.StatusForbidden, Code: "tier_limit"}, permanent: true},
		"at the cap":   {err: apiError{StatusCode: http.StatusConflict, Code: "instance_limit_reached"}, permanent: true},
	} {
		api := newFakeAPI()
		api.createErr = test.err
		installationID, machineID := uuid.New(), uuid.New()
		p := newTestProvider(api)
		result, err := p.ProvisionMachine(
			context.Background(), installationID, machineID, smallMachine(t),
			"machine-token", nil, true,
		)
		if !errors.Is(err, test.err) || result.ProviderResourceID != "" || len(api.execIDs) != 0 {
			t.Fatalf("%s: result = %+v error = %v exec ids = %v", name, result, err, api.execIDs)
		}
		if errors.Is(err, providers.ErrPermanent) != test.permanent {
			t.Fatalf("%s: error = %v, want permanent %t", name, err, test.permanent)
		}
		if name == "rate limited" {
			if delay, ok := providers.RetryAfter(err); !ok || delay != 30*time.Second {
				t.Fatalf("%s: retry after = %v %v, want the hint preserved", name, delay, ok)
			}
		}
		api.createErr = nil
		_, err = p.ProvisionMachine(
			context.Background(), installationID, machineID, smallMachine(t),
			"machine-token", nil, true,
		)
		wantCreates := 1
		if test.recreates {
			wantCreates = 2
		}
		if api.createCalls != wantCreates || test.recreates != (err == nil) {
			t.Fatalf("%s: retry error = %v create calls = %d, want %d", name, err, api.createCalls, wantCreates)
		}
	}
}

func TestAgent37ProvisionMachineWaitsForAStartingInstance(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	provisioning := ownedInstance(t, "a37booting", instanceStatusProvisioning, installationID, machineID)
	running := provisioning
	running.Status = instanceStatusRunning
	api.instances = []instance{provisioning}
	api.getErrs = []error{apiError{StatusCode: http.StatusBadGateway}}
	api.getResults = []instance{provisioning, running}
	result, err := provisionLaterAttempt(t, api, installationID, machineID)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "a37booting" || api.startCalls != 0 || len(api.execIDs) != 1 ||
		!slices.Equal(api.getLookups, []string{"a37booting", "a37booting", "a37booting"}) {
		t.Fatalf("result = %+v start calls = %d exec ids = %v lookups = %v, want the instance awaited until running",
			result, api.startCalls, api.execIDs, api.getLookups)
	}
}

func TestAgent37ProvisionMachineStartsAStoppedOrSleepingInstance(t *testing.T) {
	for _, status := range []instanceStatus{instanceStatusStopped, instanceStatusSleeping} {
		api := newFakeAPI()
		installationID := uuid.New()
		machineID := uuid.New()
		idle := ownedInstance(t, "a37idle001", status, installationID, machineID)
		running := idle
		running.Status = instanceStatusRunning
		api.instances = []instance{idle}
		api.startErrs = []error{apiError{StatusCode: http.StatusConflict, Code: "try_again"}}
		api.getResults = []instance{idle, running}
		result, err := provisionLaterAttempt(t, api, installationID, machineID)
		if err != nil || result.ProviderResourceID != "a37idle001" || api.startCalls != 2 || len(api.execIDs) != 1 {
			t.Fatalf("%s: result = %+v error = %v start calls = %d exec ids = %v, want a retried start then the daemon",
				status, result, err, api.startCalls, api.execIDs)
		}
	}

	api := newFakeAPI()
	api.created.Status = instanceStatusStopped
	api.startErrs = []error{apiError{StatusCode: http.StatusPaymentRequired, Code: "insufficient_balance"}}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), smallMachine(t), nil)
	if apiStatusCode(err) != http.StatusPaymentRequired || result.ProviderResourceID != "a37inst001" ||
		len(api.execIDs) != 0 {
		t.Fatalf("result = %+v error = %v exec ids = %v, want the owned id kept with the failure",
			result, err, api.execIDs)
	}
}

func TestAgent37ProvisionMachineBoundsTheWaitByContext(t *testing.T) {
	api := newFakeAPI()
	api.created.Status = instanceStatusRestarting
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := newTestProvider(api).ProvisionMachine(
		ctx, uuid.New(), uuid.New(), smallMachine(t), "t", nil, true,
	)
	if !errors.Is(err, context.DeadlineExceeded) || result.ProviderResourceID != "a37inst001" ||
		len(api.execIDs) != 0 || len(api.getLookups) < 2 {
		t.Fatalf("result = %+v error = %v exec ids = %v lookups = %d, want polling bounded by the context",
			result, err, api.execIDs, len(api.getLookups))
	}
}

func TestAgent37ProvisionMachineFailsPermanentlyOnAFailedInstance(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.instances = []instance{ownedInstance(t, "a37failed1", instanceStatusFailed, installationID, machineID)}
	result, err := provisionLaterAttempt(t, api, installationID, machineID)
	if !errors.Is(err, providers.ErrPermanent) || result.ProviderResourceID != "a37failed1" ||
		api.deleteCalls != 0 || api.createCalls != 0 || len(api.execIDs) != 0 {
		t.Fatalf("result = %+v error = %v delete calls = %d create calls = %d exec ids = %v",
			result, err, api.deleteCalls, api.createCalls, api.execIDs)
	}

	api = newFakeAPI()
	api.created.Status = instanceStatusProvisioning
	api.getFound = false
	result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), smallMachine(t), nil)
	if err == nil || !strings.Contains(err.Error(), "disappeared") || result.ProviderResourceID != "a37inst001" {
		t.Fatalf("result = %+v error = %v, want a vanished instance reported", result, err)
	}
}

func TestAgent37ProvisionMachineRefusesForeignMismatchedAndDuplicateInstances(t *testing.T) {
	api := newFakeAPI()
	api.created.Metadata = map[string]any{installationLabel: "ins_other", machineLabel: "mch_other"}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), smallMachine(t), nil)
	if err == nil || !strings.Contains(err.Error(), "ownership") || result.ProviderResourceID != "" ||
		len(api.execIDs) != 0 {
		t.Fatalf("result = %+v error = %v exec ids = %v, want a foreign instance refused", result, err, api.execIDs)
	}

	for field, resources := range map[string]instanceResources{
		"vcpu":   {CPU: 4, MemoryGB: 4},
		"memory": {CPU: 2, MemoryGB: 8},
	} {
		api = newFakeAPI()
		api.created.Resources = resources
		result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), smallMachine(t), nil)
		if !errors.Is(err, providers.ErrPermanent) || !strings.Contains(err.Error(), field) ||
			result.ProviderResourceID != "a37inst001" || len(api.execIDs) != 0 {
			t.Fatalf("%s: result = %+v error = %v exec ids = %v, want the mismatch reported",
				field, result, err, api.execIDs)
		}
	}

	api = newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.instances = []instance{
		ownedInstance(t, "a37dupe001", instanceStatusRunning, installationID, machineID),
		ownedInstance(t, "a37dupe002", instanceStatusRunning, installationID, machineID),
	}
	result, err = provisionLaterAttempt(t, api, installationID, machineID)
	if err == nil || !strings.Contains(err.Error(), "multiple") || result.ProviderResourceID != "" ||
		api.createCalls != 0 || len(api.execIDs) != 0 {
		t.Fatalf("result = %+v error = %v create calls = %d exec ids = %v",
			result, err, api.createCalls, api.execIDs)
	}
}

func TestAgent37ProvisionMachineReportsDaemonLauncherFailures(t *testing.T) {
	api := newFakeAPI()
	api.execErrs = []error{apiError{StatusCode: http.StatusBadGateway}}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), smallMachine(t), nil)
	if apiStatusCode(err) != http.StatusBadGateway || result.ProviderResourceID != "a37inst001" {
		t.Fatalf("result = %+v error = %v", result, err)
	}

	api = newFakeAPI()
	api.execResults = []execResult{{ExitCode: 1, Stderr: strings.Repeat("x", 3000) + "omnara daemon exited early"}}
	result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), smallMachine(t), nil)
	if err == nil || !strings.Contains(err.Error(), "status 1") || !strings.Contains(err.Error(), "exited early") ||
		len(err.Error()) > maxReportedOutputBytes+128 || result.ProviderResourceID != "a37inst001" {
		t.Fatalf("result = %+v error length = %d error = %.80s", result, len(err.Error()), err)
	}
}

func TestAgent37InspectMachineChecksOwnership(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	inspect := func(api *fakeAPI, resourceID string) (string, bool, error) {
		return newTestProvider(api).InspectMachine(
			context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, resourceID,
		)
	}

	api := newFakeAPI()
	api.created = ownedInstance(t, "a37inst001", instanceStatusFailed, installationID, machineID)
	if id, found, err := inspect(api, "a37inst001"); err != nil || !found || id != "a37inst001" {
		t.Fatalf("inspect failed instance by id = %q found %v error %v, want it reported for deletion", id, found, err)
	}

	api.created = ownedInstance(t, "a37inst001", instanceStatusRunning, uuid.New(), machineID)
	if _, found, err := inspect(api, "a37inst001"); err == nil || found {
		t.Fatalf("inspect foreign instance = found %v error %v, want a refusal", found, err)
	}

	api.created = ownedInstance(t, "a37inst002", instanceStatusRunning, installationID, machineID)
	if _, found, err := inspect(api, "a37inst001"); err == nil || found {
		t.Fatalf("inspect mismatched id = found %v error %v, want a refusal", found, err)
	}

	api = newFakeAPI()
	api.getFound = false
	if _, found, err := inspect(api, "a37inst001"); err != nil || found {
		t.Fatalf("inspect missing instance = found %v error %v, want absent", found, err)
	}

	api = newFakeAPI()
	api.instances = []instance{ownedInstance(t, "a37found01", instanceStatusStopped, installationID, machineID)}
	if id, found, err := inspect(api, ""); err != nil || !found || id != "a37found01" || len(api.getLookups) != 0 {
		t.Fatalf("inspect by ownership = %q found %v error %v lookups %v", id, found, err, api.getLookups)
	}
	if _, found, err := inspect(newFakeAPI(), ""); err != nil || found {
		t.Fatalf("inspect by ownership without instances = found %v error %v", found, err)
	}
}

func TestAgent37DeleteMachine(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	deleteMachine := func(api *fakeAPI, resourceID string) error {
		return newTestProvider(api).DeleteMachine(
			context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, resourceID,
		)
	}

	if err := deleteMachine(newFakeAPI(), ""); err == nil {
		t.Fatal("delete machine accepted an empty resource id")
	}

	api := newFakeAPI()
	api.created = ownedInstance(t, "a37inst001", instanceStatusFailed, installationID, machineID)
	if err := deleteMachine(api, "a37inst001"); err != nil || api.deletedID != "a37inst001" {
		t.Fatalf("delete owned instance error = %v deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.created = ownedInstance(t, "a37inst001", instanceStatusRunning, uuid.New(), machineID)
	if err := deleteMachine(api, "a37inst001"); err == nil || api.deleteCalls != 0 {
		t.Fatalf("delete foreign instance error = %v delete calls %d, want a refusal", err, api.deleteCalls)
	}

	api = newFakeAPI()
	api.getFound = false
	api.instances = []instance{ownedInstance(t, "a37replace", instanceStatusRunning, installationID, machineID)}
	if err := deleteMachine(api, "a37stale01"); err != nil || api.deletedID != "a37replace" {
		t.Fatalf("delete by ownership fallback error = %v deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.getFound = false
	if err := deleteMachine(api, "a37gone001"); err != nil || api.deleteCalls != 0 {
		t.Fatalf("delete missing instance error = %v delete calls %d, want a quiet no-op", err, api.deleteCalls)
	}
}
