package superserve

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
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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

func provisionLaterAttempt(
	t *testing.T,
	api *fakeAPI,
	installationID, machineID uuid.UUID,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (providers.ProvisionMachineResult, error) {
	t.Helper()
	return newTestProvider(api).ProvisionMachine(
		context.Background(), installationID, machineID, machineProvisioning, "machine-token", nil, false,
	)
}

func sizedProvisioning(t *testing.T, templateName string, cpu, memoryMB int) executionstore.MachineProvisioningConfig {
	t.Helper()
	provisioning := testMachineProvisioning(t, templateName, "")
	provisioning.CPU = &cpu
	provisioning.MemoryMB = &memoryMB
	return provisioning
}

func TestSuperservePrepareProvisioningKeepsConfiguredResources(t *testing.T) {
	api := newFakeAPI()
	facts, err := newTestProvider(api).PrepareProvisioning(context.Background(), sizedProvisioning(t, "", 4, 8192))
	if err != nil {
		t.Fatalf("prepare provisioning: %v", err)
	}
	if facts.CPU == nil || *facts.CPU != 4 || facts.MemoryMB == nil || *facts.MemoryMB != 8192 ||
		len(api.templateQueries) != 0 {
		t.Fatalf("resource facts = %+v queries = %v, want the configured values without a lookup",
			facts, api.templateQueries)
	}
}

func TestSuperserveCheckConnectionOnlyChecksAPIAccess(t *testing.T) {
	api := newFakeAPI()
	api.templates = nil
	err := newTestProvider(api).CheckConnection(context.Background(), testMachineProvisioning(t, "acme/missing", ""))
	if err != nil || !slices.Equal(api.templateQueries, []string{""}) {
		t.Fatalf("check connection error = %v queries = %v, want one unfiltered template list", err, api.templateQueries)
	}

	api = newFakeAPI()
	api.templatesErr = apiError{StatusCode: http.StatusUnauthorized}
	err = newTestProvider(api).CheckConnection(context.Background(), testMachineProvisioning(t, "", ""))
	if !errors.Is(err, api.templatesErr) {
		t.Fatalf("check connection with a rejected key = %v", err)
	}
}

func TestSuperservePrepareProvisioningResolvesTheTemplateShape(t *testing.T) {
	api := newFakeAPI()
	api.templates = []template{
		{Name: defaultTemplate, Status: templateStatusReady, VCPU: 2, MemoryMiB: 4096},
		{Name: "acme/agent-box", Status: templateStatusReady, VCPU: 2, MemoryMiB: 2048},
		{Name: "acme/agent-box-v2", Status: templateStatusReady, VCPU: 4, MemoryMiB: 4096},
	}
	for _, test := range []struct {
		templateName string
		want         [2]int
	}{
		{"", [2]int{2, 4096}},
		{"acme/agent-box", [2]int{2, 2048}},
		{"acme/agent-box-v2", [2]int{4, 4096}},
	} {
		facts, err := newTestProvider(api).PrepareProvisioning(
			context.Background(),
			testMachineProvisioning(t, test.templateName, ""),
		)
		if err != nil {
			t.Fatalf("prepare provisioning %q: %v", test.templateName, err)
		}
		if facts.CPU == nil || *facts.CPU != test.want[0] || facts.MemoryMB == nil || *facts.MemoryMB != test.want[1] {
			t.Fatalf("%q resource facts = %+v, want %v", test.templateName, facts, test.want)
		}
	}
	if !slices.Equal(api.templateQueries, []string{defaultTemplate, "acme/agent-box", "acme/agent-box-v2"}) {
		t.Fatalf("template queries = %v, want the template searched by name", api.templateQueries)
	}
}

func TestSuperservePrepareProvisioningRejectsUnusableTemplates(t *testing.T) {
	api := newFakeAPI()
	api.templates = []template{
		{Name: "acme/building", Status: "building", VCPU: 1, MemoryMiB: 1024},
		{Name: "acme/broken", Status: templateStatusReady},
	}
	for templateName, want := range map[string]string{
		"acme/missing":  "was not found",
		"acme/building": "is not ready",
		"acme/broken":   "invalid resources",
	} {
		_, err := newTestProvider(api).PrepareProvisioning(context.Background(), testMachineProvisioning(t, templateName, ""))
		if err == nil || !strings.Contains(err.Error(), want) || errors.Is(err, storeerr.ErrMachineProviderUnavailable) {
			t.Fatalf("prepare provisioning %q error = %v, want %q", templateName, err, want)
		}
	}
}

func TestSuperservePrepareProvisioningClassifiesProviderFailures(t *testing.T) {
	for name, test := range map[string]struct {
		err         error
		unavailable bool
	}{
		"transport":    {err: errors.New("connection reset"), unavailable: true},
		"rate limited": {err: apiError{StatusCode: http.StatusTooManyRequests}, unavailable: true},
		"server error": {err: apiError{StatusCode: http.StatusBadGateway}, unavailable: true},
		"unauthorized": {err: apiError{StatusCode: http.StatusUnauthorized}},
	} {
		api := newFakeAPI()
		api.templatesErr = test.err
		_, err := newTestProvider(api).PrepareProvisioning(context.Background(), testMachineProvisioning(t, "", ""))
		if err == nil || errors.Is(err, storeerr.ErrMachineProviderUnavailable) != test.unavailable {
			t.Fatalf("%s: prepare provisioning error = %v, want unavailable %t", name, err, test.unavailable)
		}
	}
}

func TestSuperserveProvisionMachineCreatesActivatesAndStartsTheDaemon(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	result, err := provisionTestMachine(
		t, api, installationID, machineID,
		sizedProvisioning(t, "acme/agent-box", 1, 1024),
		map[string]string{"TEAM": "platform", "my-var": "dropped"},
	)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" || result.SandboxURL != "" {
		t.Fatalf("provision result = %+v, want sb-123 without a sandbox url", result)
	}
	name, err := providers.MachineAllocationName(installationID, machineID)
	if err != nil {
		t.Fatal(err)
	}
	request := api.createRequest
	metadata := mustOwnershipMetadata(t, installationID, machineID)
	if api.createCalls != 1 || request.Name != name || request.FromTemplate != "acme/agent-box" ||
		request.Metadata[installationLabel] != metadata[installationLabel] ||
		request.Metadata[machineLabel] != metadata[machineLabel] || len(request.Metadata) != 2 {
		t.Fatalf("create calls = %d request = %+v", api.createCalls, request)
	}
	if api.activateCalls != 0 || api.execCalls != 1 {
		t.Fatalf("activate calls = %d exec calls = %d, want the create token reused for one exec",
			api.activateCalls, api.execCalls)
	}
	session := api.execSessions[0]
	if session.SandboxID != "sb-123" || session.AccessToken != "create-token" || session.RoutingHint != "create-hint" {
		t.Fatalf("exec session = %+v, want the credentials create returned", session)
	}
	exec := api.execRequests[0]
	if exec.Command != "/bin/sh" || len(exec.Args) != 4 || exec.Args[0] != "-c" ||
		exec.Args[1] != daemonLauncherScript() || exec.TimeoutSeconds != daemonExecTimeoutSeconds {
		t.Fatalf("exec request = %+v", exec)
	}
	for key, want := range map[string]string{
		"OMNARA_API_URL":                       "https://api.omnara.test/v1",
		"OMNARA_INSTALLER_URL":                 "https://api.omnara.test/install/omnarad.sh",
		"OMNARA_MACHINE_TOKEN":                 "machine-token",
		providers.ManagedBootstrapScriptEnvVar: providers.ManagedBootScriptPayload(),
		"TEAM":                                 "platform",
	} {
		if exec.Env[key] != want {
			t.Fatalf("exec env %s = %q, want %q", key, exec.Env[key], want)
		}
	}
	if _, ok := exec.Env["my-var"]; ok || len(exec.Env) != 5 {
		t.Fatalf("exec env = %v, want only shell-compatible names", exec.Env)
	}
}

