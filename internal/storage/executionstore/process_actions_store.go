package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (t *toolCallTransaction) createProcessAction(
	ctx context.Context,
	input CreateProcessActionInput,
) (ProcessActionRecord, error) {
	if isNilID(input.ProcessID) || input.ActionKind == "" {
		return ProcessActionRecord{}, errors.New("process and kind are required")
	}
	if !input.ActionKind.Valid() {
		return ProcessActionRecord{}, fmt.Errorf("unsupported process action kind %q", input.ActionKind)
	}
	projectID := t.input.ProjectID
	agentID := t.input.AgentID
	toolCallID := t.input.ToolCallID
	runtimeLockID := t.input.RuntimeLockID
	input.Payload = normalizedJSON(input.Payload)
	replay, replayErr := t.q.GetProcessActionByToolCall(
		ctx,
		dbsqlc.GetProcessActionByToolCallParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		},
	)
	if replayErr == nil {
		record := processActionRecordFromSQLC(replay)
		if record.ProcessID != input.ProcessID || record.ActionKind != input.ActionKind ||
			!sameJSON(record.Payload, input.Payload) ||
			record.ToolCallID != toolCallID {
			return ProcessActionRecord{}, storeerr.ErrIdempotencyConflict
		}
		t.hasDurableCompletionOwner = true
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return ProcessActionRecord{}, err
		}
		return record, nil
	}
	if !errors.Is(replayErr, pgx.ErrNoRows) {
		return ProcessActionRecord{}, fmt.Errorf("load process action replay: %w", replayErr)
	}
	processRow, err := t.q.GetProcess(
		ctx,
		dbsqlc.GetProcessParams{ProjectID: projectID, AgentID: agentID, ID: input.ProcessID},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProcessActionRecord{}, storeerr.ErrNotFound
		}
		return ProcessActionRecord{}, fmt.Errorf("load process for action machine lock: %w", err)
	}
	_, err = t.q.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: processRow.OrgID,
			ID:    processRow.MachineID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProcessActionRecord{}, t.processActionCreateBlocker(ctx, input)
		}
		return ProcessActionRecord{}, fmt.Errorf("lock machine for process action creation: %w", err)
	}
	if err := t.lockForMutation(ctx); err != nil {
		return ProcessActionRecord{}, err
	}
	if _, err := t.q.LockProcessForActionCreation(
		ctx,
		dbsqlc.LockProcessForActionCreationParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        input.ProcessID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProcessActionRecord{}, storeerr.ErrNotFound
		}
		return ProcessActionRecord{}, fmt.Errorf(
			"lock process for process action creation: %w",
			err,
		)
	}
	row, err := t.q.InsertProcessAction(
		ctx,
		dbsqlc.InsertProcessActionParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			ProcessID:     input.ProcessID,
			ToolCallID:    toolCallID,
			RuntimeLockID: runtimeLockID,
			ActionKind:    string(input.ActionKind),
			Payload:       input.Payload,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProcessActionRecord{}, t.processActionCreateBlocker(ctx, input)
		}
		return ProcessActionRecord{}, fmt.Errorf("create process action: %w", err)
	}
	record := processActionRecordFromSQLC(row)
	t.hasDurableCompletionOwner = true
	t.requiresWaitingDisposition = true
	t.notifications.AddDaemonWork(processRow.MachineID)
	return record, nil
}

