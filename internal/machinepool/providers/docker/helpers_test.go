package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type fakeAPI struct {
	networks   map[string]network
	containers map[string]container

	loseNetworkCreate   bool
	loseContainerCreate bool
	createWarnings      []string
	missingImage        bool
	createErr           error
	pullErr             error
	dropMemoryLimit     bool
	removeNetworkErr    error

	containerCreates []containerCreateRequest
	starts           []string
	pulls            []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{networks: map[string]network{}, containers: map[string]container{}}
}

func (f *fakeAPI) Ping(context.Context) error {
	return nil
}

func (f *fakeAPI) CreateNetwork(_ context.Context, request networkCreateRequest) error {
	if _, exists := f.networks[request.Name]; exists {
		return apiError{StatusCode: http.StatusConflict}
	}
	f.networks[request.Name] = network{Labels: request.Labels}
	if f.loseNetworkCreate {
		f.loseNetworkCreate = false
		return context.DeadlineExceeded
	}
	return nil
}

func (f *fakeAPI) InspectNetwork(_ context.Context, name string) (network, bool, error) {
	current, found := f.networks[name]
	return current, found, nil
}

func (f *fakeAPI) RemoveNetwork(_ context.Context, name string) error {
	if f.removeNetworkErr != nil {
		err := f.removeNetworkErr
		f.removeNetworkErr = nil
		return err
	}
	delete(f.networks, name)
	return nil
}

func (f *fakeAPI) CreateContainer(
	_ context.Context,
	name string,
	request containerCreateRequest,
) (containerCreateResponse, error) {
	f.containerCreates = append(f.containerCreates, request)
	if f.createErr != nil {
		return containerCreateResponse{}, f.createErr
	}
	if f.missingImage {
		return containerCreateResponse{}, apiError{
			StatusCode: http.StatusNotFound,
			Message:    "No such image: " + request.Image,
		}
	}
	if _, exists := f.containers[name]; exists {
		return containerCreateResponse{}, apiError{StatusCode: http.StatusConflict}
	}
	stored := request.HostConfig
	if f.dropMemoryLimit {
		stored.Memory = 0
		stored.MemorySwap = -1
	}
	f.containers[name] = container{
		State:      containerState{Status: "created"},
		Config:     containerConfig{Labels: request.Labels},
		HostConfig: stored,
	}
	if f.loseContainerCreate {
		f.loseContainerCreate = false
		return containerCreateResponse{}, context.DeadlineExceeded
	}
	return containerCreateResponse{Warnings: f.createWarnings}, nil
}

func (f *fakeAPI) InspectContainer(_ context.Context, name string) (container, bool, error) {
	current, found := f.containers[name]
	return current, found, nil
}

func (f *fakeAPI) StartContainer(_ context.Context, name string) error {
	f.starts = append(f.starts, name)
	current := f.containers[name]
	current.State.Status = "running"
	f.containers[name] = current
	return nil
}

func (f *fakeAPI) RemoveContainer(_ context.Context, name string) error {
	delete(f.containers, name)
	return nil
}

func (f *fakeAPI) ListContainers(_ context.Context, filters map[string][]string) ([]containerSummary, error) {
	var listed []containerSummary
	for name, current := range f.containers {
		matches := true
		for _, label := range filters["label"] {
			key, value, _ := strings.Cut(label, "=")
			if current.Config.Labels[key] != value {
				matches = false
			}
		}
		if matches {
			listed = append(listed, containerSummary{
				Names:  []string{"/" + name},
				Labels: current.Config.Labels,
				State:  current.State.Status,
			})
		}
	}
	return listed, nil
}

func (f *fakeAPI) PullImage(_ context.Context, image string) error {
	f.pulls = append(f.pulls, image)
	if f.pullErr != nil {
		return f.pullErr
	}
	f.missingImage = false
	return nil
}

func newTestProvider(api apiClient) *provider {
	return &provider{api: api, omnaraAPIURL: "https://api.omnara.test/v1"}
}

func testMachineProvisioning(t *testing.T, image string, cpu, memoryMB int) executionstore.MachineProvisioningConfig {
	t.Helper()
	options := map[string]json.RawMessage{}
	raw, err := json.Marshal(image)
	if err != nil {
		t.Fatal(err)
	}
	options["image"] = raw
	return executionstore.MachineProvisioningConfig{CPU: &cpu, MemoryMB: &memoryMB, ProviderOptions: options}
}
