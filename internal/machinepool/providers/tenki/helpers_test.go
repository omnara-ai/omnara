package tenki

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	sessions  []session
	requests  []createRequest
	createErr error
	rejectErr error
	hide      bool
	listErr   error
	getErr    error
	deletes   int
}

func (a *fakeAPI) Create(_ context.Context, request createRequest) (session, error) {
	a.requests = append(a.requests, request)
	if a.rejectErr != nil {
		return session{}, a.rejectErr
	}
	s := session{ID: uuid.NewString(), State: sessionStateCreating, Metadata: request.Metadata, Sticky: request.Sticky}
	a.sessions = append(a.sessions, s)
	if a.createErr != nil {
		return session{}, a.createErr
	}
	return s, nil
}
func (a *fakeAPI) List(context.Context) ([]session, error) {
	if a.listErr != nil {
		return nil, a.listErr
	}
	if a.hide {
		return nil, nil
	}
	var sessions []session
	for _, s := range a.sessions {
		if s.State != sessionStateTerminated {
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}
func (a *fakeAPI) Get(_ context.Context, id string) (session, bool, error) {
	if a.getErr != nil {
		return session{}, false, a.getErr
	}
	for _, s := range a.sessions {
		if s.ID == id {
			return s, true, nil
		}
	}
	return session{}, false, nil
}
func (a *fakeAPI) Delete(_ context.Context, id string) error {
	a.deletes++
	for i := range a.sessions {
		if a.sessions[i].ID == id {
			a.sessions[i].State = sessionStateTerminated
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

func ownedSession(t *testing.T, id, state string, installationID, machineID uuid.UUID) session {
	t.Helper()
	metadata, err := ownershipMetadata(installationID, machineID)
	require.NoError(t, err)
	return session{ID: id, State: state, Metadata: metadata, Sticky: true}
}
