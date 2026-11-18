package docker

import (
	"context"
	"encoding/json"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/testutil/providercontract"
	"github.com/stretchr/testify/require"
)

func TestDockerProviderLiveSmoke(t *testing.T) {
	hostPort, isTCP := strings.CutPrefix(strings.TrimSpace(os.Getenv("DOCKER_HOST")), "tcp://")
	if !isTCP || os.Getenv("DOCKER_TLS_VERIFY") == "" {
		t.Skip("a TLS Docker host in DOCKER_HOST with DOCKER_TLS_VERIFY is required")
	}
	image := strings.TrimSpace(os.Getenv("OMNARA_DOCKER_TEST_IMAGE"))
	if image == "" {
		image = testImage
	}
	machineProvider, err := newProvider(
		json.RawMessage(`{"api_base_url":"https://`+hostPort+`"}`),
		providers.RuntimeConfig{
			OmnaraAPIURL:      "https://api.omnara.test/v1",
			ProviderAuthToken: liveCredential(t),
			EndpointAllowlist: liveAllowlist(t, hostPort),
		},
	)
	require.NoError(t, err)
	machineProvider.api = liveTestAPI{apiClient: machineProvider.api}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	provisioning := testMachineProvisioning(t, image, 1, 1024)
	provisioning.ProviderOptions["startup_script"] = json.RawMessage(`"sleep 300"`)
	require.NoError(t, machineProvider.CheckConnection(ctx, provisioning))
	installationID, machineID := uuid.New(), uuid.New()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		id, found, err := machineProvider.InspectMachine(cleanupCtx, installationID, machineID, provisioning, "")
		if err != nil {
			t.Errorf("find live docker machine for cleanup: %v", err)
			return
		}
		if !found {
			return
		}
		if err := machineProvider.DeleteMachine(cleanupCtx, installationID, machineID, provisioning, id); err != nil {
			t.Errorf("delete live docker machine: %v", err)
		}
	})
	provision := func(firstAttempt bool) string {
		provisionCtx, provisionCancel := context.WithTimeout(ctx, provisioningTimeout)
		defer provisionCancel()
		result, err := machineProvider.ProvisionMachine(
			provisionCtx, installationID, machineID, provisioning, "live-smoke-token", nil, firstAttempt,
		)
		require.NoError(t, err, "first attempt %t", firstAttempt)
		return result.ProviderResourceID
	}
	created := provision(true)
	require.NotEmpty(t, created)
	require.Equal(t, created, provision(false))
	inspected, found, err := machineProvider.InspectMachine(ctx, installationID, machineID, provisioning, "")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, created, inspected)

	runtimeTarget := providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           machineID,
		ProviderResourceID:  created,
		MachineProvisioning: provisioning,
	}
	providercontract.WaitForPresentRuntimeObservation(t, ctx, runtimeTarget, func() (providers.RuntimeObservation, error) {
		return machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
	})
	observations, err := machineProvider.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{runtimeTarget})
	require.NoError(t, err)
	require.Len(t, observations, 1)
	providercontract.AssertRuntimeObservation(t, runtimeTarget, observations[0], providers.RuntimeStateRunning)
	missingTarget := runtimeTarget
	missingTarget.MachineID = uuid.New()
	missingTarget.ProviderResourceID, _, err = machineIdentity(installationID, missingTarget.MachineID)
	require.NoError(t, err)
	missing, err := machineProvider.ObserveRuntimeState(ctx, missingTarget)
	require.NoError(t, err)
	providercontract.AssertRuntimeObservation(t, missingTarget, missing, providers.RuntimeStateTerminated)

	require.Error(t, machineProvider.DeleteMachine(ctx, uuid.New(), machineID, provisioning, created))
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, created))
	observation, err := machineProvider.ObserveRuntimeState(ctx, runtimeTarget)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateTerminated, observation.State)
	_, found, err = machineProvider.InspectMachine(ctx, installationID, machineID, provisioning, "")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, machineProvider.DeleteMachine(ctx, installationID, machineID, provisioning, created))
}

func liveCredential(t *testing.T) string {
	t.Helper()
	certPath := os.Getenv("DOCKER_CERT_PATH")
	if certPath == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		certPath = filepath.Join(home, ".docker")
	}
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(certPath, name))
		require.NoError(t, err)
		return string(raw)
	}
	return credentialJSON(t, read("ca.pem"), read("cert.pem"), read("key.pem"))
}

func liveAllowlist(t *testing.T, hostPort string) *ssrf.Allowlist {
	t.Helper()
	host, port, err := net.SplitHostPort(hostPort)
	require.NoError(t, err)
	addresses, err := net.DefaultResolver.LookupHost(t.Context(), host)
	require.NoError(t, err)
	entries := make([]string, 0, len(addresses))
	for _, address := range addresses {
		entries = append(entries, net.JoinHostPort(address, port))
	}
	allowlist, err := ssrf.ParseAllowlist(entries)
	require.NoError(t, err)
	return allowlist
}

type liveTestAPI struct {
	apiClient
}

func (a liveTestAPI) CreateNetwork(ctx context.Context, request networkCreateRequest) error {
	request.Labels = liveLabels(request.Labels)
	return a.apiClient.CreateNetwork(ctx, request)
}

func (a liveTestAPI) CreateContainer(
	ctx context.Context,
	name string,
	request containerCreateRequest,
) (containerCreateResponse, error) {
	request.Labels = liveLabels(request.Labels)
	return a.apiClient.CreateContainer(ctx, name, request)
}

func liveLabels(labels map[string]string) map[string]string {
	labels = maps.Clone(labels)
	labels[providercontract.LiveResourceLabel] = providercontract.LiveResourceValue
	return labels
}
