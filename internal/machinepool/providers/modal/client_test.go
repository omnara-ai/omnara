package modal

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	modalsdk "github.com/modal-labs/modal-client/go"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const adapterSandboxID = "sb-nGEijt9WbBMlGrsPH9FOaC"

type adapterRPC struct {
	pb.ModalClientClient
	app       *pb.AppGetOrCreateRequest
	image     *pb.ImageGetOrCreateRequest
	secret    *pb.SecretGetOrCreateRequest
	create    *pb.SandboxCreateRequest
	lookup    *pb.SandboxGetFromNameRequest
	tagsID    string
	waitID    string
	deletedID string
	lookupErr error
	tagsErr   error
	waitErr   error
	deleteErr error
	finished  bool
}

func (m *adapterRPC) AppGetOrCreate(
	_ context.Context,
	req *pb.AppGetOrCreateRequest,
	_ ...grpc.CallOption,
) (*pb.AppGetOrCreateResponse, error) {
	m.app = req
	return pb.AppGetOrCreateResponse_builder{AppId: "ap-test"}.Build(), nil
}

func (*adapterRPC) EnvironmentGetOrCreate(
	context.Context,
	*pb.EnvironmentGetOrCreateRequest,
	...grpc.CallOption,
) (*pb.EnvironmentGetOrCreateResponse, error) {
	return pb.EnvironmentGetOrCreateResponse_builder{
		Metadata: pb.EnvironmentMetadata_builder{
			Settings: pb.EnvironmentSettings_builder{ImageBuilderVersion: "2024.10"}.Build(),
		}.Build(),
	}.Build(), nil
}

func (m *adapterRPC) ImageGetOrCreate(
	_ context.Context,
	req *pb.ImageGetOrCreateRequest,
	_ ...grpc.CallOption,
) (*pb.ImageGetOrCreateResponse, error) {
	m.image = req
	return pb.ImageGetOrCreateResponse_builder{
		ImageId: "im-test",
		Result:  pb.GenericResult_builder{Status: pb.GenericResult_GENERIC_STATUS_SUCCESS}.Build(),
	}.Build(), nil
}

func (m *adapterRPC) SecretGetOrCreate(
	_ context.Context,
	req *pb.SecretGetOrCreateRequest,
	_ ...grpc.CallOption,
) (*pb.SecretGetOrCreateResponse, error) {
	m.secret = req
	return pb.SecretGetOrCreateResponse_builder{SecretId: "st-test"}.Build(), nil
}

func (m *adapterRPC) SandboxCreate(
	_ context.Context,
	req *pb.SandboxCreateRequest,
	_ ...grpc.CallOption,
) (*pb.SandboxCreateResponse, error) {
	m.create = req
	return pb.SandboxCreateResponse_builder{SandboxId: adapterSandboxID}.Build(), nil
}

func (m *adapterRPC) SandboxGetFromName(
	_ context.Context,
	req *pb.SandboxGetFromNameRequest,
	_ ...grpc.CallOption,
) (*pb.SandboxGetFromNameResponse, error) {
	m.lookup = req
	return pb.SandboxGetFromNameResponse_builder{SandboxId: adapterSandboxID}.Build(), m.lookupErr
}

func (m *adapterRPC) SandboxTagsGet(
	_ context.Context,
	req *pb.SandboxTagsGetRequest,
	_ ...grpc.CallOption,
) (*pb.SandboxTagsGetResponse, error) {
	m.tagsID = req.GetSandboxId()
	return pb.SandboxTagsGetResponse_builder{
		Tags: []*pb.SandboxTag{pb.SandboxTag_builder{TagName: machineTag, TagValue: "owned"}.Build()},
	}.Build(), m.tagsErr
}

func (m *adapterRPC) SandboxWait(
	_ context.Context,
	req *pb.SandboxWaitRequest,
	_ ...grpc.CallOption,
) (*pb.SandboxWaitResponse, error) {
	m.waitID = req.GetSandboxId()
	response := pb.SandboxWaitResponse_builder{}
	if m.finished {
		response.Result = pb.GenericResult_builder{Status: pb.GenericResult_GENERIC_STATUS_SUCCESS}.Build()
	}
	return response.Build(), m.waitErr
}

func (m *adapterRPC) SandboxTerminate(
	_ context.Context,
	req *pb.SandboxTerminateRequest,
	_ ...grpc.CallOption,
) (*pb.SandboxTerminateResponse, error) {
	m.deletedID = req.GetSandboxId()
	return pb.SandboxTerminateResponse_builder{}.Build(), m.deleteErr
}

func newAdapterClient(t *testing.T, rpc *adapterRPC) *modalAPI {
	t.Helper()
	client, err := modalsdk.NewClientWithOptions(&modalsdk.ClientParams{
		TokenID: "ak-test", TokenSecret: "as-test", Environment: "staging", ControlPlaneClient: rpc,
	})
	if err != nil {
		t.Fatalf("create SDK client: %v", err)
	}
	t.Cleanup(client.Close)
	return &modalAPI{client: client, app: "agents", environment: "staging"}
}