func TestSuperserveProvisionMachineActivatesWhenCreateReturnsNoToken(t *testing.T) {
	api := newFakeAPI()
	api.createNoToken = true
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" || api.activateCalls != 1 || api.execCalls != 1 ||
		api.execSessions[0].AccessToken != "token-1" || api.execSessions[0].RoutingHint != "hint-token-1" {
		t.Fatalf("result = %+v activate calls = %d sessions = %+v, want one activate for the token",
			result, api.activateCalls, api.execSessions)
	}
}

func TestSuperserveDaemonLauncherScriptIsIdempotentAndDetached(t *testing.T) {
	request := daemonLauncherRequest(nil)
	launcher := providers.ManagedDaemonLauncherArgs()
	if request.Command != "/bin/sh" || len(request.Args) != 4 || request.Args[0] != "-c" ||
		request.Args[2] != "omnara-launch" || request.Args[3] != launcher[2] {
		t.Fatalf("launcher request = %+v, want the managed launcher command passed as $1", request)
	}
	script := request.Args[1]
	for _, want := range []string{
		`kill -0 "$p"`,
		"grep -q omnarad",
		"exit 0",
		`d=${OMNARA_HOME:-${HOME:-/tmp}}/.omnara-superserve`,
		`if command -v setsid >/dev/null 2>&1; then detach=setsid; fi`,
		`$detach /bin/sh -c 'trap "" HUP; echo $$ >"$1/pid" && exec /bin/sh -c "$2"' omnara-daemon "$d" "$1"`,
		`</dev/null >>"$d/log" 2>&1 &`,
		"omnara daemon exited early",
		`cat "$d/log" >&2`,
		"exit 1",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("launcher script %q does not contain %q", script, want)
		}
	}
	if strings.Contains(script, launcher[2]) {
		t.Fatal("launcher script embeds the launcher command instead of taking it as an argument")
	}
}