func (t *toolCallTransaction) processActionCreateBlocker(
	ctx context.Context,
	input CreateProcessActionInput,
) error {
	row, err := t.q.GetProcessActionCreateBlocker(
		ctx,
		dbsqlc.GetProcessActionCreateBlockerParams{
			ProjectID: t.input.ProjectID,
			AgentID:   t.input.AgentID,
			ProcessID: input.ProcessID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("diagnose process action create blocker: %w", err)
	}
	state := ProcessState(row.State)
	if isProcessTerminal(state) {
		if input.ActionKind == ProcessActionKindRead && !row.HasOnlineRuntime {
			return storeerr.ErrNoOnlineDaemonRuntime
		}
		if state == ProcessStateUnknown &&
			(input.ActionKind == ProcessActionKindInterrupt ||
				input.ActionKind == ProcessActionKindTerminate) {
			return storeerr.ErrProcessStateUnknown
		}
		if input.ActionKind == ProcessActionKindTerminate {
			return storeerr.ErrProcessAlreadyStopped
		}
		return storeerr.ErrProcessTerminal
	}
	if state != ProcessStateStarting && state != ProcessStateRunning {
		return storeerr.ErrRuntimeLockInactive
	}
	if row.HasTerminateAction && input.ActionKind != ProcessActionKindRead {
		return storeerr.ErrProcessTerminating
	}
	if !row.HasOnlineRuntime {
		return storeerr.ErrNoOnlineDaemonRuntime
	}
	return storeerr.ErrRuntimeLockInactive
}

func (s *Store) ListDaemonProcessOffers(ctx context.Context, input DaemonWorkInput) ([]DaemonProcessOffer, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return nil, err
	}
	if input.Limit <= 0 {
		input.Limit = 1
	}
	rows, err := s.q.ListDaemonProcessOffers(
		ctx,
		dbsqlc.ListDaemonProcessOffersParams{
			OrgID:           input.Authority.OrgID,
			MachineID:       input.Authority.MachineID,
			DaemonRuntimeID: input.Authority.DaemonRuntimeID,
			DaemonTokenID:   input.Authority.DaemonTokenID,
			LimitCount:      input.Limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list daemon process offers: %w", err)
	}
	offers := make([]DaemonProcessOffer, 0, len(rows))
	for _, row := range rows {
		offer := DaemonProcessOffer{Process: processRecordFromSQLC(row)}
		env, err := s.ResolveEnvironmentSecrets(ctx, row.OrgID, row.ProjectID, row.Env, row.SecretEnv)
		if err != nil {
			if !errors.Is(err, storeerr.ErrPermanentEnvironment) {
				offer.RetryError = fmt.Errorf("resolve process environment: %w", err)
			} else {
				offer.PreparationError = "process environment could not be resolved"
			}
		} else {
			offer.Env = env
		}
		offers = append(offers, offer)
	}
	return offers, nil
}

func (s *Store) AcceptDaemonProcess(
	ctx context.Context,
	input AcceptDaemonProcessInput,
) (DaemonProcessOffer, bool, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonProcessOffer{}, false, err
	}
	if isNilID(input.ProcessID) {
		return DaemonProcessOffer{}, false, errors.New("process is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonProcessOffer{}, false, fmt.Errorf("begin accept daemon process: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	_, err = qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: input.Authority.OrgID,
			ID:    input.Authority.MachineID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return DaemonProcessOffer{}, false, fmt.Errorf(
					"commit missing machine for accept daemon process: %w",
					err,
				)
			}
			return DaemonProcessOffer{}, false, nil
		}
		return DaemonProcessOffer{}, false, fmt.Errorf("lock machine for accept daemon process: %w", err)
	}
	if _, err := qtx.LockDaemonProcessForAccept(
		ctx,
		dbsqlc.LockDaemonProcessForAcceptParams{
			OrgID:     input.Authority.OrgID,
			MachineID: input.Authority.MachineID,
			ProcessID: input.ProcessID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return DaemonProcessOffer{}, false, fmt.Errorf("commit missing process for daemon accept: %w", err)
			}
			return DaemonProcessOffer{}, false, nil
		}
		return DaemonProcessOffer{}, false, fmt.Errorf("lock process for daemon accept: %w", err)
	}
	row, err := qtx.AcceptDaemonProcess(
		ctx,
		dbsqlc.AcceptDaemonProcessParams{
			OrgID:           input.Authority.OrgID,
			MachineID:       input.Authority.MachineID,
			DaemonRuntimeID: input.Authority.DaemonRuntimeID,
			DaemonTokenID:   input.Authority.DaemonTokenID,
			ProcessID:       input.ProcessID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return DaemonProcessOffer{}, false, fmt.Errorf("commit accept daemon process miss: %w", err)
		}
		return DaemonProcessOffer{}, false, nil
	}
	if err != nil {
		return DaemonProcessOffer{}, false, fmt.Errorf("accept daemon process: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return DaemonProcessOffer{}, false, fmt.Errorf("commit accept daemon process: %w", err)
	}
	return DaemonProcessOffer{Process: processRecordFromAcceptSQLC(row)}, true, nil
}

