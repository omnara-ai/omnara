package modal

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/google/uuid"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func testInstallationID() storage.ID {
	return uuid.MustParse("00000000-0000-0000-0000-000000000002")
}

func testProvider(t *testing.T, rpc *fakeControlPlane) *provider {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterModalClientServer(server, rpc)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	t.Setenv("MODAL_SERVER_URL", "http://"+listener.Addr().String())
	t.Setenv("MODAL_LOGLEVEL", "ERROR")
	return &provider{
		app:          "agents",
		environment:  "staging",
		credential:   providerCredential{TokenID: "ak-test", TokenSecret: "as-test"},
		omnaraAPIURL: "https://api.omnara.test/v1",
	}
}

func testOwnershipTags(t *testing.T, machineID storage.ID) map[string]string {
	t.Helper()
	installationOwner, err := publicid.Encode(publicid.KindInstallation, testInstallationID())
	if err != nil {
		t.Fatal(err)
	}
	machineOwner, err := publicid.Encode(publicid.KindMachine, machineID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{installationTag: installationOwner, machineTag: machineOwner}
}

func testProvisioning(t *testing.T, region string) executionstore.MachineProvisioningConfig {
	t.Helper()
	cpu := 1
	memoryMB := 1024
	options := map[string]json.RawMessage{"image": json.RawMessage(`"registry.example/daemon:latest"`)}
	if region != "" {
		options["region"] = json.RawMessage(`"` + region + `"`)
	}
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: options,
	}
}

func testPolicy(
	t *testing.T,
	defaultProvisioning executionstore.MachineProvisioningConfig,
) executionstore.MachinePoolProviderPolicy {
	t.Helper()
	maxCPU := 8
	maxMemoryMB := 8192
	return executionstore.MachinePoolProviderPolicy{
		DefaultProvisioning: defaultProvisioning,
		ResourceLimits: executionstore.MachineResourceLimits{
			MaxTotalCPU:        &maxCPU,
			MaxTotalMemoryMB:   &maxMemoryMB,
			MaxMachineCPU:      &maxCPU,
			MaxMachineMemoryMB: &maxMemoryMB,
		},
		ProviderConfig: json.RawMessage(`{"app":"omnara"}`),
	}
}

func fakeSandboxID(seed string) string {
	id := make([]byte, 0, 22)
	for _, ch := range []byte(seed) {
		if ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' {
			id = append(id, ch)
		}
	}
	for len(id) < 22 {
		id = append(id, 'x')
	}
	return "sb-" + string(id[:22])
}

func testSandboxName(t *testing.T, machineID storage.ID) string {
	t.Helper()
	name, err := providers.MachineAllocationName(testInstallationID(), machineID)
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	return name
}

type fakeSandbox struct {
	name     string
	tags     map[string]string
	finished bool
}

type fakeControlPlane struct {
	pb.UnimplementedModalClientServer
	sandboxes     map[string]*fakeSandbox
	app           *pb.AppGetOrCreateRequest
	image         *pb.ImageGetOrCreateRequest
	secret        *pb.SecretGetOrCreateRequest
	create        *pb.SandboxCreateRequest
	lookup        *pb.SandboxGetFromNameRequest
	createCalls   int
	createErr     error
	createOnError bool
	lookupErr     error
	tagsErr       error
	waitErr       error
	terminateErr  error
	terminated    []string
	tagsHook      func(context.Context) error
}

func newFakeControlPlane() *fakeControlPlane {
	return &fakeControlPlane{sandboxes: map[string]*fakeSandbox{}}
}

func (m *fakeControlPlane) add(id, name string, tags map[string]string, running bool) {
	m.sandboxes[id] = &fakeSandbox{name: name, tags: tags, finished: !running}
}

func (m *fakeControlPlane) find(id string) (*fakeSandbox, error) {
	if current, ok := m.sandboxes[id]; ok {
		return current, nil
	}
	return nil, status.Error(codes.NotFound, "sandbox "+id+" not found")
}

func (*fakeControlPlane) AuthTokenGet(
	context.Context,
	*pb.AuthTokenGetRequest,
) (*pb.AuthTokenGetResponse, error) {
	return pb.AuthTokenGetResponse_builder{Token: "test-token"}.Build(), nil
}

func (m *fakeControlPlane) AppGetOrCreate(
	_ context.Context,
	req *pb.AppGetOrCreateRequest,
) (*pb.AppGetOrCreateResponse, error) {
	m.app = req
	return pb.AppGetOrCreateResponse_builder{AppId: "ap-test"}.Build(), nil
}

