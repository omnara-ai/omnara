package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func getToolCallTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	projectID, agentID, id uuid.UUID,
) (ToolCallRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return ToolCallRecord{}, errors.New("project, agent, and tool call id are required")
	}
	row, err := dbsqlc.New(tx).
		GetToolCall(ctx, dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ToolCallRecord{}, err
	}
	return toolCallRecordFromGetSQLC(row), nil
}

func (s *Store) GetToolCall(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (ToolCallRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return ToolCallRecord{}, errors.New("project, agent, and tool call id are required")
	}
	row, err := s.q.GetToolCall(
		ctx,
		dbsqlc.GetToolCallParams{ProjectID: projectID, AgentID: agentID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ToolCallRecord{}, err
	}
	return toolCallRecordFromGetSQLC(row), nil
}

func (s *Store) GetToolCallSummary(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (ToolCallSummary, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return ToolCallSummary{}, errors.New("project, agent, and tool call id are required")
	}
	row, err := s.q.GetToolCallSummary(ctx, dbsqlc.GetToolCallSummaryParams{
		ProjectID: projectID, AgentID: agentID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallSummary{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ToolCallSummary{}, fmt.Errorf("get tool call summary: %w", err)
	}
	return ToolCallSummary{
		ID: row.ID, AgentID: row.AgentID, TurnID: row.TurnID, ProviderCallID: row.ProviderCallID,
		Name: row.Name, Input: row.Input, Type: toolcatalog.Type(row.Type), State: ToolCallState(row.State),
		Outcome: ToolResultOutcome(row.Outcome), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt,
	}, nil
}

func (s *Store) GetToolCallStatus(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (ToolCallStatus, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
		return ToolCallStatus{}, errors.New("project, agent, and tool call id are required")
	}
	row, err := s.q.GetToolCallStatus(ctx, dbsqlc.GetToolCallStatusParams{
		ProjectID: projectID, AgentID: agentID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallStatus{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ToolCallStatus{}, fmt.Errorf("get tool call status: %w", err)
	}
	return ToolCallStatus{Type: toolcatalog.Type(row.Type), State: ToolCallState(row.State)}, nil
}

func (s *Store) GetCustomToolCallInput(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (CustomToolCallInput, bool, error) {
	row, err := s.q.GetCustomToolCallInput(
		ctx,
		dbsqlc.GetCustomToolCallInputParams{ProjectID: projectID, AgentID: agentID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CustomToolCallInput{}, false, nil
	}
	if err != nil {
		return CustomToolCallInput{}, false, fmt.Errorf("get custom tool call input: %w", err)
	}
	return CustomToolCallInput(row), true, nil
}

func (s *Store) ListToolCalls(
	ctx context.Context,
	input ListToolCallsInput,
) (ListToolCallsResult, error) {
	if input.ProjectID == uuid.Nil || len(input.AgentIDs) == 0 {
		return ListToolCallsResult{}, errors.New("project and at least one agent are required")
	}
	if input.Limit <= 0 {
		return ListToolCallsResult{}, errors.New("limit must be positive")
	}
	if err := validateToolCallFilters(input.State, input.Type); err != nil {
		return ListToolCallsResult{}, err
	}
	params := dbsqlc.ListToolCallsForAgentParams{
		ProjectID: input.ProjectID,
		AgentIds:  input.AgentIDs,
		State:     string(input.State),
		Type:      string(input.Type),
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
	calls := make([]ToolCallSummary, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, ToolCallSummary{
			ID: row.ID, AgentID: row.AgentID, TurnID: row.TurnID, ProviderCallID: row.ProviderCallID,
			Name: row.Name, Input: row.Input, Type: toolcatalog.Type(row.Type), State: ToolCallState(row.State),
			Outcome: ToolResultOutcome(row.Outcome), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt,
		})
	}
	return toolCallsPage(calls, input.Limit), nil
}

func (s *Store) ListUnfinishedProjectToolCalls(
	ctx context.Context,
	input ListUnfinishedProjectToolCallsInput,
) (ListToolCallsResult, error) {
	if input.ProjectID == uuid.Nil {
		return ListToolCallsResult{}, errors.New("project is required")
	}
	if input.Limit <= 0 {
		return ListToolCallsResult{}, errors.New("limit must be positive")
	}
	if err := validateToolCallFilters(input.State, input.Type); err != nil {
		return ListToolCallsResult{}, err
	}
	if input.State == ToolCallStateCompleted {
		return ListToolCallsResult{}, errors.New("completed calls are not unfinished work")
	}
	params := dbsqlc.ListUnfinishedToolCallsForProjectParams{
		ProjectID:      input.ProjectID,
		AgentProfileID: input.AgentProfileID,
		State:          string(input.State),
		Type:           string(input.Type),
		Names:          input.Names,
		RowLimit:       int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListUnfinishedToolCallsForProject(ctx, params)
	if err != nil {
		return ListToolCallsResult{}, fmt.Errorf("list unfinished project tool calls: %w", err)
	}
	calls := make([]ToolCallSummary, 0, len(rows))
	for _, row := range rows {
		calls = append(calls, ToolCallSummary{
			ID: row.ID, AgentID: row.AgentID, TurnID: row.TurnID, ProviderCallID: row.ProviderCallID,
			Name: row.Name, Input: row.Input, Type: toolcatalog.Type(row.Type), State: ToolCallState(row.State),
			Outcome: ToolResultOutcome(row.Outcome), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt,
		})
	}
	return toolCallsPage(calls, input.Limit), nil
}

func validateToolCallFilters(state ToolCallState, toolType toolcatalog.Type) error {
	if state != "" && !state.Valid() {
		return fmt.Errorf("invalid tool call state %q", state)
	}
	if toolType != "" && !toolType.Valid() {
		return fmt.Errorf("invalid tool call type %q", toolType)
	}
	return nil
}

func toolCallsPage(calls []ToolCallSummary, limit int) ListToolCallsResult {
	result := ListToolCallsResult{ToolCalls: calls, HasMore: len(calls) > limit}
	if result.HasMore {
		result.ToolCalls = calls[:limit]
	}
	return result
}

func (s *Store) NextRunnableToolCall(
	ctx context.Context,
	projectID, agentID, modelOutputID uuid.UUID,
	excludedToolCallIDs []uuid.UUID,
) (ToolCallRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || modelOutputID == uuid.Nil {
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
	projectID, agentID, modelCallContextID uuid.UUID,
	providerCallID string,
) (ToolCallRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || modelCallContextID == uuid.Nil ||
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
	projectID, agentID uuid.UUID,
	afterEventSequence, watermark int64,
) ([]ToolCallRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
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
