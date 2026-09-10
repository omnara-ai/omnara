package modal

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestProviderProvisionCreatesSandbox(t *testing.T) {
	api := newFakeAPI()
	target := &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
	machineID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	result, err := target.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testProvisioning(t, "us-east"),
		"machine-token",
		map[string]string{"APP_ENV": "production"},
	)
	if err != nil {
		t.Fatalf("provision modal machine: %v", err)
	}
	name := testSandboxName(t, machineID)
	if result.ProviderResourceID != "sb-"+name {
		t.Fatalf("provider resource id = %q", result.ProviderResourceID)
	}
	request := api.createRequest
	if request.Name != name || request.Image != "registry.example/daemon:latest" ||
		request.CPU != 0.5 || request.MemoryMB != 1024 || request.Timeout != sandboxTimeout ||
		request.Region != "us-east" || !reflect.DeepEqual(request.Tags, testOwnershipTags(t, machineID)) {
		t.Fatalf("create request = %+v", request)
	}
	if !reflect.DeepEqual(request.Command, providers.ManagedDaemonLauncherArgs()) {
		t.Fatalf("command = %#v", request.Command)
	}
	if request.Env["APP_ENV"] != "production" ||
		request.Env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		request.Env[providers.ManagedBootstrapScriptEnvVar] == "" {
		t.Fatalf("create env = %+v", request.Env)
	}
	if _, err := base64.StdEncoding.DecodeString(request.Env[providers.ManagedBootstrapScriptEnvVar]); err != nil {
		t.Fatalf("decode bootstrap script: %v", err)
	}
}

func TestProviderProvisionOmitsAutomaticRegion(t *testing.T) {
	api := newFakeAPI()
	target := &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
	_, err := target.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		uuid.New(),
		testProvisioning(t, ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("provision modal machine: %v", err)
	}
	if api.createRequest.Region != "" {
		t.Fatalf("region = %q, want automatic placement", api.createRequest.Region)
	}
}

func TestProviderProvisionAdoptsExistingSandbox(t *testing.T) {
	api := newFakeAPI()
	machineID := uuid.New()
	name := testSandboxName(t, machineID)
	api.byName[name] = sandbox{ID: "sb-existing", Tags: testOwnershipTags(t, machineID), Running: true}
	target := &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
	result, err := target.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testProvisioning(t, ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("adopt modal sandbox: %v", err)
	}
	if result.ProviderResourceID != "sb-existing" || api.createCalls != 0 {
		t.Fatalf("result = %+v, create calls = %d", result, api.createCalls)
	}
}

func TestProviderProvisionRecoversAfterAmbiguousCreateError(t *testing.T) {
	api := newFakeAPI()
	api.createErr = errors.New("request timed out")
	api.createOnError = true
	target := &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
	machineID := uuid.New()
	result, err := target.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testProvisioning(t, ""),
		"machine-token",
		nil,
	)
	if err != nil {
		t.Fatalf("recover modal sandbox: %v", err)
	}
	if result.ProviderResourceID != "sb-"+testSandboxName(t, machineID) {
		t.Fatalf("result = %+v", result)
	}
}

func TestProviderRejectsSandboxWithoutOwnershipTag(t *testing.T) {
	api := newFakeAPI()
	machineID := uuid.New()
	name := testSandboxName(t, machineID)
	api.byName[name] = sandbox{ID: "sb-existing", Tags: map[string]string{}, Running: true}
	target := &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
	_, err := target.ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testProvisioning(t, ""),
		"machine-token",
		nil,
	)
	if err == nil {
		t.Fatal("expected ownership error")
	}
}

func TestProviderRetriesAfterExistingSandboxTerminates(t *testing.T) {
	api := newFakeAPI()
	machineID := uuid.New()
	name := testSandboxName(t, machineID)
	api.byName[name] = sandbox{ID: "sb-terminated", Tags: testOwnershipTags(t, machineID)}
	p := &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
	ctx := context.Background()
	provisioning := testProvisioning(t, "")
	result, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "token", nil)
	if !errors.Is(err, providers.ErrResourceReplaced) || result.ProviderResourceID != "" {
		t.Fatalf("terminated sandbox result = %+v, error = %v", result, err)
	}
	if api.createCalls != 0 || api.deleteCalls != 0 {
		t.Fatal("unexpected mutation of terminated sandbox")
	}
	delete(api.byName, name)
	result, err = p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "token", nil)
	if err != nil || result.ProviderResourceID != "sb-"+name || api.createCalls != 1 {
		t.Fatalf("replacement sandbox result = %+v, error = %v, creates = %d", result, err, api.createCalls)
	}
}

func TestProviderDeleteIsIdempotentForMissingOrFinishedSandbox(t *testing.T) {
	for _, test := range []struct {
		name    string
		target  sandbox
		present bool
	}{
		{name: "missing", target: sandbox{ID: "sb-missing"}},
		{name: "finished", target: sandbox{ID: "sb-finished", Running: false}, present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeAPI()
			machineID := uuid.New()
			if test.present {
				test.target.Tags = testOwnershipTags(t, machineID)
				api.byID[test.target.ID] = test.target
			}
			target := &provider{api: api}
			if err := target.DeleteMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testProvisioning(t, ""),
				test.target.ID,
			); err != nil {
				t.Fatalf("delete absent sandbox: %v", err)
			}
			if api.deleteCalls != 0 {
				t.Fatalf("delete calls = %d, want 0", api.deleteCalls)
			}
		})
	}
}

func TestProviderDeletesOwnedRunningSandbox(t *testing.T) {
	api := newFakeAPI()
	machineID := uuid.New()
	api.byID["sb-running"] = sandbox{
		ID:      "sb-running",
		Tags:    testOwnershipTags(t, machineID),
		Running: true,
	}
	target := &provider{api: api}
	if err := target.DeleteMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testProvisioning(t, ""),
		"sb-running",
	); err != nil {
		t.Fatalf("delete modal sandbox: %v", err)
	}
	if api.deletedID != "sb-running" {
		t.Fatalf("deleted id = %q", api.deletedID)
	}
}

func TestProviderRejectsMismatchedOwnershipTags(t *testing.T) {
	for _, tag := range []string{installationTag, machineTag} {
		for _, value := range []string{"", "another-owner"} {
			t.Run(tag+"/"+value, func(t *testing.T) {
				api := newFakeAPI()
				machineID := uuid.New()
				name := testSandboxName(t, machineID)
				tags := testOwnershipTags(t, machineID)
				tags[tag] = value
				current := sandbox{ID: "sb-foreign", Tags: tags, Running: true}
				api.byName[name] = current
				api.byID[current.ID] = current
				p := &provider{api: api}
				ctx := context.Background()
				provisioning := testProvisioning(t, "")
				result, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "token", nil)
				if err == nil {
					t.Fatal("adopted foreign sandbox")
				}
				if result.ProviderResourceID != "" {
					t.Fatalf("returned foreign sandbox id: %q", result.ProviderResourceID)
				}
				if err := p.DeleteMachine(ctx, testInstallationID(), machineID, provisioning, current.ID); err == nil {
					t.Fatal("deleted foreign sandbox")
				}
				target := providers.RuntimeTarget{
					InstallationID: testInstallationID(), MachineID: machineID, ProviderResourceID: current.ID,
				}
				observations, err := p.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{target})
				if err != nil || len(observations) != 1 || observations[0].State != providers.RuntimeStateUnknown {
					t.Fatalf("foreign observation: %+v, %v", observations, err)
				}
				if api.createCalls != 0 || api.deleteCalls != 0 {
					t.Fatal("mutated foreign sandbox")
				}
			})
		}
	}
}