func TestSuperserveProvisionMachineAdoptsAnOwnedSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.sandboxes = []sandbox{
		ownedSandbox(t, "sb-other", sandboxStatusActive, uuid.New(), machineID),
		ownedSandbox(t, "sb-existing", sandboxStatusActive, installationID, machineID),
	}
	api.created = api.sandboxes[1]
	result, err := provisionLaterAttempt(t, api, installationID, machineID, testMachineProvisioning(t, "", ""))
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-existing" || api.createCalls != 0 || api.activateCalls != 1 || api.execCalls != 1 {
		t.Fatalf("result = %+v create calls = %d activate calls = %d exec calls = %d, want adoption without create",
			result, api.createCalls, api.activateCalls, api.execCalls)
	}
}

func TestSuperserveLostCreateResponseNeverCreatesADuplicate(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.createErr = errors.New("connection reset")
	api.createErrCreates = true
	p := newTestProvider(api)
	provision := func(firstAttempt bool) (providers.ProvisionMachineResult, error) {
		return p.ProvisionMachine(
			context.Background(), installationID, machineID, testMachineProvisioning(t, "", ""),
			"machine-token", nil, firstAttempt,
		)
	}
	if _, err := provision(true); err == nil || !strings.Contains(err.Error(), "connection reset") ||
		api.listCalls != 0 {
		t.Fatalf("first attempt error = %v list calls = %d, want the create error without a lookup", err, api.listCalls)
	}
	hidden := api.sandboxes
	api.sandboxes = nil
	for _, firstAttempt := range []bool{true, false} {
		if _, err := provision(firstAttempt); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
			t.Fatalf("retry before the sandbox is listed = %v, want the outcome treated as unknown", err)
		}
	}
	api.sandboxes = hidden
	result, err := provision(true)
	if err != nil || result.ProviderResourceID != "sb-123" || api.createCalls != 1 || api.execCalls != 1 {
		t.Fatalf("result = %+v error = %v create calls = %d exec calls = %d, want the created sandbox adopted",
			result, err, api.createCalls, api.execCalls)
	}
	if _, err := newTestProvider(api).ProvisionMachine(
		context.Background(), uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "machine-token", nil, false,
	); err == nil || api.createCalls != 1 {
		t.Fatalf("later attempt error = %v create calls = %d, want no create", err, api.createCalls)
	}
}

