package e2b

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
		TemplateID: "pub123", Aliases: []string{"claude"}, Names: []string{"claude", "e2b/claude"},
		CPUCount: 2, MemoryMB: 2048, IsDefault: true,
	}
	team := template{TemplateID: "team456", Aliases: []string{"claude"}, Names: []string{"acme/claude"}, CPUCount: 4,
		MemoryMB: 8192}
	for _, test := range []struct {
		ref      string
		cpu      int
		memoryMB int
	}{
		{ref: "claude", cpu: 4, memoryMB: 8192},
		{ref: "e2b/claude", cpu: 2, memoryMB: 2048},
		{ref: "acme/claude", cpu: 4, memoryMB: 8192},
		{ref: "pub123", cpu: 2, memoryMB: 2048},
	} {
		a := &fakeAPI{templatePages: [][]template{{public}, {team}}}
		facts, err := testProvider(a).PrepareProvisioning(
			t.Context(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, test.ref)},
		)
		require.NoError(t, err, test.ref)
		require.Equal(t, test.cpu, *facts.CPU, test.ref)
		require.Equal(t, test.memoryMB, *facts.MemoryMB, test.ref)
		require.Equal(t, 2, a.templateCalls, test.ref)
	}
}

func TestPrepareProvisioningReportsUnusableTemplates(t *testing.T) {
	building := template{TemplateID: "b1", Aliases: []string{"building"}, Names: []string{"acme/building"}}
	for _, test := range []struct {
		name string
		api  *fakeAPI
		ref  string
		want string
	}{
		{name: "missing", api: &fakeAPI{templatePages: [][]template{{building}}}, ref: "nope", want: "not found"},
		{name: "no build", api: &fakeAPI{templatePages: [][]template{{building}}}, ref: "building", want: "no ready build"},
		{
			name: "repeated cursor",
			api:  &fakeAPI{templatePages: [][]template{{building}, {building}}, repeatCursor: true},
			ref:  "nope",
			want: "repeated a page cursor",
		},
	} {
		_, err := testProvider(test.api).PrepareProvisioning(
			t.Context(),
			executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, test.ref)},
		)
		require.ErrorContains(t, err, test.want, test.name)
		require.NotErrorIs(t, err, storeerr.ErrMachineProviderUnavailable, test.name)
	}
	_, err := testProvider(&fakeAPI{templateErr: apiError{StatusCode: http.StatusServiceUnavailable}}).PrepareProvisioning(
		t.Context(),
		executionstore.MachineProvisioningConfig{ProviderOptions: testOptions(t, "claude")},
	)
	require.ErrorIs(t, err, storeerr.ErrMachineProviderUnavailable)
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
	require.Equal(t, launcher[1:], start.Process.Args)
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
