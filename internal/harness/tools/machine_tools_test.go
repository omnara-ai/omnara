package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestMachineToolInputValidation(t *testing.T) {
	valid := []model.ToolCall{
		{Name: "create_machine", Input: json.RawMessage(`{}`)},
		{Name: "create_machine", Input: json.RawMessage(`{"machine_pool_name":"Build Pool"}`)},
		{Name: "delete_machine", Input: json.RawMessage(`{"machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		{Name: "inspect_machine", Input: json.RawMessage(`{}`)},
		{Name: "inspect_machine", Input: json.RawMessage(`{"machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		{Name: "list_machines", Input: json.RawMessage(`{}`)},
	}
	for _, call := range valid {
		if err := validateRegisteredToolInput(call.Name, call.Input); err != nil {
			t.Fatalf("%s validation failed: %v", call.Name, err)
		}
	}
	invalid := []struct {
		name string
		call model.ToolCall
	}{
		{
			name: "create rejects null",
			call: model.ToolCall{Name: "create_machine", Input: json.RawMessage(`{"machine_pool_name":null}`)},
		},
		{
			name: "create rejects extra",
			call: model.ToolCall{
				Name:  "create_machine",
				Input: json.RawMessage(`{"machine_pool_name":"Build Pool","count":2}`),
			},
		},
		{
			name: "delete requires ID",
			call: model.ToolCall{Name: "delete_machine", Input: json.RawMessage(`{}`)},
		},
		{
			name: "inspect rejects null",
			call: model.ToolCall{Name: "inspect_machine", Input: json.RawMessage(`{"machine_id":null}`)},
		},
		{
			name: "list rejects fields",
			call: model.ToolCall{Name: "list_machines", Input: json.RawMessage(`{"machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRegisteredToolInput(tc.call.Name, tc.call.Input); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestSelectCreateMachinePool(t *testing.T) {
	first := executionstore.MachinePoolSourceRecord{MachinePoolName: "Build Pool"}
	second := executionstore.MachinePoolSourceRecord{MachinePoolName: "Test Pool"}
	tests := []struct {
		name    string
		sources []executionstore.MachinePoolSourceRecord
		input   createMachineRequest
		want    string
		wantErr string
	}{
		{name: "omitted with zero sources", wantErr: "no machine pools are configured"},
		{
			name:    "omitted with one source",
			sources: []executionstore.MachinePoolSourceRecord{first},
			want:    first.MachinePoolName,
		},
		{
			name:    "omitted with multiple sources",
			sources: []executionstore.MachinePoolSourceRecord{first, second},
			wantErr: "machine_pool_name is required",
		},
		{
			name:    "explicit matching source",
			sources: []executionstore.MachinePoolSourceRecord{first, second},
			input:   createMachineRequest{MachinePoolName: second.MachinePoolName},
			want:    second.MachinePoolName,
		},
		{
			name:    "explicit missing source",
			sources: []executionstore.MachinePoolSourceRecord{first},
			input:   createMachineRequest{MachinePoolName: second.MachinePoolName},
			wantErr: "machine_pool_name is not configured",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectPoolForMachineCreate(tc.sources, tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("select create machine pool: %v", err)
			}
			if got.MachinePoolName != tc.want {
				t.Fatalf("selected pool = %q, want %q", got.MachinePoolName, tc.want)
			}
		})
	}
}

func TestSelectOnlyMachine(t *testing.T) {
	first := executionstore.PoolMachineRecord{
		Binding: executionstore.AgentMachineBindingRecord{MachineID: integrationToolTestID("machine-first")},
	}
	second := executionstore.PoolMachineRecord{
		Binding: executionstore.AgentMachineBindingRecord{MachineID: integrationToolTestID("machine-second")},
	}
	tests := []struct {
		name     string
		machines []executionstore.PoolMachineRecord
		want     storage.ID
		wantErr  error
	}{
		{name: "zero machines", wantErr: ErrNoMachine},
		{name: "one machine", machines: []executionstore.PoolMachineRecord{first}, want: first.Binding.MachineID},
		{
			name:     "multiple machines",
			machines: []executionstore.PoolMachineRecord{first, second},
			wantErr:  ErrMachineSelectionRequired,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectOnlyMachine(tc.machines)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("select inspect machine: %v", err)
			}
			if got.Binding.MachineID != tc.want {
				t.Fatalf("selected machine_id = %q, want %q", got.Binding.MachineID, tc.want)
			}
		})
	}
}

func TestMachineObservationIncludesMachinePoolName(t *testing.T) {
	machineCwd := "/pool"
	record := executionstore.PoolMachineRecord{
		Binding: executionstore.AgentMachineBindingRecord{
			MachineID:   integrationToolTestID("machine-pool"),
			BindingKind: executionstore.MachineBindingKindPool,
		},
		Machine: executionstore.MachineRecord{
			ID:          integrationToolTestID("machine-pool"),
			SourceKind:  executionstore.MachineSourceKindPool,
			DisplayName: "Build machine",
			Cwd:         machineCwd,
		},
		MachinePoolName: "Build Pool",
	}
	got, err := machineObservation(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceKind != "pool" || got.BindingKind != "pool" || got.DisplayName != "Build machine" {
		t.Fatalf("pool observation identity = %+v", got)
	}
	if got.MachinePoolName != "Build Pool" {
		t.Fatalf("machine_pool_name = %q, want Build Pool", got.MachinePoolName)
	}
	if got.Cwd != machineCwd {
		t.Fatalf("cwd = %q, want %s", got.Cwd, machineCwd)
	}
}

func TestAgentMachineObservationIdentifiesBYOBinding(t *testing.T) {
	record := executionstore.AgentMachineObservationRecord{
		MachineID:       integrationToolTestID("machine-byo"),
		SourceKind:      executionstore.MachineSourceKindBYO,
		BindingKind:     executionstore.MachineBindingKindExplicit,
		BindingState:    executionstore.AgentMachineBindingStateAttached,
		DisplayName:     "Developer laptop",
		LifecycleState:  executionstore.MachineLifecycleStateActive,
		ConnectionState: executionstore.MachineConnectionStateOnline,
		Executable:      true,
	}
	got, err := agentMachineObservation(record)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceKind != "byo" || got.BindingKind != "explicit" || got.DisplayName != "Developer laptop" {
		t.Fatalf("BYO observation identity = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal BYO observation: %v", err)
	}
	if strings.Contains(string(encoded), "machine_pool_name") {
		t.Fatalf("BYO observation = %s, want machine_pool_name omitted", encoded)
	}
}

func TestAgentMachineObservationRedactsMachineWithoutProjectGrant(t *testing.T) {
	record := executionstore.AgentMachineObservationRecord{
		MachineID:              integrationToolTestID("machine-byo"),
		SourceKind:             executionstore.MachineSourceKindBYO,
		BindingKind:            executionstore.MachineBindingKindExplicit,
		BindingState:           executionstore.AgentMachineBindingStateAttached,
		DisplayName:            "Developer laptop",
		MachinePoolName:        "Private pool",
		LifecycleState:         executionstore.MachineLifecycleStateActive,
		ConnectionState:        executionstore.MachineConnectionStateOnline,
		ConnectionStateReason:  "connected",
		Description:            "developer machine",
		Cwd:                    "/workspace",
		ProjectGrantMissing:    true,
		LifecycleReasonCode:    "healthy",
		LifecycleReasonMessage: "ready",
		FailureReport:          json.RawMessage(`{"stage":"daemon_install"}`),
	}
	inspection, err := agentMachineInspection(record)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatalf("marshal ungranted machine observation: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode ungranted machine observation: %v", err)
	}
	if got["project_grant_missing"] != true || got["executable"] != false {
		t.Fatalf("ungranted machine observation = %s", encoded)
	}
	for _, field := range []string{
		"source_kind",
		"display_name",
		"machine_pool_name",
		"connection_state_reason",
		"failure_report",
	} {
		if _, ok := got[field]; ok {
			t.Fatalf("ungranted machine observation exposed %s: %s", field, encoded)
		}
	}
	for _, field := range []string{
		"lifecycle_state",
		"connection_state",
		"cwd",
		"lifecycle_reason_code",
		"lifecycle_reason_message",
	} {
		if got[field] != "" {
			t.Fatalf("ungranted machine observation exposed %s: %s", field, encoded)
		}
	}
}

func TestMachineInspectionIncludesFailureReport(t *testing.T) {
	failure := json.RawMessage(`{"stage":"daemon_install","exit_status":7,"output_tail":"failed"}`)
	record := executionstore.AgentMachineObservationRecord{
		MachineID:     integrationToolTestID("machine-pool"),
		FailureReport: failure,
	}
	inspection, err := agentMachineInspection(record)
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := json.Marshal(inspection)
	if err != nil {
		t.Fatalf("marshal machine inspection: %v", err)
	}
	if !strings.Contains(string(inspected), `"failure_report":`+string(failure)) {
		t.Fatalf("machine inspection = %s, want failure report", inspected)
	}
	observation, err := agentMachineObservation(record)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal machine observation: %v", err)
	}
	if strings.Contains(string(listed), "failure_report") {
		t.Fatalf("machine list observation = %s, want failure report omitted", listed)
	}
}

func machinePublicIDForTest(t *testing.T, id storage.ID) string {
	t.Helper()
	value, err := publicid.Encode(publicid.KindMachine, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestMachineToolsRequirePublicMachineIDs(t *testing.T) {
	id := integrationToolTestID("public-machine")
	publicID := machinePublicIDForTest(t, id)
	for _, name := range []string{
		"run_command", "inspect_machine", "delete_machine", "upload_artifact", "download_artifact",
	} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{
				publicID, "  " + publicID + "  ", id.String(), "mchr-abc234",
				"agt_" + strings.TrimPrefix(publicID, "mch_"),
			} {
				body := map[string]any{"machine_id": value}
				switch name {
				case "run_command":
					body["command"] = "pwd"
				case "upload_artifact", "download_artifact":
					body["path"] = "report.pdf"
					if name == "download_artifact" {
						body["artifact_id"] = "art_" + strings.TrimPrefix(publicID, "mch_")
					}
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				err = validateRegisteredToolInput(name, raw)
				wantValid := strings.TrimSpace(value) == publicID
				if (err == nil) != wantValid {
					t.Fatalf("machine_id %q: error = %v, want valid = %v", value, err, wantValid)
				}
				delete(body, "machine_id")
				body["machine_ref"] = value
				raw, err = json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateRegisteredToolInput(name, raw); err == nil {
					t.Fatal("legacy machine_ref was accepted")
				}
			}
		})
	}
}

func TestMachineSelectionUsesPublicIDAcrossBindings(t *testing.T) {
	machineID := integrationToolTestID("shared-machine")
	publicID := machinePublicIDForTest(t, machineID)
	for _, agent := range []string{"parent", "child", "reattached"} {
		binding := executionstore.AgentMachineBindingRecord{
			ID:        integrationToolTestID(agent + "-binding"),
			AgentID:   integrationToolTestID(agent),
			MachineID: machineID,
		}
		other := executionstore.AgentMachineBindingRecord{MachineID: integrationToolTestID("other-machine")}
		selected, err := selectMachineExecutionTarget([]executionstore.AgentMachineBindingRecord{other, binding}, publicID)
		if err != nil || selected.ID != binding.ID {
			t.Fatalf("%s selection = %+v, %v", agent, selected, err)
		}
		observation, err := agentMachineObservation(executionstore.AgentMachineObservationRecord{MachineID: machineID})
		if err != nil || observation.MachineID != publicID {
			t.Fatalf("%s observation = %+v, %v", agent, observation, err)
		}
		for _, unavailable := range []string{
			machineID.String(), "mchr-abc234",
			machinePublicIDForTest(t, integrationToolTestID("unattached-machine")),
		} {
			_, err := selectMachineExecutionTarget([]executionstore.AgentMachineBindingRecord{binding}, unavailable)
			if !errors.Is(err, ErrMachineIDUnavailable) {
				t.Fatalf("selection %q error = %v, want unavailable", unavailable, err)
			}
		}
	}
}
