package e2b

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestPrepareProvisioningKeepsStoredSize(t *testing.T) {
	a := &fakeAPI{templateErr: errors.New("dashboard down")}
	facts, err := testProvider(a).PrepareProvisioning(t.Context(), testProvisioning(t))
	require.NoError(t, err)
	require.Equal(t, 2, *facts.CPU)
	require.Equal(t, 2048, *facts.MemoryMB)
	require.Zero(t, a.templateCalls)
}

func TestPrepareProvisioningResolvesTemplatesLikeE2B(t *testing.T) {
	public := template{
		TemplateID: "pub123", Names: []string{"claude", "e2b/claude"},
		CPUCount: 2, MemoryMB: 2048, IsDefault: true,
	}
	historical := template{
		TemplateID: "old789", Names: []string{"old-acme/claude"}, CPUCount: 8, MemoryMB: 8192,
	}
	current := template{
		TemplateID: "team456", Names: []string{"acme/claude"}, CPUCount: 4, MemoryMB: 4096,
	}
	for _, test := range []struct {
		ref         string
		teamAliases map[string]string
		cpu         int
	}{
		{ref: "claude", teamAliases: map[string]string{"claude": "team456"}, cpu: 4},
		{ref: "claude", cpu: 2},
		{ref: "team456", teamAliases: map[string]string{"team456": "team456"}, cpu: 4},
		{ref: "pub123", cpu: 2},
		{ref: "e2b/claude", cpu: 2},
		{ref: "acme/claude", cpu: 4},
		{ref: "old-acme/claude", cpu: 8},
	} {
		a := &fakeAPI{templatePages: [][]template{{public, historical}, {current}}, teamAliases: test.teamAliases}
		facts, err := testProvider(a).PrepareProvisioning(
			t.Context(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, test.ref)},
		)
		require.NoError(t, err, test.ref)
		require.Equal(t, test.cpu, *facts.CPU, test.ref)
	}
}

func TestPrepareProvisioningReportsUnusableTemplates(t *testing.T) {
	building := template{TemplateID: "b1", Names: []string{"acme/building"}}
	pages := [][]template{{building}}
	unavailable := apiError{StatusCode: http.StatusServiceUnavailable}
	for _, test := range []struct {
		name        string
		api         *fakeAPI
		ref         string
		want        string
		unavailable bool
	}{
		{name: "missing", api: &fakeAPI{templatePages: pages}, ref: "nope", want: "not found"},
		{name: "missing name", api: &fakeAPI{templatePages: pages}, ref: "acme/nope", want: "not found"},
		{
			name: "no build",
			api:  &fakeAPI{templatePages: pages, teamAliases: map[string]string{"building": "b1"}},
			ref:  "building",
			want: "no ready build",
		},
		{
			name: "repeated cursor",
			api:  &fakeAPI{templatePages: [][]template{{building}, {building}}, repeatCursor: true},
			ref:  "acme/nope",
			want: "repeated a page cursor",
		},
		{name: "rejected key", api: &fakeAPI{resolveErr: apiError{StatusCode: http.StatusUnauthorized}}, ref: "claude",
			want: "HTTP 401"},
		{name: "alias lookup down", api: &fakeAPI{resolveErr: unavailable}, ref: "claude", want: "HTTP 503",
			unavailable: true},
		{name: "template list down", api: &fakeAPI{templateErr: unavailable}, ref: "acme/claude", want: "HTTP 503",
			unavailable: true},
	} {
		_, err := testProvider(test.api).PrepareProvisioning(
			t.Context(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, test.ref)},
		)
		require.ErrorContains(t, err, test.want, test.name)
		require.Equal(t, test.unavailable, errors.Is(err, storeerr.ErrMachineProviderUnavailable), test.name)
	}
}

func TestProvisionCreatesSandboxAndStartsDaemon(t *testing.T) {
	a := &fakeAPI{}
	installationID, machineID := uuid.New(), uuid.New()
	result, err := testProvider(a).ProvisionMachine(
		t.Context(), installationID, machineID, testProvisioning(t), "machine-token",
		map[string]string{"USER_KEY": "value"}, true,
	)
	require.NoError(t, err)
	require.Equal(t, "sbx1", result.ProviderResourceID)
	metadata, err := ownershipMetadata(installationID, machineID)
	require.NoError(t, err)
	require.Equal(t, []createSandboxRequest{{
		TemplateID: "claude",
		Timeout:    createTimeoutSeconds,
		Metadata:   metadata,
		Network:    networkConfig{AllowPublicTraffic: false},
	}}, a.creates)
	require.Equal(t, []string{"sbx1"}, a.extended)
	require.Len(t, a.starts, 1)
	start := a.starts[0]
	launcher := providers.ManagedDaemonLauncherArgs()
	require.Equal(t, launcher[0], start.Process.Cmd)
	require.Equal(t, []string{launcher[1], launcherGuard(launcherLockPath) + launcher[2]}, start.Process.Args)
	require.Equal(t, daemonProcessTag, start.Tag)
	require.False(t, start.Stdin)
	require.Equal(t, "machine-token", start.Process.Envs["OMNARA_MACHINE_TOKEN"])
	require.Equal(t, "value", start.Process.Envs["USER_KEY"])
	require.Equal(t, providers.ManagedBootScriptPayload(), start.Process.Envs[providers.ManagedBootstrapScriptEnvVar])
}

