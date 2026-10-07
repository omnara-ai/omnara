package createos

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func sandboxIn(status sandboxStatus) sandbox {
	return sandbox{ID: "sb-123", Status: status}
}

func wakeTestMachine(ctx context.Context, api *fakeAPI) error {
	return newTestProvider(api).WakeMachine(ctx, providers.WakeMachineInput{ProviderResourceID: "sb-123"})
}

func TestCreateOSProviderOptionsValidateSleepAfter(t *testing.T) {
	options := testOptions(t, "s-1vcpu-1gb", "devbox:1", "")
	options["sleep_after_ms"] = mustRawJSON(t, 30000)
	if parsed, err := parseProviderOptions(options); err != nil || parsed.SleepAfterMS != 30000 {
		t.Fatalf("parse provider options = %+v, %v", parsed, err)
	}
	options["sleep_after_ms"] = mustRawJSON(t, 29999)
	if _, err := parseProviderOptions(options); err == nil || !strings.Contains(err.Error(), "sleep_after_ms") {
		t.Fatalf("parse provider options error = %v, want sleep_after_ms below the minimum rejected", err)
	}
}

func TestCreateOSProvisionWithSleepLetsTheDaemonPauseTheSandbox(t *testing.T) {
	api := newFakeAPI()
	machineProvisioning := testMachineProvisioning(t, "s-1vcpu-1gb", "devbox:1", "")
	machineProvisioning.ProviderOptions["sleep_after_ms"] = mustRawJSON(t, 45000)
	result, err := provisionTestMachine(t, api, uuid.New(), uuid.New(), machineProvisioning, nil)
	if err != nil {
		t.Fatalf("provision machine: %v", err)
	}
	if result.SandboxURL != "https://api.sb.createos.sh/v1/sandboxes/sb-123" {
		t.Fatalf("sandbox url = %q, want the sandbox's control-plane url", result.SandboxURL)
	}
	for key, want := range map[string]string{
		daemonprotocol.SleepAfterEnvVar:     "45000",
		daemonprotocol.WakeListenAddrEnvVar: "127.0.0.1:8377",
		daemonprotocol.SleepPlatformEnvVar:  daemonprotocol.SleepPlatformCreateOS,
	} {
		if got := api.createRequest.Envs[key]; got != want {
			t.Fatalf("create env %s = %q, want %q", key, got, want)
		}
	}
}

func TestCreateOSWakeMachinePokesTheDaemonOnceRunning(t *testing.T) {
	for name, test := range map[string]struct {
		statuses    []sandboxStatus
		resumeErr   error
		wantResumes int
	}{
		"paused":              {statuses: []sandboxStatus{sandboxStatusPaused, sandboxStatusResuming}, wantResumes: 1},
		"resume failed":       {statuses: []sandboxStatus{sandboxStatusError}, wantResumes: 1},
		"pause in flight":     {statuses: []sandboxStatus{sandboxStatusRunning, sandboxStatusPausing, sandboxStatusPaused}, wantResumes: 1},
		"already resuming":    {statuses: []sandboxStatus{sandboxStatusPaused}, resumeErr: apiError{StatusCode: http.StatusConflict}, wantResumes: 1},
		"no capacity yet":     {statuses: []sandboxStatus{sandboxStatusPaused, sandboxStatusPaused}, resumeErr: apiError{StatusCode: http.StatusServiceUnavailable}, wantResumes: 2},
		"rate limited resume": {statuses: []sandboxStatus{sandboxStatusPaused, sandboxStatusPaused}, resumeErr: apiError{StatusCode: http.StatusTooManyRequests}, wantResumes: 2},
		"never paused":        {},
		"resumed by someone":  {statuses: []sandboxStatus{sandboxStatusResuming}},
	} {
		api := newFakeAPI()
		api.resumeErr = test.resumeErr
		for _, status := range test.statuses {
			api.getResults = append(api.getResults, sandboxIn(status))
		}
		if err := wakeTestMachine(context.Background(), api); err != nil {
			t.Fatalf("%s: wake machine: %v", name, err)
		}
		lookups := len(api.getLookups)
		if api.resumeCalls != test.wantResumes ||
			(len(test.statuses) > 0 && lookups != len(test.statuses)+1) ||
			(len(test.statuses) == 0 && lookups < 2) {
			t.Fatalf("%s: resumes = %d lookups = %d, want %d resumes", name, api.resumeCalls, lookups, test.wantResumes)
		}
		if api.execCalls != 1 || api.execTarget != "sb-123" ||
			api.execRequest.Command != wakePokeCommand.Command ||
			!slices.Equal(api.execRequest.Args, wakePokeCommand.Args) {
			t.Fatalf("%s: poke calls = %d target = %q request = %+v",
				name, api.execCalls, api.execTarget, api.execRequest)
		}
	}
}