func (s *Store) GetDaemonProcessForMachineReport(
	ctx context.Context,
	authority DaemonRuntimeAuthority,
	processID ID,
) (ProcessRecord, bool, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return ProcessRecord{}, false, err
	}
	row, err := s.q.GetDaemonProcessForMachineReport(
		ctx,
		dbsqlc.GetDaemonProcessForMachineReportParams{
			OrgID:           authority.OrgID,
			MachineID:       authority.MachineID,
			DaemonRuntimeID: authority.DaemonRuntimeID,
			DaemonTokenID:   authority.DaemonTokenID,
			ID:              processID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if authorityErr := checkReportableDaemonRuntimeAuthority(ctx, s.q, authority); authorityErr != nil {
			return ProcessRecord{}, false, authorityErr
		}
		return ProcessRecord{}, false, nil
	}
	if err != nil {
		return ProcessRecord{}, false, fmt.Errorf("get daemon process for machine report: %w", err)
	}
	return processRecordFromSQLC(row), true, nil
}

func (s *Store) ListDaemonProcessActionOffers(
	ctx context.Context,
	input DaemonWorkInput,
) ([]ProcessActionRecord, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return nil, err
	}
	if input.Limit <= 0 {
		input.Limit = 16
	}
	rows, err := s.q.ListDaemonProcessActionOffers(
		ctx,
		dbsqlc.ListDaemonProcessActionOffersParams{
			OrgID:           input.Authority.OrgID,
			MachineID:       input.Authority.MachineID,
			DaemonRuntimeID: input.Authority.DaemonRuntimeID,
			DaemonTokenID:   input.Authority.DaemonTokenID,
			LimitCount:      input.Limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list daemon process action offers: %w", err)
	}
	offers := make([]ProcessActionRecord, 0, len(rows))
	for _, row := range rows {
		offers = append(offers, processActionRecordFromSQLC(row))
	}
	return offers, nil
}