func TestSuperserveProvisionMachineClassifiesCreateFailures(t *testing.T) {
	for name, test := range map[string]struct {
		err       error
		permanent bool
		recreates bool
	}{
		"capacity":      {err: providers.WithResponse(apiError{StatusCode: http.StatusTooManyRequests, Code: "too_many_sandboxes"}, providers.HTTPResponse{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}}), recreates: true},
		"outage":        {err: apiError{StatusCode: http.StatusServiceUnavailable}},
		"unreachable":   {err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, recreates: true},
		"conflict":      {err: apiError{StatusCode: http.StatusConflict}},
		"transport":     {err: errors.New("connection reset")},
		"bad template":  {err: apiError{StatusCode: http.StatusNotFound, Code: "not_found"}, permanent: true},
		"bad request":   {err: apiError{StatusCode: http.StatusBadRequest}, permanent: true},
		"bad api key":   {err: apiError{StatusCode: http.StatusUnauthorized}, permanent: true},
		"not permitted": {err: apiError{StatusCode: http.StatusForbidden}, permanent: true},
	} {
		api := newFakeAPI()
		api.createErr = test.err
		installationID, machineID := uuid.New(), uuid.New()
		p := newTestProvider(api)
		result, err := p.ProvisionMachine(
			context.Background(), installationID, machineID, testMachineProvisioning(t, "", ""),
			"machine-token", nil, true,
		)
		if !errors.Is(err, test.err) || result.ProviderResourceID != "" || api.execCalls != 0 {
			t.Fatalf("%s: result = %+v error = %v exec calls = %d", name, result, err, api.execCalls)
		}
		if errors.Is(err, providers.ErrPermanent) != test.permanent {
			t.Fatalf("%s: error = %v, want permanent %t", name, err, test.permanent)
		}
		if name == "capacity" {
			if delay, ok := providers.RetryAfter(err); !ok || delay != 30*time.Second {
				t.Fatalf("%s: retry after = %v %v, want the hint preserved", name, delay, ok)
			}
		}
		api.createErr = nil
		_, err = p.ProvisionMachine(
			context.Background(), installationID, machineID, testMachineProvisioning(t, "", ""),
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

func TestSuperserveProvisionMachineWaitsForAResumingSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	paused := ownedSandbox(t, "sb-paused", sandboxStatusPaused, installationID, machineID)
	api.sandboxes = []sandbox{paused}
	api.created = paused
	resuming := paused
	resuming.Status = sandboxStatusResuming
	active := paused
	active.Status = sandboxStatusActive
	active.AccessToken = "get-token"
	api.activateResults = []sandbox{resuming}
	api.getResults = []sandbox{resuming, active}
	result, err := provisionLaterAttempt(t, api, installationID, machineID, sizedProvisioning(t, "", 1, 1024))
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-paused" || api.createCalls != 0 || api.execCalls != 1 ||
		api.execSessions[0].AccessToken != "get-token" {
		t.Fatalf("result = %+v create calls = %d exec calls = %d sessions = %+v, want the sandbox awaited until active",
			result, api.createCalls, api.execCalls, api.execSessions)
	}
	if api.activateCalls != 1 || !slices.Equal(api.getLookups, []string{"sb-paused", "sb-paused"}) {
		t.Fatalf("activate calls = %d lookups = %v", api.activateCalls, api.getLookups)
	}
}

func TestSuperserveProvisionMachineRidesOutTransientActivateFailures(t *testing.T) {
	api := newFakeAPI()
	api.created.Status = sandboxStatusPaused
	api.activateErrs = []error{
		apiError{StatusCode: http.StatusConflict},
		apiError{StatusCode: http.StatusTooManyRequests, Code: "too_many_sandboxes"},
	}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.ProviderResourceID != "sb-123" || api.activateCalls != 3 || api.execCalls != 1 {
		t.Fatalf("result = %+v activate calls = %d exec calls = %d, want activate retried",
			result, api.activateCalls, api.execCalls)
	}

	api = newFakeAPI()
	api.created.Status = sandboxStatusPaused
	api.activateErrs = []error{apiError{StatusCode: http.StatusPaymentRequired}}
	result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if apiStatusCode(err) != http.StatusPaymentRequired || result.ProviderResourceID != "sb-123" || api.execCalls != 0 {
		t.Fatalf("result = %+v error = %v exec calls = %d, want the owned id kept with the failure",
			result, err, api.execCalls)
	}
}

func TestSuperserveProvisionMachineHonorsRetryAfterFromLookups(t *testing.T) {
	api := newFakeAPI()
	api.created.Status = sandboxStatusStarting
	rateLimited := providers.WithResponse(
		apiError{StatusCode: http.StatusTooManyRequests, Code: "rate_limited"},
		providers.HTTPResponse{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"30"}}},
	)
	api.getErrs = []error{rateLimited}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := newTestProvider(api).ProvisionMachine(
		ctx, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "machine-token", nil, true,
	)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, rateLimited) ||
		len(api.getLookups) != 1 || api.activateCalls != 0 || result.ProviderResourceID != "sb-123" {
		t.Fatalf("result = %+v error = %v lookups = %d activate calls = %d, want the Retry-After wait honored",
			result, err, len(api.getLookups), api.activateCalls)
	}
}

