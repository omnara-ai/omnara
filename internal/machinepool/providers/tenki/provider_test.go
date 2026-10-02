package tenki

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

func TestProvisionStartsDaemonFromBootRuntime(t *testing.T) {
	a := &fakeAPI{}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	result, err := p.ProvisionMachine(
		t.Context(), installationID, machineID, testProvisioning(), "machine-token",
		map[string]string{"USER_KEY": "value", "my-var": "value"},
		true,
	)
	require.NoError(t, err)
	require.Equal(t, a.sessions[0].ID, result.ProviderResourceID)
	request := a.requests[0]
	metadata, err := ownershipMetadata(installationID, machineID)
	require.NoError(t, err)
	require.Equal(t, metadata, request.Metadata)
	require.Equal(t, []string{managedTag}, request.Tags)
	require.True(t, request.Sticky)
	require.False(t, request.AllowInbound)
	require.True(t, request.AllowOutbound)
	require.Equal(t, 2, request.CPUCores)
	require.Equal(t, 4096, request.MemoryMB)
	require.EqualValues(t, baseImageDiskSizeGB, request.DiskSizeGB)
	require.Empty(t, request.RegistryRef)
	require.Equal(t, providers.ManagedDaemonLauncherArgs(), request.Runtime.Start.Argv)
	require.Equal(t, "TEMPLATE_RUNTIME_RUN_AT_BOOT", request.Runtime.RunAt)
	require.Equal(t, "TEMPLATE_RESTART_POLICY_NEVER", request.Runtime.RestartPolicy)
	require.Equal(t, providers.ManagedBootScriptPayload(), request.Runtime.Env[providers.ManagedBootstrapScriptEnvVar])
	require.Equal(t, "machine-token", request.Runtime.Env["OMNARA_MACHINE_TOKEN"])
	require.Equal(t, "value", request.Runtime.Env["USER_KEY"])
	require.NotContains(t, request.Runtime.Env, "my-var")
}

func TestProvisionKeepsCustomImageDiskUnlessSet(t *testing.T) {
	for _, test := range []struct {
		options map[string]json.RawMessage
		disk    int32
	}{
		{options: map[string]json.RawMessage{"image": json.RawMessage(`"ws/custom"`)}, disk: 0},
		{
			options: map[string]json.RawMessage{
				"image":        json.RawMessage(`"ws/custom"`),
				"disk_size_gb": json.RawMessage(`40`),
			},
			disk: 40,
		},
	} {
		a := &fakeAPI{}
		p := testProvider(a)
		installationID, machineID := uuid.New(), uuid.New()
		config := testProvisioning()
		config.ProviderOptions = test.options
		_, err := p.ProvisionMachine(t.Context(), installationID, machineID, config, "token", nil, true)
		require.NoError(t, err)
		require.Equal(t, "ws/custom", a.requests[0].RegistryRef)
		require.Equal(t, test.disk, a.requests[0].DiskSizeGB)
	}
}

func TestValidateMachineConfigLimitsBootEnvSize(t *testing.T) {
	p := testProvider(&fakeAPI{})
	big := strings.Repeat("x", maxRuntimeEnvBytes)
	filtered := map[string]string{"lower_Case_1": "x", "my-var": big}
	require.NoError(t, p.ValidateMachineConfig(testProvisioning(), filtered))
	require.ErrorContains(t, p.ValidateMachineConfig(testProvisioning(), map[string]string{"BIG": big}), "tenki env")
}

func TestLostCreateResponseNeverCreatesDuplicate(t *testing.T) {
	ctx := t.Context()
	a := &fakeAPI{createErr: errors.New("lost response")}
	installationID, machineID := uuid.New(), uuid.New()
	p := testProvider(a)
	_, err := p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil, true)
	require.ErrorContains(t, err, "lost response")
	a.hide = true
	for range 3 {
		_, err = p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil, true)
		require.ErrorContains(t, err, "outcome is unknown")
	}
	p = testProvider(a)
	for range 3 {
		_, err = p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil, false)
		require.ErrorContains(t, err, "outcome is unknown")
	}
	require.Equal(t, 1, len(a.requests))
	a.hide = false
	result, err := p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil, false)
	require.NoError(t, err)
	require.Equal(t, a.sessions[0].ID, result.ProviderResourceID)
	require.Equal(t, 1, len(a.requests))
}

func TestCreateRejectionsArePermanentExceptTooManyRequests(t *testing.T) {
	for _, test := range []struct {
		err       error
		permanent bool
		recreates bool
	}{
		{err: apiError{StatusCode: http.StatusTooManyRequests, Code: "resource_exhausted"}, recreates: true},
		{err: apiError{StatusCode: http.StatusBadRequest, Code: "invalid_argument"}, permanent: true},
		{err: apiError{StatusCode: http.StatusServiceUnavailable, Code: "unavailable"}},
		{err: apiError{StatusCode: http.StatusRequestTimeout}},
		{err: errors.New("connection reset")},
	} {
		a := &fakeAPI{rejectErr: test.err}
		p := testProvider(a)
		installationID, machineID := uuid.New(), uuid.New()
		_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil, true)
		require.ErrorIs(t, err, test.err)
		require.Equal(t, test.permanent, errors.Is(err, providers.ErrPermanent), "%v", test.err)
		a.rejectErr = nil
		_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil, true)
		if test.recreates {
			require.NoError(t, err, "%v", test.err)
			require.Equal(t, 2, len(a.requests))
		} else {
			require.ErrorContains(t, err, "outcome is unknown", "%v", test.err)
			require.Equal(t, 1, len(a.requests))
		}
	}
}

func TestFirstAttemptCreatesWithoutDiscovery(t *testing.T) {
	a := &fakeAPI{listErr: errors.New("list unavailable")}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	result, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil, true)
	require.NoError(t, err)
	require.Equal(t, a.sessions[0].ID, result.ProviderResourceID)
	_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil, true)
	require.ErrorContains(t, err, "list unavailable")
	require.Equal(t, 1, len(a.requests))
}

func TestLaterAttemptNeverCreates(t *testing.T) {
	a := &fakeAPI{}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil, false)
	require.ErrorContains(t, err, "outcome is unknown")
	require.Zero(t, len(a.requests))
}

func TestDeleteVerifiesOwnershipAndSkipsTerminatedSessions(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	a := &fakeAPI{sessions: []session{
		{ID: "foreign", State: sessionStateRunning, Metadata: map[string]string{}},
		ownedSession(t, "terminated", sessionStateTerminated, installationID, machineID),
		ownedSession(t, "running", sessionStateRunning, installationID, machineID),
	}}
	p := testProvider(a)
	require.Error(t, p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "foreign"))
	require.NoError(t, p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "terminated"))
	require.Zero(t, a.deletes)
	require.NoError(t, p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "missing"))
	require.Equal(t, 1, a.deletes)
	require.NoError(t, p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "running"))
	require.Equal(t, 2, a.deletes)
	a.getErr = errors.New("permission denied")
	require.ErrorContains(
		t,
		p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "running"),
		"permission denied",
	)
}
