package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestMachineToolInputValidation(t *testing.T) {
	valid := []model.ToolCall{
		{Name: "create_machine", Input: json.RawMessage(`{}`)},
		{Name: "create_machine", Input: json.RawMessage(`{"machine_pool_id":"mpo_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		{Name: "create_machine", Input: json.RawMessage(`{"cpu":4,"memory_mb":8192}`)},
		{Name: "create_machine", Input: json.RawMessage(`{"memory_mb":1024}`)},
		{Name: "delete_machine", Input: json.RawMessage(`{"machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		{Name: "inspect_machine", Input: json.RawMessage(`{}`)},
		{Name: "inspect_machine", Input: json.RawMessage(`{"machine_id":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		{Name: "list_machines", Input: json.RawMessage(`{}`)},
		{Name: "list_machines", Input: json.RawMessage(`{"cursor":"mpo_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
		{Name: "list_machines", Input: json.RawMessage(`{"cursor":"mch_aaaaaaaaaaaaaaaaaaaaaaaaae"}`)},
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
			call: model.ToolCall{Name: "create_machine", Input: json.RawMessage(`{"machine_pool_id":null}`)},
		},
		{
			name: "create rejects extra",
			call: model.ToolCall{
				Name:  "create_machine",
				Input: json.RawMessage(`{"machine_pool_id":"mpo_aaaaaaaaaaaaaaaaaaaaaaaaae","count":2}`),
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
		{
			name: "list rejects nil cursor UUID",
			call: model.ToolCall{Name: "list_machines", Input: json.RawMessage(`{"cursor":"mch_aaaaaaaaaaaaaaaaaaaaaaaaaa"}`)},
		},
		{
			name: "list rejects noncanonical cursor",
			call: model.ToolCall{Name: "list_machines", Input: json.RawMessage(`{"cursor":"mch_aaaaaaaaaaaaaaaaaaaaaaaaab"}`)},
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

func TestCreateMachineRejectsInvalidSizes(t *testing.T) {
	for _, input := range []string{
		`{"cpu":null}`, `{"memory_mb":null}`, `{"cpu":0}`, `{"memory_mb":-1}`,
		`{"cpu":1.5}`, `{"memory_mb":"8192"}`, `{"cpu":2147483648}`, `{"memory_mb":2147483648}`,
	} {
		t.Run(input, func(t *testing.T) {
			require.Error(t, validateRegisteredToolInput("create_machine", json.RawMessage(input)))
		})
	}
}

func TestSelectCreateMachinePool(t *testing.T) {
	first := executionstore.MachinePoolSourceRecord{MachinePoolID: uuid.UUID{15: 1}, MachinePoolName: "Build Pool"}
	second := executionstore.MachinePoolSourceRecord{MachinePoolID: uuid.UUID{15: 2}, MachinePoolName: "Test Pool"}
	tests := []struct {
		name    string
		sources []executionstore.MachinePoolSourceRecord
		input   createMachineRequest
		want    uuid.UUID
		wantErr string
	}{
		{name: "omitted with zero sources", wantErr: "no machine pools are configured"},
		{
			name:    "omitted with one source",
			sources: []executionstore.MachinePoolSourceRecord{first},
			want:    first.MachinePoolID,
		},
		{
			name:    "omitted with multiple sources",
			sources: []executionstore.MachinePoolSourceRecord{first, second},
			wantErr: "machine_pool_id is required",
		},
		{
			name:    "explicit matching source",
			sources: []executionstore.MachinePoolSourceRecord{first, second},
			input:   createMachineRequest{MachinePoolID: poolPublicIDForTest(t, second.MachinePoolID)},
			want:    second.MachinePoolID,
		},
		{
			name:    "explicit missing source",
			sources: []executionstore.MachinePoolSourceRecord{first},
			input:   createMachineRequest{MachinePoolID: poolPublicIDForTest(t, second.MachinePoolID)},
			wantErr: "machine_pool_id is not available",
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
			if got.MachinePoolID != tc.want {
				t.Fatalf("selected pool = %q, want %q", got.MachinePoolID, tc.want)
			}
		})
	}
}

func TestSelectCreateMachinePoolValidatesOverrides(t *testing.T) {
	source := executionstore.MachinePoolSourceRecord{
		MachinePoolID: uuid.UUID{15: 1}, MachinePoolName: "Build Pool", SupportedOverrides: []string{"cpu", "memory_mb"},
		MinCPU: new(2), MaxCPU: new(4), MinMemoryMB: new(2048), MaxMemoryMB: new(8192),
	}
	for _, test := range []struct {
		name    string
		input   createMachineRequest
		wantErr string
	}{
		{name: "omitted"},
		{name: "minimum", input: createMachineRequest{CPU: new(2), MemoryMB: new(2048)}},
		{name: "maximum", input: createMachineRequest{CPU: new(4), MemoryMB: new(8192)}},
		{name: "cpu below minimum", input: createMachineRequest{CPU: new(1)}, wantErr: "cpu must be at least 2"},
		{name: "cpu above maximum", input: createMachineRequest{CPU: new(5)}, wantErr: "cpu must be at most 4"},
		{
			name: "memory below minimum", input: createMachineRequest{MemoryMB: new(1024)},
			wantErr: "memory_mb must be at least 2048",
		},
		{
			name: "memory above maximum", input: createMachineRequest{MemoryMB: new(16384)},
			wantErr: "memory_mb must be at most 8192",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, poolID := range []string{"", poolPublicIDForTest(t, source.MachinePoolID)} {
				input := test.input
				input.MachinePoolID = poolID
				_, err := selectPoolForMachineCreate([]executionstore.MachinePoolSourceRecord{source}, input)
				if test.wantErr != "" {
					require.ErrorContains(t, err, test.wantErr)
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
	for _, test := range []struct {
		input createMachineRequest
		field string
	}{
		{createMachineRequest{CPU: new(2)}, "cpu"},
		{createMachineRequest{MemoryMB: new(2048)}, "memory_mb"},
	} {
		t.Run("unsupported "+test.field, func(t *testing.T) {
			_, err := selectPoolForMachineCreate([]executionstore.MachinePoolSourceRecord{{}}, test.input)
			require.ErrorContains(t, err, "does not support "+test.field+" overrides")
		})
	}
}

func TestCreateMachineApprovalPinsPoolID(t *testing.T) {
	poolID := uuid.New()
	call := model.ToolCall{ID: "create", Name: "create_machine", Input: json.RawMessage(`{"machine_pool_id":"` + poolPublicIDForTest(t, poolID) + `"}`)}
	selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	descriptor, ok := toolpermission.FindMode(toolpermission.CommonModeDescriptors(), selection.Mode)
	require.True(t, ok)
	approvedInput, err := machineCreateAuthorizationInput(poolID, createMachineRequest{})
	require.NoError(t, err)
	request, err := permissionChallenge(
		call, permissionModeContext{descriptor: descriptor, selection: selection}, approvedInput,
	)
	require.NoError(t, err)
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)
	action := executionstore.AgentInteractionRecord{
		ProviderCallID: call.ID, InteractionKind: executionstore.AgentInteractionKindPermission, Request: requestJSON,
	}
	require.True(t, toolCallAuthorizationMatches(action, call, uuid.Nil, selection, approvedInput))
	otherPool, err := machineCreateAuthorizationInput(uuid.New(), createMachineRequest{})
	require.NoError(t, err)
	require.False(t, toolCallAuthorizationMatches(action, call, uuid.Nil, selection, otherPool))
	for _, input := range []createMachineRequest{{CPU: new(4)}, {MemoryMB: new(8192)}} {
		resized, err := machineCreateAuthorizationInput(poolID, input)
		require.NoError(t, err)
		require.False(t, toolCallAuthorizationMatches(action, call, uuid.Nil, selection, resized))
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
		want     uuid.UUID
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

func TestMachineObservationIncludesMachinePoolIdentity(t *testing.T) {
	machineCwd := "/pool"
	record := executionstore.PoolMachineRecord{
		Binding: executionstore.AgentMachineBindingRecord{
			MachineID:   integrationToolTestID("machine-pool"),
			BindingKind: executionstore.MachineBindingKindPool,
		},
		Machine: executionstore.MachineRecord{
			ID:            integrationToolTestID("machine-pool"),
			MachinePoolID: integrationToolTestID("pool"),
			SourceKind:    executionstore.MachineSourceKindPool,
			DisplayName:   "Build machine",
			Cwd:           machineCwd,
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
	if got.MachinePoolID != poolPublicIDForTest(t, record.Machine.MachinePoolID) || got.MachinePoolName != "Build Pool" {
		t.Fatalf("machine pool identity = (%q, %q), want (%q, %q)",
			got.MachinePoolID, got.MachinePoolName, poolPublicIDForTest(t, record.Machine.MachinePoolID), "Build Pool")
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
		MachinePoolID:          integrationToolTestID("private-pool"),
		CPU:                    new(4),
		MemoryMB:               new(8192),
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
		"machine_pool_id",
		"connection_state_reason",
		"failure_report",
		"cpu",
		"memory_mb",
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

func machinePublicIDForTest(t *testing.T, id uuid.UUID) string {
	t.Helper()
	value, err := publicid.Encode(publicid.KindMachine, id)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestMachineToolsValidatePublicMachineIDs(t *testing.T) {
	id := integrationToolTestID("public-machine")
	publicID := machinePublicIDForTest(t, id)
	for _, tc := range []struct{ name, input string }{
		{"run_command", `{"command":"pwd","%s":"%s"}`},
		{"inspect_machine", `{"%s":"%s"}`},
		{"delete_machine", `{"%s":"%s"}`},
		{"upload_file", `{"path":"/artifacts","source":"report.pdf","%s":"%s"}`},
		{"download_file", `{"path":"/artifacts/art_aaaaaaaaaaaaaaaaaaaaaaaaae","destination":"report.pdf","%s":"%s"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, value := range []string{
				publicID, "mch_invalid", "", "agt_aaaaaaaaaaaaaaaaaaaaaaaaae",
			} {
				input := json.RawMessage(fmt.Sprintf(tc.input, "machine_id", value))
				err := validateRegisteredToolInput(tc.name, input)
				if (err == nil) != (value == publicID) {
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

func TestMachineListPaginationAfterCursorResourceRemoval(t *testing.T) {
	for _, counts := range []struct{ pools, machines int }{{0, 150}, {150, 0}, {75, 75}} {
		t.Run(fmt.Sprintf("pools=%d/machines=%d", counts.pools, counts.machines), func(t *testing.T) {
			machines := make([]executionstore.AgentMachineObservationRecord, counts.machines)
			for i := range machines {
				machines[i] = executionstore.AgentMachineObservationRecord{
					MachineID: uuid.UUID{15: byte(len(machines) - i)}, Description: strings.Repeat("<é", 500),
				}
			}
			pools := make([]executionstore.MachinePoolSourceRecord, counts.pools)
			for i := range pools {
				pools[i] = executionstore.MachinePoolSourceRecord{
					MachinePoolID: uuid.UUID{15: byte(len(pools) - i)}, MachinePoolName: fmt.Sprint(i),
					Description: strings.Repeat("<é", 500),
				}
			}
			var seenPools, seenMachines []string
			cursor := ""
			for range counts.pools + counts.machines + 1 {
				page := machineListPageForTest(t, machines, pools, cursor)
				for _, pool := range page.MachinePools {
					require.Empty(t, seenMachines, "pools must precede machines across pages")
					seenPools = append(seenPools, pool.MachinePoolID)
					cursor = pool.MachinePoolID
				}
				for _, machine := range page.Machines {
					seenMachines = append(seenMachines, machine.MachineID)
					cursor = machine.MachineID
				}
				if page.NextCursor == "" {
					break
				}
				require.Equal(t, cursor, page.NextCursor)
				pools = slices.DeleteFunc(pools, func(pool executionstore.MachinePoolSourceRecord) bool {
					return poolPublicIDForTest(t, pool.MachinePoolID) == cursor
				})
				machines = slices.DeleteFunc(machines, func(machine executionstore.AgentMachineObservationRecord) bool {
					return machinePublicIDForTest(t, machine.MachineID) == cursor
				})
			}
			require.Len(t, seenPools, counts.pools)
			require.Len(t, seenMachines, counts.machines)
			for i, id := range seenPools {
				require.Equal(t, poolPublicIDForTest(t, uuid.UUID{15: byte(i + 1)}), id)
			}
			for i, id := range seenMachines {
				require.Equal(t, machinePublicIDForTest(t, uuid.UUID{15: byte(i + 1)}), id)
			}
			page := machineListPageForTest(t, machines, pools, cursor)
			require.Equal(t, machineListResult{
				Machines: []machineObservationPayload{}, MachinePools: []machinePoolPayload{},
			}, page)
		})
	}
}

func TestMachineListPageSizeBoundary(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			machines := []executionstore.AgentMachineObservationRecord{
				{MachineID: uuid.UUID{15: 1}}, {MachineID: uuid.UUID{15: 2}},
			}
			content, err := structuredToolResultContent(machineListResult{
				MachinePools: []machinePoolPayload{},
				Machines:     []machineObservationPayload{machineObservationForPaginationTest(t, machines[0])},
				NextCursor:   machinePublicIDForTest(t, machines[0].MachineID),
			})
			if err != nil {
				t.Fatal(err)
			}
			parts, err := content.contentParts()
			if err != nil {
				t.Fatal(err)
			}
			machines[0].Description = strings.Repeat("x", executionstore.ToolResultInlineBudgetBytes-len(parts)+delta)
			if delta <= 0 {
				page := machineListPageForTest(t, machines, nil, "")
				if len(page.Machines) != 1 || page.NextCursor != machinePublicIDForTest(t, machines[0].MachineID) {
					t.Fatalf("unexpected boundary page: machines=%d cursor=%s", len(page.Machines), page.NextCursor)
				}
			} else {
				result, err := machineListPage(machines, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				failure, ok := result.(failTransaction)
				if !ok {
					t.Fatalf("oversized entry returned %T", result)
				}
				parts, err := failure.content.contentParts()
				if err != nil || len(parts) > executionstore.ToolResultInlineBudgetBytes ||
					!strings.Contains(string(parts), "inspect_machine") ||
					!strings.Contains(string(parts), machinePublicIDForTest(t, machines[0].MachineID)) ||
					!strings.Contains(string(parts), "cursor") {
					t.Fatalf("missing bounded recovery instructions: %s, %v", parts, err)
				}
			}
			page := machineListPageForTest(t, machines, nil, machinePublicIDForTest(t, machines[0].MachineID))
			if len(page.Machines) != 1 || page.Machines[0].MachineID != machinePublicIDForTest(t, machines[1].MachineID) ||
				page.NextCursor != "" {
				t.Fatalf("continuation skipped remaining machine: %+v", page)
			}
		})
	}
}

func TestMachineListPageEncodedEntriesBoundary(t *testing.T) {
	pools := []executionstore.MachinePoolSourceRecord{{
		MachinePoolID: uuid.UUID{15: 1}, MachinePoolName: "Build", Description: "é<\"\\\n",
	}}
	for _, count := range []int{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			machines := []executionstore.AgentMachineObservationRecord{
				{MachineID: uuid.UUID{15: 1}, Description: "é<\"\\\n"},
				{MachineID: uuid.UUID{15: 2}, Description: "é<\"\\\n"},
				{MachineID: uuid.UUID{15: 3}},
			}
			machines = machines[:count]
			cursor := ""
			if count > 2 {
				cursor = machinePublicIDForTest(t, machines[1].MachineID)
			}
			content, err := structuredToolResultContent(machineListResult{
				MachinePools: []machinePoolPayload{{
					MachinePoolID:   poolPublicIDForTest(t, pools[0].MachinePoolID),
					MachinePoolName: pools[0].MachinePoolName, Description: pools[0].Description,
				}},
				Machines: []machineObservationPayload{
					machineObservationForPaginationTest(t, machines[0]), machineObservationForPaginationTest(t, machines[1]),
				},
				NextCursor: cursor,
			})
			if err != nil {
				t.Fatal(err)
			}
			parts, err := content.contentParts()
			if err != nil {
				t.Fatal(err)
			}
			machines[1].Description += strings.Repeat("x", executionstore.ToolResultInlineBudgetBytes-len(parts))
			page := machineListPageForTest(t, machines, pools, "")
			if len(page.Machines) != 2 || page.NextCursor != cursor {
				t.Fatalf("exact-size page: machines=%d cursor=%q", len(page.Machines), page.NextCursor)
			}
			machines[1].Description += "x"
			page = machineListPageForTest(t, machines, pools, "")
			if len(page.Machines) != 1 || page.NextCursor != machinePublicIDForTest(t, machines[0].MachineID) {
				t.Fatalf("oversized page: machines=%d cursor=%q", len(page.Machines), page.NextCursor)
			}
		})
	}
}

func machineListPageForTest(
	t *testing.T,
	machines []executionstore.AgentMachineObservationRecord,
	pools []executionstore.MachinePoolSourceRecord,
	cursor string,
) machineListResult {
	t.Helper()
	result, err := machineListPage(machines, pools, cursor)
	if err != nil {
		t.Fatal(err)
	}
	completed, ok := result.(completeTransaction)
	if !ok {
		t.Fatalf("page returned %T", result)
	}
	parts, err := completed.content.contentParts()
	if err != nil || len(parts) > executionstore.ToolResultInlineBudgetBytes {
		t.Fatalf("page size=%d, error=%v", len(parts), err)
	}
	var blocks []struct{ Value machineListResult }
	if err := json.Unmarshal(parts, &blocks); err != nil {
		t.Fatal(err)
	}
	return blocks[0].Value
}

func machineObservationForPaginationTest(
	t *testing.T,
	machine executionstore.AgentMachineObservationRecord,
) machineObservationPayload {
	t.Helper()
	observation, err := agentMachineObservation(machine)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func poolPublicIDForTest(t *testing.T, id uuid.UUID) string {
	t.Helper()
	value, err := publicid.Encode(publicid.KindMachinePool, id)
	require.NoError(t, err)
	return value
}

func TestCreateMachineRequiresPublicPoolID(t *testing.T) {
	for _, id := range []string{
		"", "Build", uuid.NewString(), "mpo_invalid", "mpo_aaaaaaaaaaaaaaaaaaaaaaaaaa", "mch_aaaaaaaaaaaaaaaaaaaaaaaaae",
	} {
		t.Run(id, func(t *testing.T) {
			require.Error(t, validateRegisteredToolInput("create_machine", json.RawMessage(`{"machine_pool_id":"`+id+`"}`)))
		})
	}
}

func TestMachineResultsExposeSizes(t *testing.T) {
	record := executionstore.PoolMachineRecord{
		Machine: executionstore.MachineRecord{ID: uuid.New(), CPU: new(4), MemoryMB: new(8192)},
	}
	created, err := machineProvisioningAcceptedResult(record)
	require.NoError(t, err)
	parts, err := created.contentParts()
	require.NoError(t, err)
	require.Contains(t, string(parts), `"cpu":4`)
	require.Contains(t, string(parts), `"memory_mb":8192`)
	deleted, err := machineDeletionAcceptedResult(record)
	require.NoError(t, err)
	parts, err = deleted.contentParts()
	require.NoError(t, err)
	require.NotContains(t, string(parts), `"cpu"`)
	require.NotContains(t, string(parts), `"memory_mb"`)
	for _, known := range []bool{false, true} {
		observation := executionstore.AgentMachineObservationRecord{MachineID: record.Machine.ID}
		if known {
			observation.CPU = record.Machine.CPU
			observation.MemoryMB = record.Machine.MemoryMB
		}
		listed, err := agentMachineObservation(observation)
		require.NoError(t, err)
		inspected, err := agentMachineInspection(observation)
		require.NoError(t, err)
		for _, payload := range []any{listed, inspected} {
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			if known {
				require.Contains(t, string(raw), `"cpu":4`)
				require.Contains(t, string(raw), `"memory_mb":8192`)
			} else {
				require.NotContains(t, string(raw), `"cpu"`)
				require.NotContains(t, string(raw), `"memory_mb"`)
			}
		}
	}
}