func TestSuperserveProvisionMachineBoundsTheWaitByContext(t *testing.T) {
	api := newFakeAPI()
	api.created.Status = sandboxStatusPausing
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := newTestProvider(api).ProvisionMachine(
		ctx, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), "t", nil, true,
	)
	if !errors.Is(err, context.DeadlineExceeded) || result.ProviderResourceID != "sb-123" || api.execCalls != 0 ||
		api.activateCalls != 0 || len(api.getLookups) < 2 {
		t.Fatalf("result = %+v error = %v exec calls = %d activate calls = %d lookups = %d, "+
			"want polling bounded by the context",
			result, err, api.execCalls, api.activateCalls, len(api.getLookups))
	}
}

func TestSuperserveProvisionMachineFailsPermanentlyOnAFailedSandbox(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.sandboxes = []sandbox{ownedSandbox(t, "sb-dead", sandboxStatusFailed, installationID, machineID)}
	result, err := provisionLaterAttempt(t, api, installationID, machineID, testMachineProvisioning(t, "", ""))
	if !errors.Is(err, providers.ErrPermanent) || result.ProviderResourceID != "sb-dead" ||
		api.deleteCalls != 0 || api.createCalls != 0 || api.execCalls != 0 {
		t.Fatalf("result = %+v error = %v delete calls = %d create calls = %d exec calls = %d",
			result, err, api.deleteCalls, api.createCalls, api.execCalls)
	}

	api = newFakeAPI()
	api.sandboxes = []sandbox{ownedSandbox(t, "sb-gone", sandboxStatusDeleted, installationID, machineID)}
	_, err = provisionLaterAttempt(t, api, installationID, machineID, testMachineProvisioning(t, "", ""))
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") || api.createCalls != 0 {
		t.Fatalf("deleted sandbox error = %v create calls = %d, want it ignored without a create", err, api.createCalls)
	}

	api = newFakeAPI()
	api.created.Status = sandboxStatusStarting
	failed := api.created
	failed.Status = sandboxStatusFailed
	api.getResults = []sandbox{failed}
	result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if !errors.Is(err, providers.ErrPermanent) || result.ProviderResourceID != "sb-123" || api.deleteCalls != 0 {
		t.Fatalf("result = %+v error = %v delete calls = %d, want a sandbox that failed to start kept for cleanup",
			result, err, api.deleteCalls)
	}
}

func TestSuperserveProvisionMachineReportsASandboxThatDisappears(t *testing.T) {
	api := newFakeAPI()
	api.created.Status = sandboxStatusResuming
	api.getFound = false
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "disappeared") || result.ProviderResourceID != "sb-123" {
		t.Fatalf("result = %+v error = %v", result, err)
	}
}

