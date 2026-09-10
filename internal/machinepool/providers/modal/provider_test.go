package modal

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestProviderProvisionCreatesSandbox(t *testing.T) {
	for _, region := range []string{"", "us-east"} {
		t.Run("region="+region, func(t *testing.T) {
			rpc := newFakeControlPlane()
			machineID := uuid.New()
			result, err := testProvider(t, rpc).ProvisionMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testProvisioning(t, region),
				"machine-token",
				map[string]string{"APP_ENV": "production"},
			)
			if err != nil {
				t.Fatalf("provision modal machine: %v", err)
			}
			name := testSandboxName(t, machineID)
			if result.ProviderResourceID != fakeSandboxID(name) {
				t.Fatalf("provider resource id = %q", result.ProviderResourceID)
			}
			if rpc.app.GetAppName() != "agents" ||
				rpc.app.GetEnvironmentName() != "staging" ||
				rpc.app.GetObjectCreationType() != pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_IF_MISSING {
				t.Fatalf("incorrect app request: %v", rpc.app)
			}
			if diff := cmp.Diff(
				[]string{"FROM registry.example/daemon:latest"},
				rpc.image.GetImage().GetDockerfileCommands(),
			); diff != "" {
				t.Fatal(diff)
			}
			env := rpc.secret.GetEnvDict()
			if rpc.secret.GetEnvironmentName() != "staging" ||
				env["APP_ENV"] != "production" ||
				env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
				env[providers.ManagedBootstrapScriptEnvVar] == "" {
				t.Fatalf("create env = %+v", env)
			}
			if _, err := base64.StdEncoding.DecodeString(env[providers.ManagedBootstrapScriptEnvVar]); err != nil {
				t.Fatalf("decode bootstrap script: %v", err)
			}
			definition := rpc.create.GetDefinition()
			resources := definition.GetResources()
			if resources.GetMilliCpu() != 500 ||
				resources.GetMilliCpuMax() != 500 ||
				resources.GetMemoryMb() != 1024 ||
				resources.GetMemoryMbMax() != 1024 {
				t.Fatalf("incorrect resources: %v", resources)
			}
			if rpc.create.GetAppId() != "ap-test" ||
				definition.GetImageId() != "im-test" ||
				definition.GetName() != name ||
				definition.GetTimeoutSecs() != 86400 {
				t.Fatalf("incorrect create request: %v", rpc.create)
			}
			if diff := cmp.Diff(providers.ManagedDaemonLauncherArgs(), definition.GetEntrypointArgs()); diff != "" {
				t.Fatal(diff)
			}
			if diff := cmp.Diff([]string{"st-test"}, definition.GetSecretIds()); diff != "" {
				t.Fatal(diff)
			}
			var regions []string
			if region != "" {
				regions = []string{region}
			}
			if diff := cmp.Diff(regions, definition.GetSchedulerPlacement().GetRegions()); diff != "" {
				t.Fatal(diff)
			}
			if diff := cmp.Diff(testOwnershipTags(t, machineID), rpc.sandboxes[fakeSandboxID(name)].tags); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestProviderProvisionFailsOnLookupError(t *testing.T) {
	rpc := newFakeControlPlane()
	rpc.lookupErr = status.Error(codes.PermissionDenied, "denied")
	machineID := uuid.New()
	_, err := testProvider(t, rpc).ProvisionMachine(
		context.Background(),
		testInstallationID(),
		machineID,
		testProvisioning(t, ""),
		"machine-token",
		nil,
	)
	if status.Code(err) != codes.PermissionDenied || rpc.createCalls != 0 {
		t.Fatalf("lookup failure: %v, create calls = %d", err, rpc.createCalls)
	}
	if rpc.lookup.GetAppName() != "agents" ||
		rpc.lookup.GetEnvironmentName() != "staging" ||
		rpc.lookup.GetSandboxName() != testSandboxName(t, machineID) {
		t.Fatalf("incorrect lookup: %v", rpc.lookup)
	}
}

func TestProviderProvisionAdoptsExistingSandbox(t *testing.T) {
	rpc := newFakeControlPlane()
	machineID := uuid.New()
	rpc.add(fakeSandboxID("existing"), testSandboxName(t, machineID), testOwnershipTags(t, machineID), true)
	result, err := testProvider(t, rpc).ProvisionMachine(
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
	if result.ProviderResourceID != fakeSandboxID("existing") || rpc.createCalls != 0 {
		t.Fatalf("result = %+v, create calls = %d", result, rpc.createCalls)
	}
}

func TestProviderProvisionRecoversAfterAmbiguousCreateError(t *testing.T) {
	rpc := newFakeControlPlane()
	rpc.createErr = errors.New("request timed out")
	rpc.createOnError = true
	machineID := uuid.New()
	result, err := testProvider(t, rpc).ProvisionMachine(
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
	if result.ProviderResourceID != fakeSandboxID(testSandboxName(t, machineID)) {
		t.Fatalf("result = %+v", result)
	}
}

func TestProviderRetriesAfterExistingSandboxTerminates(t *testing.T) {
	rpc := newFakeControlPlane()
	machineID := uuid.New()
	name := testSandboxName(t, machineID)
	rpc.add(fakeSandboxID("terminated"), name, testOwnershipTags(t, machineID), false)
	p := testProvider(t, rpc)
	ctx := context.Background()
	provisioning := testProvisioning(t, "")
	result, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "token", nil)
	if !errors.Is(err, providers.ErrResourceReplaced) || result.ProviderResourceID != "" {
		t.Fatalf("terminated sandbox result = %+v, error = %v", result, err)
	}
	if rpc.createCalls != 0 || len(rpc.terminated) != 0 {
		t.Fatal("unexpected mutation of terminated sandbox")
	}
	delete(rpc.sandboxes, fakeSandboxID("terminated"))
	result, err = p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "token", nil)
	if err != nil || result.ProviderResourceID != fakeSandboxID(name) || rpc.createCalls != 1 {
		t.Fatalf("replacement sandbox result = %+v, error = %v, creates = %d", result, err, rpc.createCalls)
	}
}

func TestProviderDeleteIsIdempotentForMissingOrFinishedSandbox(t *testing.T) {
	for _, test := range []struct {
		name         string
		present      bool
		terminateErr error
	}{
		{name: "missing"},
		{name: "finished", present: true},
		{name: "terminate not found", present: true, terminateErr: status.Error(codes.NotFound, "gone")},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := newFakeControlPlane()
			rpc.terminateErr = test.terminateErr
			machineID := uuid.New()
			if test.present {
				running := test.terminateErr != nil
				rpc.add(fakeSandboxID("target"), testSandboxName(t, machineID), testOwnershipTags(t, machineID), running)
			}
			if err := testProvider(t, rpc).DeleteMachine(
				context.Background(),
				testInstallationID(),
				machineID,
				testProvisioning(t, ""),
				fakeSandboxID("target"),
			); err != nil {
				t.Fatalf("delete absent sandbox: %v", err)
			}
			if (len(rpc.terminated) != 0) != (test.terminateErr != nil) {
				t.Fatalf("terminated = %v", rpc.terminated)
			}
		})
	}
}

func TestProviderDeletesOwnedRunningSandbox(t *testing.T) {
	for _, terminateErr := range []error{nil, status.Error(codes.PermissionDenied, "denied")} {
		rpc := newFakeControlPlane()
		rpc.terminateErr = terminateErr
		machineID := uuid.New()
		rpc.add(fakeSandboxID("running"), testSandboxName(t, machineID), testOwnershipTags(t, machineID), true)
		err := testProvider(t, rpc).DeleteMachine(
			context.Background(),
			testInstallationID(),
			machineID,
			testProvisioning(t, ""),
			fakeSandboxID("running"),
		)
		if (err != nil) != (terminateErr != nil) ||
			len(rpc.terminated) != 1 || rpc.terminated[0] != fakeSandboxID("running") {
			t.Fatalf("delete modal sandbox: %v, terminated = %v", err, rpc.terminated)
		}
	}
}

func TestProviderRejectsMismatchedOwnershipTags(t *testing.T) {
	for _, tag := range []string{installationTag, machineTag} {
		for _, value := range []string{"", "another-owner"} {
			t.Run(tag+"/"+value, func(t *testing.T) {
				rpc := newFakeControlPlane()
				machineID := uuid.New()
				tags := testOwnershipTags(t, machineID)
				tags[tag] = value
				rpc.add(fakeSandboxID("foreign"), testSandboxName(t, machineID), tags, true)
				p := testProvider(t, rpc)
				ctx := context.Background()
				provisioning := testProvisioning(t, "")
				result, err := p.ProvisionMachine(ctx, testInstallationID(), machineID, provisioning, "token", nil)
				if err == nil {
					t.Fatal("adopted foreign sandbox")
				}
				if result.ProviderResourceID != "" {
					t.Fatalf("returned foreign sandbox id: %q", result.ProviderResourceID)
				}
				err = p.DeleteMachine(ctx, testInstallationID(), machineID, provisioning, fakeSandboxID("foreign"))
				if err == nil {
					t.Fatal("deleted foreign sandbox")
				}
				target := providers.RuntimeTarget{
					InstallationID: testInstallationID(), MachineID: machineID, ProviderResourceID: fakeSandboxID("foreign"),
				}
				observations, err := p.ObserveRuntimeStates(ctx, []providers.RuntimeTarget{target})
				if err != nil || len(observations) != 1 || observations[0].State != providers.RuntimeStateUnknown {
					t.Fatalf("foreign observation: %+v, %v", observations, err)
				}
				if rpc.createCalls != 0 || len(rpc.terminated) != 0 {
					t.Fatal("mutated foreign sandbox")
				}
			})
		}
	}
}