func TestClientCreateRequestMapping(t *testing.T) {
	for _, region := range []string{"", "us-east"} {
		t.Run("region="+region, func(t *testing.T) {
			rpc := &adapterRPC{}
			client := newAdapterClient(t, rpc)
			request := createSandboxRequest{
				Name: "owned", Image: "alpine:latest", CPU: 0.5, MemoryMB: 1024,
				Timeout: 24 * time.Hour, Command: []string{"sh", "-c", "sleep 60"},
				Env: map[string]string{"OMNARA_TEST": "value"}, Region: region,
				Tags: map[string]string{machineTag: "owned"},
			}
			created, err := client.CreateSandbox(t.Context(), request)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if diff := cmp.Diff(sandbox{ID: adapterSandboxID, Tags: request.Tags, Running: true}, created); diff != "" {
				t.Fatal(diff)
			}
			if rpc.app.GetAppName() != "agents" ||
				rpc.app.GetEnvironmentName() != "staging" ||
				rpc.app.GetObjectCreationType() != pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_IF_MISSING {
				t.Fatalf("incorrect app request: %v", rpc.app)
			}
			if diff := cmp.Diff([]string{"FROM alpine:latest"}, rpc.image.GetImage().GetDockerfileCommands()); diff != "" {
				t.Fatal(diff)
			}
			if diff := cmp.Diff(request.Env, rpc.secret.GetEnvDict()); diff != "" {
				t.Fatal(diff)
			}
			if rpc.secret.GetEnvironmentName() != "staging" {
				t.Fatal("incorrect secret environment")
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
				definition.GetName() != "owned" ||
				definition.GetTimeoutSecs() != 86400 {
				t.Fatalf("incorrect create request: %v", rpc.create)
			}
			if diff := cmp.Diff(request.Command, definition.GetEntrypointArgs()); diff != "" {
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
			if tags := rpc.create.GetTags(); len(tags) != 1 ||
				tags[0].GetTagName() != machineTag ||
				tags[0].GetTagValue() != "owned" {
				t.Fatalf("incorrect tags: %v", tags)
			}
		})
	}
}

func TestClientLookupAndRuntimeStatus(t *testing.T) {
	for _, test := range []struct {
		name                        string
		lookupErr, tagsErr, waitErr error
		finished, found, wantError  bool
	}{
		{name: "running", found: true},
		{name: "finished", finished: true, found: true},
		{name: "missing name", lookupErr: status.Error(codes.NotFound, "missing")},
		{name: "missing tags", tagsErr: status.Error(codes.NotFound, "missing")},
		{name: "missing poll", waitErr: status.Error(codes.NotFound, "missing")},
		{name: "lookup failure", lookupErr: status.Error(codes.Unavailable, "offline"), wantError: true},
		{name: "tags failure", tagsErr: status.Error(codes.PermissionDenied, "denied"), wantError: true},
		{name: "poll failure", waitErr: status.Error(codes.Unavailable, "offline"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := &adapterRPC{lookupErr: test.lookupErr, tagsErr: test.tagsErr, waitErr: test.waitErr, finished: test.finished}
			client := newAdapterClient(t, rpc)
			current, found, err := client.GetSandboxByName(t.Context(), "owned")
			if found != test.found || (err != nil) != test.wantError {
				t.Fatalf("lookup found=%v error=%v", found, err)
			}
			if rpc.lookup.GetAppName() != "agents" ||
				rpc.lookup.GetEnvironmentName() != "staging" ||
				rpc.lookup.GetSandboxName() != "owned" {
				t.Fatalf("incorrect lookup: %v", rpc.lookup)
			}
			if test.found && (current.ID != adapterSandboxID ||
				current.Running == test.finished ||
				current.Tags[machineTag] != "owned") {
				t.Fatalf("incorrect sandbox: %+v", current)
			}
			if test.lookupErr != nil {
				return
			}
			_, found, err = client.GetSandboxByID(t.Context(), adapterSandboxID)
			if found != test.found || (err != nil) != test.wantError {
				t.Fatalf("exact lookup found=%v error=%v", found, err)
			}
			if rpc.tagsID != adapterSandboxID || (test.tagsErr == nil && rpc.waitID != adapterSandboxID) {
				t.Fatal("incorrect exact lookup id")
			}
		})
	}
}

func TestClientDeleteNormalizesNotFound(t *testing.T) {
	for _, code := range []codes.Code{codes.OK, codes.NotFound, codes.PermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			rpc := &adapterRPC{deleteErr: status.Error(code, "delete")}
			err := newAdapterClient(t, rpc).DeleteSandbox(t.Context(), adapterSandboxID)
			if (err != nil) != (code == codes.PermissionDenied) {
				t.Fatalf("delete: %v", err)
			}
			if rpc.deletedID != adapterSandboxID {
				t.Fatal("incorrect deletion id")
			}
		})
	}
}