func TestSuperserveProvisionMachineRefusesForeignAndMismatchedSandboxes(t *testing.T) {
	api := newFakeAPI()
	api.created.Metadata = map[string]string{installationLabel: "ins_other", machineLabel: "mch_other"}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "ownership") || result.ProviderResourceID != "" || api.execCalls != 0 {
		t.Fatalf("result = %+v error = %v exec calls = %d, want a foreign sandbox refused", result, err, api.execCalls)
	}

	for field, mutate := range map[string]func(*sandbox){
		"vcpu":   func(s *sandbox) { s.VCPUCount = 2 },
		"memory": func(s *sandbox) { s.MemoryMiB = 2048 },
	} {
		api = newFakeAPI()
		mutate(&api.created)
		result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), sizedProvisioning(t, "", 1, 1024), nil)
		if !errors.Is(err, providers.ErrPermanent) || !strings.Contains(err.Error(), field) ||
			result.ProviderResourceID != "sb-123" || api.deleteCalls != 0 || api.execCalls != 0 {
			t.Fatalf("%s: result = %+v error = %v delete calls = %d exec calls = %d, want the mismatch reported",
				field, result, err, api.deleteCalls, api.execCalls)
		}
	}
}

func TestSuperserveProvisionMachineRefusesDuplicateOwnedSandboxes(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	machineID := uuid.New()
	api.sandboxes = []sandbox{
		ownedSandbox(t, "sb-a", sandboxStatusActive, installationID, machineID),
		ownedSandbox(t, "sb-b", sandboxStatusActive, installationID, machineID),
	}
	result, err := provisionLaterAttempt(t, api, installationID, machineID, testMachineProvisioning(t, "", ""))
	if err == nil || !strings.Contains(err.Error(), "multiple") || result.ProviderResourceID != "" ||
		api.deleteCalls != 0 || api.createCalls != 0 || api.execCalls != 0 {
		t.Fatalf("result = %+v error = %v delete calls = %d create calls = %d exec calls = %d",
			result, err, api.deleteCalls, api.createCalls, api.execCalls)
	}
}

func TestSuperserveProvisionMachineRetriesTheDaemonExecWithAFreshToken(t *testing.T) {
	for name, status := range map[string]int{
		"paused":      http.StatusServiceUnavailable,
		"stale token": http.StatusUnauthorized,
	} {
		api := newFakeAPI()
		api.execErrs = []error{apiError{StatusCode: status}}
		_, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
		if err != nil {
			t.Fatalf("%s: provision machine: %v", name, err)
		}
		if api.execCalls != 2 || api.activateCalls != 1 || api.execSessions[0].AccessToken != "create-token" ||
			api.execSessions[1].AccessToken != "token-1" {
			t.Fatalf("%s: exec calls = %d activate calls = %d sessions = %+v, want one retry with a fresh token",
				name, api.execCalls, api.activateCalls, api.execSessions)
		}
	}

	api := newFakeAPI()
	api.execErrs = []error{apiError{StatusCode: http.StatusServiceUnavailable}}
	api.activateErrs = []error{apiError{StatusCode: http.StatusTooManyRequests}}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if err == nil || result.ProviderResourceID != "sb-123" {
		t.Fatalf("result = %+v error = %v, want the failed retry reported with the owned id", result, err)
	}
}

func TestSuperserveProvisionMachineReportsDaemonLauncherFailures(t *testing.T) {
	api := newFakeAPI()
	api.execErrs = []error{apiError{StatusCode: http.StatusBadGateway}}
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if apiStatusCode(err) != http.StatusBadGateway || result.ProviderResourceID != "sb-123" || api.execCalls != 1 {
		t.Fatalf("result = %+v error = %v exec calls = %d", result, err, api.execCalls)
	}

	api = newFakeAPI()
	api.execResults = []execResult{{ExitCode: 1, Stderr: strings.Repeat("x", 3000) + "omnara daemon exited early"}}
	result, err = provisionTestMachine(t, api, uuid.New(), uuid.New(), testMachineProvisioning(t, "", ""), nil)
	if err == nil || !strings.Contains(err.Error(), "status 1") || !strings.Contains(err.Error(), "exited early") ||
		len(err.Error()) > maxReportedOutputBytes+128 || result.ProviderResourceID != "sb-123" {
		t.Fatalf("result = %+v error length = %d error = %.80s", result, len(err.Error()), err)
	}
}