func (*fakeControlPlane) EnvironmentGetOrCreate(
	context.Context,
	*pb.EnvironmentGetOrCreateRequest,
) (*pb.EnvironmentGetOrCreateResponse, error) {
	return pb.EnvironmentGetOrCreateResponse_builder{
		Metadata: pb.EnvironmentMetadata_builder{
			Settings: pb.EnvironmentSettings_builder{ImageBuilderVersion: "2024.10"}.Build(),
		}.Build(),
	}.Build(), nil
}

func (m *fakeControlPlane) ImageGetOrCreate(
	_ context.Context,
	req *pb.ImageGetOrCreateRequest,
) (*pb.ImageGetOrCreateResponse, error) {
	m.image = req
	return pb.ImageGetOrCreateResponse_builder{
		ImageId: "im-test",
		Result:  pb.GenericResult_builder{Status: pb.GenericResult_GENERIC_STATUS_SUCCESS}.Build(),
	}.Build(), nil
}

func (m *fakeControlPlane) SecretGetOrCreate(
	_ context.Context,
	req *pb.SecretGetOrCreateRequest,
) (*pb.SecretGetOrCreateResponse, error) {
	m.secret = req
	return pb.SecretGetOrCreateResponse_builder{SecretId: "st-test"}.Build(), nil
}

func (m *fakeControlPlane) SandboxCreate(
	_ context.Context,
	req *pb.SandboxCreateRequest,
) (*pb.SandboxCreateResponse, error) {
	m.createCalls++
	m.create = req
	name := req.GetDefinition().GetName()
	if m.createErr == nil || m.createOnError {
		tags := make(map[string]string, len(req.GetTags()))
		for _, tag := range req.GetTags() {
			tags[tag.GetTagName()] = tag.GetTagValue()
		}
		m.add(fakeSandboxID(name), name, tags, true)
	}
	if m.createErr != nil {
		return nil, m.createErr
	}
	return pb.SandboxCreateResponse_builder{SandboxId: fakeSandboxID(name)}.Build(), nil
}

func (m *fakeControlPlane) SandboxGetFromName(
	_ context.Context,
	req *pb.SandboxGetFromNameRequest,
) (*pb.SandboxGetFromNameResponse, error) {
	m.lookup = req
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	for id, current := range m.sandboxes {
		if current.name == req.GetSandboxName() {
			return pb.SandboxGetFromNameResponse_builder{SandboxId: id}.Build(), nil
		}
	}
	return nil, status.Error(codes.NotFound, "sandbox "+req.GetSandboxName()+" not found")
}

func (m *fakeControlPlane) SandboxTagsGet(
	ctx context.Context,
	req *pb.SandboxTagsGetRequest,
) (*pb.SandboxTagsGetResponse, error) {
	if m.tagsHook != nil {
		if err := m.tagsHook(ctx); err != nil {
			return nil, err
		}
	}
	if m.tagsErr != nil {
		return nil, m.tagsErr
	}
	current, err := m.find(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	tags := make([]*pb.SandboxTag, 0, len(current.tags))
	for name, value := range current.tags {
		tags = append(tags, pb.SandboxTag_builder{TagName: name, TagValue: value}.Build())
	}
	return pb.SandboxTagsGetResponse_builder{Tags: tags}.Build(), nil
}

func (m *fakeControlPlane) SandboxWait(
	_ context.Context,
	req *pb.SandboxWaitRequest,
) (*pb.SandboxWaitResponse, error) {
	if m.waitErr != nil {
		return nil, m.waitErr
	}
	current, err := m.find(req.GetSandboxId())
	if err != nil {
		return nil, err
	}
	response := pb.SandboxWaitResponse_builder{}
	if current.finished {
		response.Result = pb.GenericResult_builder{Status: pb.GenericResult_GENERIC_STATUS_SUCCESS}.Build()
	}
	return response.Build(), nil
}

func (m *fakeControlPlane) SandboxTerminate(
	_ context.Context,
	req *pb.SandboxTerminateRequest,
) (*pb.SandboxTerminateResponse, error) {
	m.terminated = append(m.terminated, req.GetSandboxId())
	if m.terminateErr != nil {
		return nil, m.terminateErr
	}
	if current, ok := m.sandboxes[req.GetSandboxId()]; ok {
		current.finished = true
	}
	return pb.SandboxTerminateResponse_builder{}.Build(), nil
}
