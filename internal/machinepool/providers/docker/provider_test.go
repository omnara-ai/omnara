package docker

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

const testImage = "buildpack-deps:24.04-curl"

func provision(
	t *testing.T,
	api *fakeAPI,
	installationID, machineID uuid.UUID,
) (providers.ProvisionMachineResult, error) {
	t.Helper()
	return newTestProvider(api).ProvisionMachine(
		context.Background(),
		installationID,
		machineID,
		testMachineProvisioning(t, testImage, 2, 4096),
		"machine-token",
		map[string]string{"APP_ENV": "test"},
		true,
	)
}

func TestDockerProvisionStartsHardenedContainerOnItsOwnNetwork(t *testing.T) {
	api := newFakeAPI()
	installationID, machineID := uuid.New(), uuid.New()
	result, err := provision(t, api, installationID, machineID)
	require.NoError(t, err)
	name, labels, err := machineIdentity(installationID, machineID)
	require.NoError(t, err)
	require.Equal(t, name, result.ProviderResourceID)
	require.Equal(t, labels, api.networks[name].Labels)
	require.Equal(t, []string{name}, api.starts)

	require.Len(t, api.containerCreates, 1)
	request := api.containerCreates[0]
	require.Equal(t, testImage, request.Image)
	require.Equal(t, providers.ManagedDaemonLauncherArgs(), request.Entrypoint)
	require.Equal(t, labels, request.Labels)
	require.Contains(t, request.Env, "OMNARA_MACHINE_TOKEN=machine-token")
	require.Contains(t, request.Env, "APP_ENV=test")
	host := request.HostConfig
	require.Equal(t, int64(2_000_000_000), host.NanoCPUs)
	require.Equal(t, int64(4096*mebibyte), host.Memory)
	require.Equal(t, host.Memory, host.MemorySwap)
	require.Equal(t, int64(processLimit), host.PidsLimit)
	require.True(t, host.Init)
	require.Equal(t, []string{"NET_RAW"}, host.CapDrop)
	require.Equal(t, logDriver, host.LogConfig.Type)
	require.Equal(t, name, host.NetworkMode)
}

func TestDockerProvisionAdoptsResourcesAfterLostCreateResponses(t *testing.T) {
	api := newFakeAPI()
	api.loseNetworkCreate = true
	installationID, machineID := uuid.New(), uuid.New()
	_, err := provision(t, api, installationID, machineID)
	require.Error(t, err)

	api.loseContainerCreate = true
	_, err = provision(t, api, installationID, machineID)
	require.Error(t, err)
	require.Empty(t, api.starts)

	result, err := provision(t, api, installationID, machineID)
	require.NoError(t, err)
	name, _, err := machineIdentity(installationID, machineID)
	require.NoError(t, err)
	require.Equal(t, name, result.ProviderResourceID)
	require.Len(t, api.networks, 1)
	require.Len(t, api.containers, 1)
	require.Equal(t, "running", api.containers[name].State.Status)
}

func TestDockerProvisionReplacesStoppedContainer(t *testing.T) {
	api := newFakeAPI()
	installationID, machineID := uuid.New(), uuid.New()
	_, err := provision(t, api, installationID, machineID)
	require.NoError(t, err)
	name, _, err := machineIdentity(installationID, machineID)
	require.NoError(t, err)
	stopped := api.containers[name]
	stopped.State.Status = "exited"
	api.containers[name] = stopped

	_, err = provision(t, api, installationID, machineID)
	require.ErrorIs(t, err, providers.ErrResourceReplaced)
	require.NotContains(t, api.containers, name)

	_, err = provision(t, api, installationID, machineID)
	require.NoError(t, err)
	require.Equal(t, "running", api.containers[name].State.Status)
}

func TestDockerProvisionRejectsSettingsDockerCouldNotApply(t *testing.T) {
	api := newFakeAPI()
	api.createWarnings = []string{"Your kernel does not support PIDs limit capabilities. PIDs limit discarded."}
	_, err := provision(t, api, uuid.New(), uuid.New())
	require.ErrorIs(t, err, providers.ErrPermanent)
	require.ErrorContains(t, err, "PIDs limit discarded")
	require.Empty(t, api.starts)
}