func TestSuperserveInspectMachineChecksOwnership(t *testing.T) {
	installationID := uuid.New()
	machineID := uuid.New()
	inspect := func(api *fakeAPI, resourceID string) (string, bool, error) {
		return newTestProvider(api).InspectMachine(
			context.Background(), installationID, machineID, executionstore.MachineProvisioningConfig{}, resourceID,
		)
	}

	api := newFakeAPI()
	api.created = ownedSandbox(t, "sb-123", sandboxStatusFailed, installationID, machineID)
	if id, found, err := inspect(api, "sb-123"); err != nil || !found || id != "sb-123" {
		t.Fatalf("inspect failed sandbox by id = %q found %v error %v, want it reported for deletion", id, found, err)
	}

	api.created = ownedSandbox(t, "sb-123", sandboxStatusActive, uuid.New(), machineID)
	if _, found, err := inspect(api, "sb-123"); err == nil || found {
		t.Fatalf("inspect foreign sandbox = found %v error %v, want a refusal", found, err)
	}

	api.created = ownedSandbox(t, "sb-456", sandboxStatusActive, installationID, machineID)
	if _, found, err := inspect(api, "sb-123"); err == nil || found {
		t.Fatalf("inspect mismatched id = found %v error %v, want a refusal", found, err)
	}

	api.created = ownedSandbox(t, "sb-123", sandboxStatusDeleted, installationID, machineID)
	if _, found, err := inspect(api, "sb-123"); err != nil || found {
		t.Fatalf("inspect deleted sandbox = found %v error %v, want absent", found, err)
	}

	api = newFakeAPI()
	api.getFound = false
	if _, found, err := inspect(api, "sb-123"); err != nil || found {
		t.Fatalf("inspect missing sandbox = found %v error %v, want absent", found, err)
	}

	api = newFakeAPI()
	api.sandboxes = []sandbox{ownedSandbox(t, "sb-found", sandboxStatusPaused, installationID, machineID)}
	if id, found, err := inspect(api, ""); err != nil || !found || id != "sb-found" || len(api.getLookups) != 0 {
		t.Fatalf("inspect by ownership = %q found %v error %v lookups %v", id, found, err, api.getLookups)
	}
	if _, found, err := inspect(newFakeAPI(), ""); err != nil || found {
		t.Fatalf("inspect by ownership without sandboxes = found %v error %v", found, err)
	}
}

func TestSuperserveDeleteMachine(t *testing.T) {
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
	api.created = ownedSandbox(t, "sb-123", sandboxStatusFailed, installationID, machineID)
	if err := deleteMachine(api, "sb-123"); err != nil || api.deletedID != "sb-123" {
		t.Fatalf("delete owned sandbox error = %v deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.created = ownedSandbox(t, "sb-123", sandboxStatusActive, uuid.New(), machineID)
	if err := deleteMachine(api, "sb-123"); err == nil || api.deleteCalls != 0 {
		t.Fatalf("delete foreign sandbox error = %v delete calls %d, want a refusal", err, api.deleteCalls)
	}

	api = newFakeAPI()
	api.getFound = false
	api.sandboxes = []sandbox{ownedSandbox(t, "sb-replacement", sandboxStatusActive, installationID, machineID)}
	if err := deleteMachine(api, "sb-stale"); err != nil || api.deletedID != "sb-replacement" {
		t.Fatalf("delete by ownership fallback error = %v deleted %q", err, api.deletedID)
	}

	api = newFakeAPI()
	api.getFound = false
	if err := deleteMachine(api, "sb-gone"); err != nil || api.deleteCalls != 0 {
		t.Fatalf("delete missing sandbox error = %v delete calls %d, want a quiet no-op", err, api.deleteCalls)
	}

	api = newFakeAPI()
	api.getErrs = []error{errors.New("permission denied")}
	if err := deleteMachine(api, "sb-123"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("delete with a lookup failure error = %v", err)
	}
}