func TestProvisionAdoptsDaemonWhoseStartResponseWasLost(t *testing.T) {
	a := &fakeAPI{startErr: errors.New("lost response")}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.ErrorContains(t, err, "lost response")
	a.startErr = nil
	result, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.NoError(t, err)
	require.Equal(t, "sbx1", result.ProviderResourceID)
	require.Len(t, a.creates, 1)
	require.Len(t, a.starts, 1)
}

func TestLauncherGuardAllowsOneBootstrapAtATime(t *testing.T) {
	dir := t.TempDir()
	run := func(lockPath, script string) (string, error) {
		output, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", launcherGuard(lockPath)+script).Output()
		return string(output), err
	}
	exited := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 0")
	require.NoError(t, exited.Run())
	dead := strconv.Itoa(exited.Process.Pid)

	stale := filepath.Join(dir, "stale")
	for slot := range 10 {
		require.NoError(t, os.Symlink(dead, stale+"."+strconv.Itoa(slot)))
	}
	output, err := run(stale, "echo ran")
	require.NoError(t, err)
	require.Equal(t, "ran\n", output)
	_, err = os.Readlink(stale + ".10")
	require.NoError(t, err)

	held := filepath.Join(dir, "held")
	require.NoError(t, os.Symlink(strconv.Itoa(os.Getpid()), held+".0"))
	output, err = run(held, "echo ran")
	require.NoError(t, err)
	require.Empty(t, output)

	notLink := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(notLink+".0", nil, 0o600))
	for _, lockPath := range []string{notLink, filepath.Join(dir, "missing", "lock")} {
		output, err = run(lockPath, "echo ran")
		require.Error(t, err, lockPath)
		require.Empty(t, output, lockPath)
	}

	race := func(lockPath string) int {
		type result struct {
			output string
			err    error
		}
		results := make(chan result, 4)
		for range cap(results) {
			go func() {
				output, err := run(lockPath, "sleep 1;echo ran")
				results <- result{output: output, err: err}
			}()
		}
		ran := 0
		for range cap(results) {
			current := <-results
			require.NoError(t, current.err)
			if current.output == "ran\n" {
				ran++
			}
		}
		return ran
	}
	require.Equal(t, 1, race(filepath.Join(dir, "fresh")))
	recovering := filepath.Join(dir, "recovering")
	require.NoError(t, os.Symlink(dead, recovering+".0"))
	require.Equal(t, 1, race(recovering))
}

func TestRetriedStartIsGuardedWhenTheFirstWasNeverListed(t *testing.T) {
	a := &fakeAPI{startErr: errors.New("lost response"), startUnlisted: true}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.ErrorContains(t, err, "lost response")
	a.startErr, a.startUnlisted = nil, false
	_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.NoError(t, err)
	require.Len(t, a.starts, 2)
	for _, start := range a.starts {
		require.Equal(t, daemonLauncherArgs()[1:], start.Process.Args)
	}
}

func TestLostCreateResponseNeverCreatesDuplicate(t *testing.T) {
	a := &fakeAPI{createErr: errors.New("connection reset")}
	installationID, machineID := uuid.New(), uuid.New()
	p := testProvider(a)
	_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.ErrorContains(t, err, "connection reset")
	a.createErr = nil
	a.addSandbox("sbx-late", mustMetadata(t, installationID, machineID), 2, 2048)
	a.hide = true
	_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.ErrorContains(t, err, "outcome is unknown")
	_, err = testProvider(a).ProvisionMachine(
		t.Context(), installationID, machineID, testProvisioning(t), "token", nil, false,
	)
	require.ErrorContains(t, err, "outcome is unknown")
	a.hide = false
	result, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
	require.NoError(t, err)
	require.Equal(t, "sbx-late", result.ProviderResourceID)
	require.Len(t, a.creates, 1)
}

