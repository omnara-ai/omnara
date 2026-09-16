package tenki

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/LuxorLabs/tenki-sdk-go/sandbox"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

func TestTenkiProviderLiveSmoke(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("TENKI_API_KEY"))
	if token == "" {
		if os.Getenv("OMNARA_REQUIRE_TENKI_LIVE") == "1" {
			t.Fatal("TENKI_API_KEY is required")
		}
		t.Skip("TENKI_API_KEY is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	api := &sdkClient{baseURL: defaultAPIBaseURL, token: token}
	installationID, machineID := uuid.New(), uuid.New()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		sessions, err := api.List(cleanup)
		require.NoError(t, err)
		for _, session := range sessions {
			if ownedBy(session, installationID, machineID) {
				require.NoError(t, api.Delete(cleanup, session.ID))
			}
		}
	})
	fault := &lostCreateResponseAPI{apiClient: api, bootstrapErr: errors.New("stop before daemon bootstrap")}
	p := testProvider(fault)
	p.AuthorizeCreation(installationID, machineID)
	result, err := p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "test-token", nil)
	require.ErrorContains(t, err, "injected lost create response")
	require.Empty(t, result.ProviderResourceID)
	require.NotEmpty(t, fault.createdID)
	p = testProvider(fault)
	require.Eventually(t, func() bool {
		result, err = p.ProvisionMachine(ctx, installationID, machineID, testProvisioning(), "test-token", nil)
		return errors.Is(err, fault.bootstrapErr)
	}, 3*time.Minute, time.Second)
	require.Equal(t, fault.createdID, result.ProviderResourceID)
	require.Equal(t, 1, fault.creates)
	ready, found, err := api.Get(ctx, result.ProviderResourceID)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ready.Sticky)
	require.Equal(t, "RUNNING", ready.State)
	client, err := api.newClient()
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	session, err := client.Get(ctx, result.ProviderResourceID)
	require.NoError(t, err)
	nonce := uuid.NewString()
	command := "printf '%s' " + shellQuote(nonce) + " > /home/tenki/omnara-live-nonce"
	execution, err := session.Command([]string{"sh", "-c", command}).Exec(ctx)
	require.NoError(t, err)
	require.Zero(t, execution.ExitCode)
	data, err := session.ReadFile(ctx, "/home/tenki/omnara-live-nonce")
	require.NoError(t, err)
	require.Equal(t, nonce, string(data))
	p = testProvider(api)
	id, found, err := p.InspectMachine(ctx, installationID, machineID, testProvisioning(), "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, fault.createdID, id)
	observation, err := p.ObserveRuntimeState(
		ctx,
		providers.RuntimeTarget{InstallationID: installationID, MachineID: machineID, ProviderResourceID: id},
	)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateRunning, observation.State)
	require.Error(t, p.DeleteMachine(ctx, uuid.New(), machineID, testProvisioning(), id))
	require.NoError(t, p.DeleteMachine(ctx, installationID, machineID, testProvisioning(), id))
	require.Eventually(t, func() bool {
		current, found, err := api.Get(ctx, id)
		return err == nil && (!found || current.State == "TERMINATED")
	}, time.Minute, time.Second)
	require.NoError(t, p.DeleteMachine(ctx, installationID, machineID, testProvisioning(), id))
	_, err = client.Get(ctx, id)
	require.True(t, err == nil || errors.Is(err, sdk.ErrSessionNotFound))
}

// The service E2E covers bootstrap; this test stops there to isolate create recovery.
type lostCreateResponseAPI struct {
	apiClient
	createdID    string
	creates      int
	bootstrapErr error
}

func (a *lostCreateResponseAPI) Create(ctx context.Context, request createRequest) (sandbox, error) {
	a.creates++
	created, err := a.apiClient.Create(ctx, request)
	if err != nil {
		return created, err
	}
	a.createdID = created.ID
	return sandbox{}, errors.New("injected lost create response")
}

func (a *lostCreateResponseAPI) Bootstrap(context.Context, string, map[string]string) error {
	return a.bootstrapErr
}
