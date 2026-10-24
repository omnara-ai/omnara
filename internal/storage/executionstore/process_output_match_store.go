package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

const (
	maxOutputMatchLines     = 20
	maxOutputMatchLineBytes = 4 * 1024
)

type NotifyProcessOutputMatchInput struct {
	Authority DaemonRuntimeAuthority
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	ID        uuid.UUID
	Sequence  int64
	Result    json.RawMessage
}

// NotifyProcessOutputMatch applies a daemon's process_output_matched report by
// sending the agent the matched lines. Repeated reports send one message.
func (s *Store) NotifyProcessOutputMatch(ctx context.Context, input NotifyProcessOutputMatchInput) error {
	if err := validateDaemonRuntimeAuthority(input.Authority); err != nil {
		return err
	}
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ID == uuid.Nil || input.Sequence <= 0 {
		return errors.New("project, agent, process, and a positive sequence are required")
	}
	matched, err := DecodeOutputMatchResult(input.Result)
	if err != nil {
		return err
	}
	_, err = agentexecution.Retry(ctx, "notify_process_output_match", func(ctx context.Context) (struct{}, error) {
		unit, err := s.cell.Begin(ctx)
		if err != nil {
			return struct{}{}, err
		}
		defer func() { _ = unit.Rollback(ctx) }()
		if err := requireReportableDaemonRuntimeAuthorityTx(ctx, dbsqlc.New(unit.DB()), input.Authority); err != nil {
			return struct{}{}, err
		}
		if _, err := lockExecution(ctx, unit, input.ProjectID, input.AgentID, false, uuid.Nil); err != nil {
			return struct{}{}, fmt.Errorf("lock agent for process output match: %w", err)
		}
		h, err := unit.Handle(input.ProjectID, input.AgentID)
		if err != nil {
			return struct{}{}, err
		}
		if err := h.NotifyProcessOutputMatch(ctx, input.ID, input.Sequence, matched); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, unit.Commit(ctx, "notify process output match")
	})
	return err
}

// DecodeOutputMatchResult decodes and bounds a process_output_matched result.
func DecodeOutputMatchResult(raw json.RawMessage) (daemonprotocol.OutputMatchResult, error) {
	var matched daemonprotocol.OutputMatchResult
	if err := json.Unmarshal(raw, &matched); err != nil {
		return daemonprotocol.OutputMatchResult{}, fmt.Errorf("decode output match result: %w", err)
	}
	if len(matched.Lines) == 0 && matched.Dropped == 0 {
		return daemonprotocol.OutputMatchResult{}, errors.New("output match result has no matches")
	}
	if len(matched.Lines) > maxOutputMatchLines || matched.Dropped < 0 {
		return daemonprotocol.OutputMatchResult{}, errors.New("output match result exceeds its line limit")
	}
	for _, line := range matched.Lines {
		if len(line) > maxOutputMatchLineBytes {
			return daemonprotocol.OutputMatchResult{}, errors.New("output match line exceeds its size limit")
		}
	}
	return matched, nil
}
