package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func getToolCallTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, id ID,
) (ToolCallRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return ToolCallRecord{}, errors.New("project, agent, and tool call id are required")
	}
	row, err := dbsqlc.New(tx).
		GetToolCall(ctx, dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: id})
	if err != nil {
		return ToolCallRecord{}, err
	}
	return toolCallRecordFromGetSQLC(row), nil
}

func (s *Store) GetToolCall(
	ctx context.Context,
	projectID, agentID, id ID,
) (ToolCallRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return ToolCallRecord{}, errors.New("project, agent, and tool call id are required")
	}
	row, err := s.q.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: id},
	)
	if err != nil {
		return ToolCallRecord{}, err
	}
	return toolCallRecordFromGetSQLC(row), nil
}

func (s *Store) ListToolCalls(
	ctx context.Context,
	input ListToolCallsInput,
) (ListToolCallsResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) {
		return ListToolCallsResult{}, errors.New("project and agent are required")
	}
	if input.Limit <= 0 {
		return ListToolCallsResult{}, errors.New("limit must be positive")
	}
	switch input.State {
	case "",
		ToolCallStateAwaitingAuthorization,
		ToolCallStateAwaitingPermission,
		ToolCallStateReady,
		ToolCallStateRunning,
		ToolCallStateWaiting,
		ToolCallStateCompleted:
	default:
		return ListToolCallsResult{}, fmt.Errorf(
			"invalid tool call state %q",
			input.State,
		)
	}
	switch input.Type {
	case "",
		toolcatalog.ToolTypeBuiltIn,
		toolcatalog.ToolTypeCustom,
		toolcatalog.ToolTypeMCP:
	default:
		return ListToolCallsResult{}, fmt.Errorf("invalid tool call type %q", input.Type)
	}
	params := dbsqlc.ListToolCallsForAgentParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		State:     string(input.State),
		Type:      input.Type,
		RowLimit:  int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListToolCallsForAgent(ctx, params)
	if err != nil {
		return ListToolCallsResult{}, fmt.Errorf("list tool calls: %w", err)
	}
	result := ListToolCallsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.ToolCalls = make([]ToolCallRecord, 0, len(rows))
	for _, row := range rows {
		result.ToolCalls = append(result.ToolCalls, toolCallRecordFromAgentListSQLC(row))
	}
	return result, nil
}

func (s *Store) NextRunnableToolCall(
	ctx context.Context,
	projectID, agentID, modelOutputID ID,
	excludedToolCallIDs []ID,
) (ToolCallRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(modelOutputID) {
		return ToolCallRecord{}, false, errors.New(
			"project, agent, and model output ids are required",
		)
	}
	row, err := s.q.NextRunnableToolCallForModelOutput(
		ctx,
		dbsqlc.NextRunnableToolCallForModelOutputParams{
			ProjectID:           projectID,
			AgentID:             agentID,
			ModelOutputID:       modelOutputID,
			ExcludedToolCallIds: excludedToolCallIDs,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallRecord{}, false, nil
	}
	if err != nil {
		return ToolCallRecord{}, false, fmt.Errorf(
			"load next runnable tool call: %w",
			err,
		)
	}
	return toolCallRecordFromRunnableSQLC(row), true, nil
}

func (s *Store) GetToolCallByProviderCall(
	ctx context.Context,
	projectID, agentID, modelCallContextID ID,
	providerCallID string,
) (ToolCallRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(modelCallContextID) ||
		providerCallID == "" {
		return ToolCallRecord{}, false, errors.New(
			"project, agent, model context, and provider call id are required",
		)
	}
	row, err := s.q.GetToolCallByProviderCall(
		ctx,
		dbsqlc.GetToolCallByProviderCallParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ModelCallContextID: modelCallContextID,
			ProviderCallID:     providerCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallRecord{}, false, nil
	}
	if err != nil {
		return ToolCallRecord{}, false, err
	}
	return toolCallRecordFromProviderSQLC(row), true, nil
}

func (s *Store) ListCompletedToolCallsAtWatermark(
	ctx context.Context,
	projectID, agentID ID,
	afterEventSequence, watermark int64,
) ([]ToolCallRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	if afterEventSequence < 0 || watermark < afterEventSequence {
		return nil, errors.New("watermark must be at or after the non-negative event cursor")
	}
	if watermark == afterEventSequence {
		return []ToolCallRecord{}, nil
	}
	rows, err := s.q.ListCompletedToolCallsAtWatermark(
		ctx,
		dbsqlc.ListCompletedToolCallsAtWatermarkParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			AfterEventSequence: afterEventSequence,
			MaxEventSequence:   watermark,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list completed tool calls at watermark: %w", err)
	}
	out := make([]ToolCallRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, toolCallRecordFromWatermarkSQLC(row))
	}
	return out, nil
}