func TestCreateFailuresDecideWhetherToCreateAgain(t *testing.T) {
	dialErr := fmt.Errorf("e2b request failed: %w", &url.Error{
		Op:  "Post",
		URL: "https://api.e2b.app/v2/sandboxes",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
	})
	dnsErr := fmt.Errorf("e2b request failed: %w", &url.Error{
		Op:  "Post",
		URL: "https://api.e2b.app/v2/sandboxes",
		Err: &net.DNSError{Err: "no such host", Name: "api.e2b.app", IsNotFound: true},
	})
	for _, test := range []struct {
		name      string
		err       error
		permanent bool
		recreates bool
	}{
		{name: "rate limited", err: apiError{StatusCode: http.StatusTooManyRequests}, recreates: true},
		{name: "unavailable", err: apiError{StatusCode: http.StatusServiceUnavailable}, recreates: true},
		{name: "never sent", err: dialErr, recreates: true},
		{name: "dns failure", err: dnsErr, recreates: true},
		{name: "unknown template", err: apiError{StatusCode: http.StatusNotFound}, permanent: true},
		{name: "bad request", err: apiError{StatusCode: http.StatusBadRequest}, permanent: true},
		{name: "server error", err: apiError{StatusCode: http.StatusInternalServerError}},
		{name: "request timeout", err: apiError{StatusCode: http.StatusRequestTimeout}},
		{name: "reset", err: errors.New("connection reset")},
	} {
		a := &fakeAPI{createErr: test.err}
		p := testProvider(a)
		installationID, machineID := uuid.New(), uuid.New()
		_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
		require.ErrorIs(t, err, test.err, test.name)
		require.Equal(t, test.permanent, errors.Is(err, providers.ErrPermanent), test.name)
		a.createErr = nil
		_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(t), "token", nil, true)
		if test.recreates {
			require.NoError(t, err, test.name)
			require.Len(t, a.creates, 2, test.name)
		} else {
			require.ErrorContains(t, err, "outcome is unknown", test.name)
			require.Len(t, a.creates, 1, test.name)
		}
	}
}

func TestLaterAttemptNeverCreates(t *testing.T) {
	a := &fakeAPI{}
	_, err := testProvider(a).ProvisionMachine(
		t.Context(), uuid.New(), uuid.New(), testProvisioning(t), "token", nil, false,
	)
	require.ErrorContains(t, err, "outcome is unknown")
	require.Empty(t, a.creates)
}

func TestProvisionRejectsSandboxesItCannotTrust(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	owned := mustMetadata(t, installationID, machineID)
	for _, test := range []struct {
		name      string
		setup     func(*fakeAPI)
		want      string
		permanent bool
	}{
		{
			name:      "wrong size",
			setup:     func(a *fakeAPI) { a.addSandbox("sbx-small", owned, 2, 512) },
			want:      "not the resolved machine size",
			permanent: true,
		},
		{
			name: "two owned sandboxes",
			setup: func(a *fakeAPI) {
				a.addSandbox("sbx-a", owned, 2, 2048)
				a.addSandbox("sbx-b", owned, 2, 2048)
			},
			want: "multiple e2b sandboxes",
		},
	} {
		a := &fakeAPI{}
		test.setup(a)
		_, err := testProvider(a).ProvisionMachine(
			t.Context(), installationID, machineID, testProvisioning(t), "token", nil, false,
		)
		require.ErrorContains(t, err, test.want, test.name)
		require.Equal(t, test.permanent, errors.Is(err, providers.ErrPermanent), test.name)
		require.Empty(t, a.starts, test.name)
	}
}

func TestInspectAndDeleteCheckOwnership(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	a := &fakeAPI{}
	a.addSandbox("sbx-own", mustMetadata(t, installationID, machineID), 2, 2048)
	a.addSandbox("sbx-other", mustMetadata(t, installationID, uuid.New()), 2, 2048)
	p := testProvider(a)
	config := testProvisioning(t)

	id, found, err := p.InspectMachine(t.Context(), installationID, machineID, config, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "sbx-own", id)
	_, _, err = p.InspectMachine(t.Context(), installationID, machineID, config, "sbx-other")
	require.ErrorContains(t, err, "ownership")

	require.ErrorContains(t, p.DeleteMachine(t.Context(), installationID, machineID, config, ""), "required")
	require.ErrorContains(t, p.DeleteMachine(t.Context(), installationID, machineID, config, "sbx-other"), "ownership")
	require.NoError(t, p.DeleteMachine(t.Context(), installationID, machineID, config, "sbx-own"))
	require.NoError(t, p.DeleteMachine(t.Context(), installationID, machineID, config, "sbx-own"))
	require.Equal(t, []string{"sbx-own"}, a.deleted)
}

func TestCheckConnectionListsSandboxes(t *testing.T) {
	require.NoError(t, testProvider(&fakeAPI{}).CheckConnection(t.Context(), testProvisioning(t)))
	err := testProvider(&fakeAPI{findErr: apiError{StatusCode: http.StatusUnauthorized}}).CheckConnection(
		t.Context(),
		testProvisioning(t),
	)
	require.Equal(t, http.StatusUnauthorized, apiStatusCode(err))
}

func mustMetadata(t *testing.T, installationID, machineID uuid.UUID) map[string]string {
	t.Helper()
	metadata, err := ownershipMetadata(installationID, machineID)
	require.NoError(t, err)
	return metadata
}
