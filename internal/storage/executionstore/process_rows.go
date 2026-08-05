package executionstore

import (
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func processRecordFromSQLC(row dbsqlc.Process) ProcessRecord {
	return ProcessRecord{
		ID:                    row.ID,
		OrgID:                 row.OrgID,
		ProjectID:             row.ProjectID,
		AgentID:               row.AgentID,
		ToolCallID:            row.ToolCallID,
		RuntimeLockID:         row.RuntimeLockID,
		AgentMachineBindingID: row.AgentMachineBindingID,
		MachineID:             row.MachineID,
		ExecutionGrantedAt:    row.ExecutionGrantedAt,
		IOMode:                processcmd.IOMode(row.IoMode),
		Command:               row.Command,
		ShellSelector:         processcmd.ShellSelector(row.ShellSelector),
		Cwd:                   row.Cwd,
		TimeoutSeconds:        int(row.TimeoutSeconds),
		InitialWaitMS:         int(row.InitialWaitMs),
		DefaultOutputCursor:   row.DefaultOutputCursor,
		State:                 ProcessState(row.State),
		StateReasonCode:       stringFromSQLCText(row.StateReasonCode),
		StateReasonMessage:    row.StateReasonMessage,
		SourceStartedAt:       row.SourceStartedAt,
		SourceEndedAt:         row.SourceEndedAt,
		StateChangedAt:        row.StateChangedAt,
		ExitCode:              intPtrFromSQLC(row.ExitCode),
		ExitSignal:            row.ExitSignal,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}

func processRecordFromInsertSQLC(row dbsqlc.Process) ProcessRecord {
	return processRecordFromSQLC(row)
}

func processRecordFromStartedSQLC(row dbsqlc.Process) ProcessRecord {
	return processRecordFromSQLC(row)
}

func processRecordFromCompleteSQLC(row dbsqlc.Process) ProcessRecord {
	return processRecordFromSQLC(row)
}

func processRecordFromAcceptSQLC(row dbsqlc.AcceptDaemonProcessRow) ProcessRecord {
	return ProcessRecord{
		ID:                    row.ID,
		OrgID:                 row.OrgID,
		ProjectID:             row.ProjectID,
		AgentID:               row.AgentID,
		ToolCallID:            row.ToolCallID,
		RuntimeLockID:         row.RuntimeLockID,
		AgentMachineBindingID: row.AgentMachineBindingID,
		MachineID:             row.MachineID,
		ExecutionGrantedAt:    row.ExecutionGrantedAt,
		IOMode:                processcmd.IOMode(row.IoMode),
		Command:               row.Command,
		ShellSelector:         processcmd.ShellSelector(row.ShellSelector),
		Cwd:                   row.Cwd,
		TimeoutSeconds:        int(row.TimeoutSeconds),
		InitialWaitMS:         int(row.InitialWaitMs),
		DefaultOutputCursor:   row.DefaultOutputCursor,
		State:                 ProcessState(row.State),
		StateReasonCode:       stringFromSQLCText(row.StateReasonCode),
		StateReasonMessage:    row.StateReasonMessage,
		SourceStartedAt:       row.SourceStartedAt,
		SourceEndedAt:         row.SourceEndedAt,
		StateChangedAt:        row.StateChangedAt,
		ExitCode:              intPtrFromSQLC(row.ExitCode),
		ExitSignal:            row.ExitSignal,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}

func activeProcessRecordFromSQLC(row dbsqlc.ListActiveProcessesForContextRow) ActiveProcessRecord {
	return ActiveProcessRecord{
		ID:              row.ID,
		State:           ProcessState(row.State),
		MachineID:       row.MachineID,
		IOMode:          processcmd.IOMode(row.IoMode),
		Command:         row.Command,
		ShellSelector:   processcmd.ShellSelector(row.ShellSelector),
		Cwd:             row.Cwd,
		SourceStartedAt: row.SourceStartedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
		ToolCallID:      row.ToolCallID,
	}
}

func processActionRecordFromSQLC(row dbsqlc.ProcessAction) ProcessActionRecord {
	return ProcessActionRecord{
		ID:                 row.ID,
		OrgID:              row.OrgID,
		ProjectID:          row.ProjectID,
		AgentID:            row.AgentID,
		ProcessID:          row.ProcessID,
		ToolCallID:         row.ToolCallID,
		RuntimeLockID:      row.RuntimeLockID,
		ActionKind:         ProcessActionKind(row.ActionKind),
		Seq:                row.Seq,
		Payload:            row.Payload,
		State:              ProcessActionState(row.State),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		StateReasonCode:    stringFromSQLCText(row.StateReasonCode),
		StateReasonMessage: row.StateReasonMessage,
	}
}

func processActionRecordFromAcceptSQLC(
	row dbsqlc.AcceptDaemonProcessActionRow,
) ProcessActionRecord {
	return ProcessActionRecord{
		ID:                 row.ID,
		OrgID:              row.OrgID,
		ProjectID:          row.ProjectID,
		AgentID:            row.AgentID,
		ProcessID:          row.ProcessID,
		ToolCallID:         row.ToolCallID,
		RuntimeLockID:      row.RuntimeLockID,
		ActionKind:         ProcessActionKind(row.ActionKind),
		Seq:                row.Seq,
		Payload:            row.Payload,
		State:              ProcessActionState(row.State),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		StateReasonCode:    stringFromSQLCText(row.StateReasonCode),
		StateReasonMessage: row.StateReasonMessage,
	}
}
