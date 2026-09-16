package tenki

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	mu        sync.Mutex
	sessions  []sandbox
	creates   int
	createErr error
	hide      bool
	getErr    error
	bootErr   error
	deletes   int
	boots     int
}

func (a *fakeAPI) Create(_ context.Context, request createRequest) (sandbox, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.creates++
	s := sandbox{
		ID:         uuid.NewString(),
		State:      "RUNNING",
		Metadata:   request.Metadata,
		CPU:        request.CPU,
		MemoryMB:   request.MemoryMB,
		DiskSizeGB: request.Options.DiskSizeGB,
		Sticky:     true,
	}
	a.sessions = append(a.sessions, s)
	if a.createErr != nil {
		return sandbox{}, a.createErr
	}
	return s, nil
}
func (a *fakeAPI) List(context.Context) ([]sandbox, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.hide {
		return nil, nil
	}
	return append([]sandbox(nil), a.sessions...), nil
}
func (a *fakeAPI) Get(_ context.Context, id string) (sandbox, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.getErr != nil {
		return sandbox{}, false, a.getErr
	}
	for _, s := range a.sessions {
		if s.ID == id {
			return s, true, nil
		}
	}
	return sandbox{}, false, nil
}
func (a *fakeAPI) WaitReady(ctx context.Context, id string) (sandbox, error) {
	s, _, err := a.Get(ctx, id)
	return s, err
}
func (a *fakeAPI) Bootstrap(context.Context, string, map[string]string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.boots++
	return a.bootErr
}
func (a *fakeAPI) Delete(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deletes++
	for i := range a.sessions {
		if a.sessions[i].ID == id {
			a.sessions[i].State = "TERMINATED"
		}
	}
	return nil
}

func testProvisioning() executionstore.MachineProvisioningConfig {
	return executionstore.MachineProvisioningConfig{CPU: new(2), MemoryMB: new(4096)}
}
func testProvider(api apiClient) *provider {
	return &provider{api: api, omnaraAPIURL: "https://omnara.example/api/v1"}
}

func TestLostCreateResponseNeverCreatesDuplicate(t *testing.T) {
	ctx := t.Context()
	a := &fakeAPI{createErr: errors.New("lost response")}
	installationID, machineID := uuid.New(), uuid.New()
	p := testProvider(a)
	p.AuthorizeCreation(installationID, machineID)
	_, err := p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil)
	require.ErrorContains(t, err, "lost response")
	a.hide = true
	p = testProvider(a)
	for range 3 {
		_, err = p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil)
		require.ErrorContains(t, err, "outcome is unknown")
	}
	require.Equal(t, 1, a.creates)
	a.hide = false
	result, err := p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "token", nil)
	require.NoError(t, err)
	require.Equal(t, a.sessions[0].ID, result.ProviderResourceID)
	require.Equal(t, 1, a.creates)
}

func TestCreationRequiresDurableAuthorization(t *testing.T) {
	a := &fakeAPI{}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil)
	require.Error(t, err)
	require.Zero(t, a.creates)
	p.AuthorizeCreation(installationID, uuid.New())
	_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil)
	require.Error(t, err)
	require.Zero(t, a.creates)
}

func TestConcurrentProvisioningConsumesCreationOnce(t *testing.T) {
	a := &fakeAPI{hide: true}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	p.AuthorizeCreation(installationID, machineID)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			_, _ = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil)
		})
	}
	wg.Wait()
	require.Equal(t, 1, a.creates)
	p.AuthorizeCreation(installationID, machineID)
	_, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil)
	require.Error(t, err)
	require.Equal(t, 1, a.creates)
}

func TestBootstrapFailurePreservesResourceID(t *testing.T) {
	a := &fakeAPI{bootErr: errors.New("bootstrap failed")}
	p := testProvider(a)
	installationID, machineID := uuid.New(), uuid.New()
	p.AuthorizeCreation(installationID, machineID)
	result, err := p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil)
	require.Error(t, err)
	require.Equal(t, a.sessions[0].ID, result.ProviderResourceID)
	a.bootErr = nil
	_, err = p.ProvisionMachine(t.Context(), installationID, machineID, testProvisioning(), "token", nil)
	require.NoError(t, err)
	require.Equal(t, 1, a.creates)
}

func TestDeleteVerifiesOwnershipAndPropagatesLookupFailure(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	a := &fakeAPI{sessions: []sandbox{{ID: "foreign", Metadata: map[string]string{}}}}
	p := testProvider(a)
	require.Error(t, p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "foreign"))
	require.Zero(t, a.deletes)
	a.getErr = errors.New("permission denied")
	require.ErrorContains(
		t,
		p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "missing"),
		"permission denied",
	)
	a.getErr = nil
	require.NoError(t, p.DeleteMachine(t.Context(), installationID, machineID, testProvisioning(), "missing"))
	require.Zero(t, a.deletes)
}

func TestRuntimeObservationTreatsUncertainResultsConservatively(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	target := providers.RuntimeTarget{
		InstallationID:     installationID,
		MachineID:          machineID,
		ProviderResourceID: "session",
	}
	a := &fakeAPI{}
	p := testProvider(a)
	bulk, err := p.ObserveRuntimeStates(t.Context(), []providers.RuntimeTarget{target})
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateUnknown, bulk[0].State)
	exact, err := p.ObserveRuntimeState(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateTerminated, exact.State)
	a.sessions = []sandbox{
		{
			ID:       "session",
			State:    "RUNNING",
			Metadata: map[string]string{installationLabel: installationID.String(), machineLabel: machineID.String()},
		},
	}
	bulk, err = p.ObserveRuntimeStates(t.Context(), []providers.RuntimeTarget{target, target})
	require.NoError(t, err)
	for _, observation := range bulk {
		require.Equal(t, providers.RuntimeStateUnknown, observation.State)
	}
	exact, err = p.ObserveRuntimeState(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateRunning, exact.State)
	a.sessions[0].State = "FUTURE_STATE"
	exact, err = p.ObserveRuntimeState(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateUnknown, exact.State)
}