func TestDockerProvisionNeverStartsExistingContainerWithDroppedLimit(t *testing.T) {
	api := newFakeAPI()
	api.dropMemoryLimit = true
	api.loseContainerCreate = true
	installationID, machineID := uuid.New(), uuid.New()
	_, err := provision(t, api, installationID, machineID)
	require.Error(t, err)

	_, err = provision(t, api, installationID, machineID)
	require.ErrorIs(t, err, providers.ErrPermanent)
	require.Empty(t, api.starts)
}

func TestDockerProvisionPullsMissingImage(t *testing.T) {
	api := newFakeAPI()
	api.missingImage = true
	installationID, machineID := uuid.New(), uuid.New()
	_, err := provision(t, api, installationID, machineID)
	require.NoError(t, err)
	name, _, err := machineIdentity(installationID, machineID)
	require.NoError(t, err)
	require.Equal(t, []string{testImage}, api.pulls)
	require.Equal(t, "running", api.containers[name].State.Status)
}

func TestDockerProvisionAsksOperatorToPullImageItCannotFetch(t *testing.T) {
	api := newFakeAPI()
	api.missingImage = true
	api.pullErr = apiError{StatusCode: http.StatusNotFound, Message: "pull access denied"}
	_, err := provision(t, api, uuid.New(), uuid.New())
	require.ErrorIs(t, err, providers.ErrPermanent)
	require.ErrorContains(t, err, "pull it on the Docker host first")
	require.Empty(t, api.starts)
}

func TestDockerProvisionFailsFastOnSettingsDockerRejects(t *testing.T) {
	api := newFakeAPI()
	api.createErr = apiError{StatusCode: http.StatusBadRequest, Message: "unknown or invalid runtime name: runsc"}
	_, err := provision(t, api, uuid.New(), uuid.New())
	require.ErrorIs(t, err, providers.ErrPermanent)
	require.ErrorContains(t, err, "unknown or invalid runtime name")
	require.Empty(t, api.starts)
}

func TestDockerProvisionRefusesResourcesOwnedByAnotherMachine(t *testing.T) {
	api := newFakeAPI()
	installationID, machineID := uuid.New(), uuid.New()
	name, labels, err := machineIdentity(installationID, machineID)
	require.NoError(t, err)
	foreign := map[string]string{installationLabel: labels[installationLabel], machineLabel: "mch_other"}
	api.networks[name] = network{Labels: foreign}

	_, err = provision(t, api, installationID, machineID)
	require.ErrorIs(t, err, errNotThisMachine)
	require.Empty(t, api.containerCreates)
	require.ErrorIs(t, newTestProvider(api).DeleteMachine(
		context.Background(), installationID, machineID, testMachineProvisioning(t, testImage, 2, 4096), name,
	), errNotThisMachine)
	require.Contains(t, api.networks, name)
}

func TestDockerCleanupFindsAndRemovesNetworkWithoutContainer(t *testing.T) {
	api := newFakeAPI()
	api.loseNetworkCreate = true
	installationID, machineID := uuid.New(), uuid.New()
	_, err := provision(t, api, installationID, machineID)
	require.Error(t, err)

	machineProvisioning := testMachineProvisioning(t, testImage, 2, 4096)
	resourceID, found, err := newTestProvider(api).InspectMachine(
		context.Background(), installationID, machineID, machineProvisioning, "",
	)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, newTestProvider(api).DeleteMachine(
		context.Background(), installationID, machineID, machineProvisioning, resourceID,
	))
	require.Empty(t, api.networks)
}

func TestDockerDeleteFinishesAfterPartialFailure(t *testing.T) {
	api := newFakeAPI()
	installationID, machineID := uuid.New(), uuid.New()
	result, err := provision(t, api, installationID, machineID)
	require.NoError(t, err)
	api.removeNetworkErr = errors.New("docker unavailable")
	machineProvisioning := testMachineProvisioning(t, testImage, 2, 4096)
	deleteMachine := func() error {
		return newTestProvider(api).DeleteMachine(
			context.Background(), installationID, machineID, machineProvisioning, result.ProviderResourceID,
		)
	}

	require.Error(t, deleteMachine())
	require.Empty(t, api.containers)
	require.NoError(t, deleteMachine())
	require.Empty(t, api.networks)
	require.NoError(t, deleteMachine())
}
