package revyl

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func testInstallationID() uuid.UUID {
	return uuid.MustParse("10000000-0000-0000-0000-000000000001")
}

func testMachineID() uuid.UUID {
	return uuid.MustParse("20000000-0000-0000-0000-000000000002")
}

func testComputerID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := machineComputerID(testInstallationID(), testMachineID())
	if err != nil {
		t.Fatalf("machine computer id: %v", err)
	}
	return id
}

type fakeAPI struct {
	createRequests []createComputerRequest
	createResult   computer
	createErr      error
	getResults     []computer
	getFound       bool
	getErr         error
	stopCalls      []uuid.UUID
	stopErr        error
	listErr        error
	writes         map[string][]byte
	execRequests   []execRequest
	execResult     execResult
	execErr        error
}

func (f *fakeAPI) CreateComputer(_ context.Context, request createComputerRequest) (computer, error) {
	f.createRequests = append(f.createRequests, request)
	return f.createResult, f.createErr
}

func (f *fakeAPI) GetComputer(context.Context, uuid.UUID) (computer, bool, error) {
	if f.getErr != nil || !f.getFound {
		return computer{}, false, f.getErr
	}
	current := f.getResults[0]
	if len(f.getResults) > 1 {
		f.getResults = f.getResults[1:]
	}
	return current, true, nil
}

func (f *fakeAPI) StopComputer(_ context.Context, id uuid.UUID) (computer, bool, error) {
	f.stopCalls = append(f.stopCalls, id)
	return computer{}, f.stopErr == nil, f.stopErr
}

func (f *fakeAPI) ListComputers(context.Context, int) error {
	return f.listErr
}

func (f *fakeAPI) WriteFile(_ context.Context, _ uuid.UUID, path string, data []byte) error {
	if f.writes == nil {
		f.writes = map[string][]byte{}
	}
	f.writes[path] = append([]byte(nil), data...)
	return nil
}

func (f *fakeAPI) Exec(_ context.Context, _ uuid.UUID, request execRequest) (execResult, error) {
	f.execRequests = append(f.execRequests, request)
	return f.execResult, f.execErr
}