func TestCreateOSWakeMachineRetriesAFailedPoke(t *testing.T) {
	api := newFakeAPI()
	api.getResults = []sandbox{sandboxIn(sandboxStatusPaused)}
	api.execErrs = []error{apiError{StatusCode: http.StatusBadGateway}}
	if err := wakeTestMachine(context.Background(), api); err != nil ||
		api.resumeCalls != 1 || api.execCalls != 2 {
		t.Fatalf("wake error = %v resumes = %d pokes = %d, want one resume and a second poke",
			err, api.resumeCalls, api.execCalls)
	}
}

func TestCreateOSWakeMachineRidesOutARateLimitedLookup(t *testing.T) {
	api := newFakeAPI()
	api.getErrs = []error{apiError{StatusCode: http.StatusTooManyRequests}}
	api.getResults = []sandbox{sandboxIn(sandboxStatusPaused)}
	if err := wakeTestMachine(context.Background(), api); err != nil || api.resumeCalls != 1 || api.execCalls != 1 {
		t.Fatalf("wake error = %v resumes = %d pokes = %d, want the wake to continue after the 429",
			err, api.resumeCalls, api.execCalls)
	}
}

func TestCreateOSWakeMachineWaitsOutRetryAfter(t *testing.T) {
	api := newFakeAPI()
	api.getErrs = []error{providers.WithRetryAfter(
		apiError{StatusCode: http.StatusTooManyRequests},
		http.Header{"Retry-After": {"60"}},
	)}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := wakeTestMachine(ctx, api); !errors.Is(err, context.DeadlineExceeded) || len(api.getLookups) != 1 {
		t.Fatalf("wake error = %v lookups = %d, want one lookup and then the Retry-After wait",
			err, len(api.getLookups))
	}
}

func TestCreateOSWakeMachineReportsSandboxesItCannotWake(t *testing.T) {
	for name, test := range map[string]struct {
		configure func(*fakeAPI)
		want      string
	}{
		"no credit": {
			configure: func(api *fakeAPI) {
				api.created.Status = sandboxStatusPaused
				api.resumeErr = apiError{StatusCode: http.StatusPaymentRequired}
			},
			want: "HTTP 402",
		},
		"destroyed": {
			configure: func(api *fakeAPI) { api.created.Status = sandboxStatusDestroyed },
			want:      `cannot wake from status "destroyed"`,
		},
		"missing": {
			configure: func(api *fakeAPI) { api.getFound = false },
			want:      "was not found",
		},
	} {
		api := newFakeAPI()
		test.configure(api)
		err := wakeTestMachine(context.Background(), api)
		if err == nil || !strings.Contains(err.Error(), test.want) || api.execCalls != 0 {
			t.Fatalf("%s: wake error = %v poke calls = %d, want %q", name, err, api.execCalls, test.want)
		}
	}

	if err := newTestProvider(newFakeAPI()).WakeMachine(
		context.Background(),
		providers.WakeMachineInput{},
	); err == nil {
		t.Fatal("wake machine accepted an empty resource id")
	}

	api := newFakeAPI()
	api.created.Status = sandboxStatusPausing
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := wakeTestMachine(ctx, api); !errors.Is(err, context.DeadlineExceeded) || api.execCalls != 0 {
		t.Fatalf("wake error = %v poke calls = %d, want the wait bounded by the context",
			err, api.execCalls)
	}
}