func (s *Store) AcceptDaemonProcessAction(
	ctx context.Context,
	input AcceptDaemonProcessActionInput,
) (DaemonProcessActionGrant, bool, error) {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return DaemonProcessActionGrant{}, false, err
	}
	if isNilID(input.ProcessID) || isNilID(input.ID) {
		return DaemonProcessActionGrant{}, false, errors.New("process and action are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DaemonProcessActionGrant{}, false, fmt.Errorf("begin accept daemon process action: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	_, err = qtx.LockMachineForRuntimeRegistration(
		ctx,
		dbsqlc.LockMachineForRuntimeRegistrationParams{
			OrgID: input.Authority.OrgID,
			ID:    input.Authority.MachineID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return DaemonProcessActionGrant{}, false, fmt.Errorf(
					"commit missing machine for accept daemon process action: %w",
					err,
				)
			}
			return DaemonProcessActionGrant{}, false, nil
		}
		return DaemonProcessActionGrant{}, false, fmt.Errorf("lock machine for accept daemon process action: %w", err)
	}
	if _, err := qtx.LockDaemonProcessForAccept(
		ctx,
		dbsqlc.LockDaemonProcessForAcceptParams{
			OrgID:     input.Authority.OrgID,
			MachineID: input.Authority.MachineID,
			ProcessID: input.ProcessID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return DaemonProcessActionGrant{}, false, fmt.Errorf(
					"commit missing process for daemon action accept: %w",
					err,
				)
			}
			return DaemonProcessActionGrant{}, false, nil
		}
		return DaemonProcessActionGrant{}, false, fmt.Errorf("lock process for daemon action accept: %w", err)
	}
	if _, err := qtx.LockDaemonProcessActionForAccept(
		ctx,
		dbsqlc.LockDaemonProcessActionForAcceptParams{
			OrgID:     input.Authority.OrgID,
			ProcessID: input.ProcessID,
			ID:        input.ID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return DaemonProcessActionGrant{}, false, fmt.Errorf(
					"commit missing process action for daemon accept: %w",
					err,
				)
			}
			return DaemonProcessActionGrant{}, false, nil
		}
		return DaemonProcessActionGrant{}, false, fmt.Errorf("lock process action for daemon accept: %w", err)
	}
	row, err := qtx.AcceptDaemonProcessAction(
		ctx,
		dbsqlc.AcceptDaemonProcessActionParams{
			ID:              input.ID,
			ProcessID:       input.ProcessID,
			OrgID:           input.Authority.OrgID,
			MachineID:       input.Authority.MachineID,
			DaemonRuntimeID: input.Authority.DaemonRuntimeID,
			DaemonTokenID:   input.Authority.DaemonTokenID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return DaemonProcessActionGrant{}, false, fmt.Errorf("commit accept daemon process action: %w", err)
		}
		return DaemonProcessActionGrant{}, false, nil
	}
	if err != nil {
		return DaemonProcessActionGrant{}, false, fmt.Errorf("accept daemon process action: %w", err)
	}
	grant := DaemonProcessActionGrant{
		Action:              processActionRecordFromAcceptSQLC(row),
		ProcessState:        ProcessState(row.ProcessState),
		DefaultOutputCursor: row.DefaultOutputCursor,
	}
	if err := tx.Commit(ctx); err != nil {
		return DaemonProcessActionGrant{}, false, fmt.Errorf("commit accept daemon process action: %w", err)
	}
	return grant, true, nil
}

func (s *Store) GetProcessActionForDaemonReport(
	ctx context.Context,
	authority DaemonRuntimeAuthority,
	processID, actionID ID,
) (ProcessActionRecord, bool, error) {
	if err := validateDaemonRuntimeAuthority(authority); err != nil {
		return ProcessActionRecord{}, false, err
	}
	row, err := s.q.GetDaemonProcessActionForMachineReport(
		ctx,
		dbsqlc.GetDaemonProcessActionForMachineReportParams{
			OrgID:           authority.OrgID,
			MachineID:       authority.MachineID,
			DaemonRuntimeID: authority.DaemonRuntimeID,
			DaemonTokenID:   authority.DaemonTokenID,
			ProcessID:       processID,
			ID:              actionID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if authorityErr := checkReportableDaemonRuntimeAuthority(ctx, s.q, authority); authorityErr != nil {
			return ProcessActionRecord{}, false, authorityErr
		}
		return ProcessActionRecord{}, false, nil
	}
	if err != nil {
		return ProcessActionRecord{}, false, fmt.Errorf("get process action for daemon report: %w", err)
	}
	return processActionRecordFromSQLC(row), true, nil
}

func (s *Store) GetProcessActionByToolCall(
	ctx context.Context,
	projectID, agentID, toolCallID ID,
) (ProcessActionRecord, bool, error) {
	row, err := s.q.GetProcessActionByToolCall(
		ctx,
		dbsqlc.GetProcessActionByToolCallParams{ProjectID: projectID, AgentID: agentID, ToolCallID: toolCallID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessActionRecord{}, false, nil
	}
	if err != nil {
		return ProcessActionRecord{}, false, fmt.Errorf("get process action by tool call: %w", err)
	}
	return processActionRecordFromSQLC(row), true, nil
}
