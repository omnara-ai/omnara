package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestMachineToolInputValidation(t *testing.T) {
	valid := []model.ToolCall{
		{Name: "create_machine", Input: json.RawMessage(`{}`)},
		{Name: "create_machine", Input: json.RawMessage(`{"machine_pool_name":"Build Pool"}`)},
		{Name: "delete_machine", Input: json.RawMessage(`{"machine_ref":"mchr-123"}`)},
		{Name: "inspect_machine", Input: json.RawMessage(`{}`)},
		{Name: "inspect_machine", Input: json.RawMessage(`{"machine_ref":"mchr-123"}`)},
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
			name: "delete requires ref",
			call: model.ToolCall{Name: "delete_machine", Input: json.RawMessage(`{}`)},
		},
		{
			name: "inspect rejects null",
			call: model.ToolCall{Name: "inspect_machine", Input: json.RawMessage(`{"machine_ref":null}`)},
		},
		{
			name: "list rejects fields",
			call: model.ToolCall{Name: "list_machines", Input: json.RawMessage(`{"machine_ref":"mchr-123"}`)},
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
	first := executionstore.PoolMachineRecord{Binding: executionstore.AgentMachineBindingRecord{MachineRef: "mchr-first1"}}
	second := executionstore.PoolMachineRecord{
		Binding: executionstore.AgentMachineBindingRecord{MachineRef: "mchr-secon2"},
	}
	tests := []struct {
		name     string
		machines []executionstore.PoolMachineRecord
		want     string
		wantErr  error
	}{
		{name: "zero machines", wantErr: ErrNoMachine},
		{name: "one machine", machines: []executionstore.PoolMachineRecord{first}, want: first.Binding.MachineRef},
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
			if got.Binding.MachineRef != tc.want {
				t.Fatalf("selected machine_ref = %q, want %q", got.Binding.MachineRef, tc.want)
			}
		})
	}
}

func TestMachineObservationIncludesMachinePoolName(t *testing.T) {
	machineCwd := "/pool"
	record := executionstore.PoolMachineRecord{
		Binding:         executionstore.AgentMachineBindingRecord{MachineRef: "mchr-pool01"},
		Machine:         executionstore.MachineRecord{Cwd: machineCwd},
		MachinePoolName: "Build Pool",
	}
	got := machineObservation(record)
	if got.MachinePoolName != "Build Pool" {
		t.Fatalf("machine_pool_name = %q, want Build Pool", got.MachinePoolName)
	}
	if got.Cwd != machineCwd {
		t.Fatalf("cwd = %q, want %s", got.Cwd, machineCwd)
	}
}

func TestAgentMachineObservationIdentifiesBYOBinding(t *testing.T) {
	record := executionstore.AgentMachineObservationRecord{
		MachineRef:      "mchr-byo001",
		SourceKind:      executionstore.MachineSourceKindBYO,
		BindingKind:     executionstore.MachineBindingKindExplicit,
		BindingState:    executionstore.AgentMachineBindingStateAttached,
		DisplayName:     "Developer laptop",
		LifecycleState:  executionstore.MachineLifecycleStateActive,
		ConnectionState: executionstore.MachineConnectionStateOnline,
		Executable:      true,
	}
	got := agentMachineObservation(record)
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

func TestMachineInspectionIncludesFailureReport(t *testing.T) {
	failure := json.RawMessage(`{"stage":"daemon_install","exit_status":7,"output_tail":"failed"}`)
	record := executionstore.AgentMachineObservationRecord{
		MachineRef:    "mchr-pool01",
		FailureReport: failure,
	}
	inspected, err := json.Marshal(agentMachineInspection(record))
	if err != nil {
		t.Fatalf("marshal machine inspection: %v", err)
	}
	if !strings.Contains(string(inspected), `"failure_report":`+string(failure)) {
		t.Fatalf("machine inspection = %s, want failure report", inspected)
	}
	listed, err := json.Marshal(agentMachineObservation(record))
	if err != nil {
		t.Fatalf("marshal machine observation: %v", err)
	}
	if strings.Contains(string(listed), "failure_report") {
		t.Fatalf("machine list observation = %s, want failure report omitted", listed)
	}
}
