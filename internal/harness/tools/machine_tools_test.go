package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
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
	if got.MachineID != machinePublicIDForTest(t, record.Machine.ID) ||
		got.SourceKind != "pool" || got.BindingKind != "pool" || got.DisplayName != "Build machine" {
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
	if got.MachineID != machinePublicIDForTest(t, record.MachineID) ||
		got.SourceKind != "byo" || got.BindingKind != "explicit" || got.DisplayName != "Developer laptop" {
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
	for _, tc := range []struct{ name, input string }{
		{"run_command", `{"command":"pwd","%s":"%s"}`},
		{"inspect_machine", `{"%s":"%s"}`},
		{"delete_machine", `{"%s":"%s"}`},
		{"upload_artifact", `{"path":"report.pdf","%s":"%s"}`},
		{"download_artifact", `{"artifact_id":"art_aaaaaaaaaaaaaaaaaaaaaaaaae","path":"report.pdf","%s":"%s"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, value := range []string{
				publicID, "  " + publicID + "  ", id.String(), "mchr-abc234", "agt_aaaaaaaaaaaaaaaaaaaaaaaaae",
			} {
				input := json.RawMessage(fmt.Sprintf(tc.input, "machine_id", value))
				err := validateRegisteredToolInput(tc.name, input)
				if (err == nil) != (strings.TrimSpace(value) == publicID) {
					t.Fatalf("machine_id %q: unexpected validation result: %v", value, err)
				}
			}
			input := json.RawMessage(fmt.Sprintf(tc.input, "machine_ref", "mchr-abc234"))
			if err := validateRegisteredToolInput(tc.name, input); err == nil {
				t.Fatal("legacy machine_ref was accepted")
			}
		})
	}
}

func TestInspectMachinePermissionUsesCanonicalMachineID(t *testing.T) {
	selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	descriptor, ok := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), selection.Mode)
	if !ok {
		t.Fatal("always_ask descriptor missing")
	}
	want := json.RawMessage(`{"mode":"inspect","machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)
	for _, machineID := range []string{
		"mch_aaaaaaaaaaaaaaaaaaaaaaaaae",
		"  mch_aaaaaaaaaaaaaaaaaaaaaaaaae  ",
		"mch_AAAAAAAAAAAAAAAAAAAAAAAAAE",
		"mch_aaaaaaaaaaaaaaaaaaaaaaaaaf",
	} {
		t.Run(machineID, func(t *testing.T) {
			call := model.ToolCall{
				Name:  "inspect_machine",
				Input: json.RawMessage(fmt.Sprintf(`{"machine_id":%q}`, machineID)),
			}
			request, err := inspectMachinePermissionChallenge(
				t.Context(), Executor{}, Turn{}, call,
				permissionModeContext{selection: selection, descriptor: descriptor},
			)
			if err != nil {
				t.Fatalf("prepare inspect permission: %v", err)
			}
			if !jsoncanonical.Equal(request.Authorization.Input, want) {
				t.Fatalf("inspection authorization = %s, want %s", request.Authorization.Input, want)
			}
		})
	}
}
