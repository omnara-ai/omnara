package unikraft

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestUnikraftProviderLiveSmoke(t *testing.T) {
	if os.Getenv("OMNARA_UNIKRAFT_LIVE") != "1" {
		t.Skip("set OMNARA_UNIKRAFT_LIVE=1 to run live Unikraft smoke test")
	}
	token := os.Getenv("OMNARA_UNIKRAFT_API_TOKEN")
	image := os.Getenv("OMNARA_UNIKRAFT_TEST_IMAGE")
	if token == "" || image == "" {
		t.Skip("OMNARA_UNIKRAFT_API_TOKEN and OMNARA_UNIKRAFT_TEST_IMAGE are required")
	}
	omnaraPublicURL := os.Getenv("OMNARA_PUBLIC_URL")
	if omnaraPublicURL == "" {
		omnaraPublicURL = "https://app.omnara.com"
	}
	machineProvisioning := testMachineProvisioning(
		t,
		map[string]any{"provider_options": map[string]any{"image": image}},
	)
	provider, err := (Definition{}).NewProvider(
		mustRawJSON(t, map[string]any{"allowed_images": []string{image}, "allowed_metros": []string{"*"}}),
		providers.RuntimeConfig{PublicURL: omnaraPublicURL, ProviderAuthToken: token},
	)
	if err != nil {
		t.Fatalf("new unikraft provider: %v", err)
	}
	machineID := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	providerResourceID, err := provider.ProvisionMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		"live-smoke-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision live unikraft instance: %v", err)
	}
	if providerResourceID.ProviderResourceID == "" {
		t.Fatal("expected provider resource id")
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := provider.DeleteMachine(
			cleanupCtx,
			testInstallationID(),
			machineID,
			machineProvisioning,
			providerResourceID.ProviderResourceID,
		); err != nil {
			t.Fatalf("delete live unikraft instance: %v", err)
		}
	}()
	_, found, err := provider.InspectMachine(
		ctx,
		testInstallationID(),
		machineID,
		machineProvisioning,
		providerResourceID.ProviderResourceID,
	)
	if err != nil {
		t.Fatalf("inspect live unikraft instance: %v", err)
	}
	if !found {
		t.Fatal("expected live instance to be found")
	}
}
