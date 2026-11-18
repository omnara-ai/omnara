package executionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	maxTurnTraceEvents = 5000
	// maxTurnTraceInputLinks bounds the links on a turn's root span.
	maxTurnTraceInputLinks = 50
)

func (s *Store) LoadTurnTrace(ctx context.Context, projectID, agentID, turnID uuid.UUID) (oteltrace.Turn, error) {
	if projectID == uuid.Nil {
		return oteltrace.Turn{}, errors.New("project is required")
	}
	return s.loadTurnTrace(ctx, &projectID, agentID, turnID)
}

func (s *Store) loadTurnTrace(
	ctx context.Context, requiredProjectID *uuid.UUID, agentID, turnID uuid.UUID,
) (oteltrace.Turn, error) {
	row, err := s.q.GetTurnTraceContext(ctx, dbsqlc.GetTurnTraceContextParams{
		ProjectID: requiredProjectID, AgentID: agentID, TurnID: turnID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return oteltrace.Turn{}, storeerr.ErrNotFound
	}
	if err != nil {
		return oteltrace.Turn{}, fmt.Errorf("load turn trace context: %w", err)
	}
	projectID := row.ProjectID
	turn := oteltrace.Turn{TurnID: row.TurnID, TurnSequence: row.TurnSequence, AgentName: row.AgentName}
	for _, id := range []struct {
		target *string
		kind   publicid.Kind
		value  uuid.UUID
	}{
		{&turn.OrgID, publicid.KindOrganization, row.OrgID},
		{&turn.ProjectID, publicid.KindProject, row.ProjectID},
		{&turn.AgentID, publicid.KindAgent, row.AgentID},
		{&turn.ParentAgentID, publicid.KindAgent, storeutil.IDFromPtr(row.ParentAgentID)},
		{&turn.RootAgentID, publicid.KindAgent, row.RootAgentID},
		{&turn.PublicTurnID, publicid.KindAgentTurn, row.TurnID},
	} {
		if id.value == uuid.Nil {
			continue
		}
		if *id.target, err = publicid.Encode(id.kind, id.value); err != nil {
			return oteltrace.Turn{}, err
		}
	}

	modelCalls, err := s.q.ListTurnTraceModelCalls(ctx, dbsqlc.ListTurnTraceModelCallsParams{
		AgentID: agentID, TurnID: turnID,
	})
	if err != nil {
		return oteltrace.Turn{}, fmt.Errorf("list turn trace model calls: %w", err)
	}
	turn.ModelCalls = make([]oteltrace.ModelCall, 0, len(modelCalls))
	for _, call := range modelCalls {
		turn.ModelCalls = append(turn.ModelCalls, oteltrace.ModelCall{
			ID: call.ID, Operation: call.OperationKind, Attempt: call.AttemptNumber, State: call.State,
			APIFormat: call.ApiFormat, RequestModel: call.ProviderModelSlug, ResponseModel: call.ServedProviderModelSlug,
			ResponseID: call.ProviderResponseID, StopReason: call.StopReason, CostUSD: call.ProviderReportedCostUsd,
			ErrorKind: call.ErrorKind, ErrorMessage: call.ErrorMessage,
			StartedAt: call.CreatedAt, CompletedAt: storeutil.TimeOrZero(call.CompletedAt),
			InputTokens: call.InputTokensTotal, CacheReadTokens: call.CacheReadInputTokens,
			CacheWriteTokens: call.CacheWriteInputTokens, OutputTokens: call.OutputTokensTotal,
			ReasoningTokens: call.ReasoningOutputTokens,
		})
	}

	toolCalls, err := s.q.ListTurnTraceToolCalls(ctx, dbsqlc.ListTurnTraceToolCallsParams{
		AgentID: agentID, TurnID: turnID,
	})
	if err != nil {
		return oteltrace.Turn{}, fmt.Errorf("list turn trace tool calls: %w", err)
	}
	turn.ToolCalls = make([]oteltrace.ToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		turn.ToolCalls = append(turn.ToolCalls, oteltrace.ToolCall{
			ID: call.ID, Name: call.Name, ProviderCallID: call.ProviderCallID, Input: call.Input,
			Outcome: call.Outcome, StartedAt: call.CreatedAt, CompletedAt: call.CompletedAt,
		})
	}

	if err := s.loadTurnTraceLinks(ctx, &turn, projectID, agentID, turnID); err != nil {
		return oteltrace.Turn{}, err
	}

	var pages [][]AgentEventReadRecord
	var beforeSequence int64
	for count := 0; count < maxTurnTraceEvents; {
		page, err := listTurnEventsForReadTx(
			ctx, s.q, projectID, agentID, turnID, beforeSequence, MaxAgentEventsReadPageLimit)
		if err != nil {
			return oteltrace.Turn{}, err
		}
		if len(page) == 0 {
			break
		}
		pages = append(pages, page)
		count += len(page)
		beforeSequence = page[0].Sequence
		if len(page) < int(MaxAgentEventsReadPageLimit) {
			break
		}
		if count >= maxTurnTraceEvents {
			older, err := listTurnEventsForReadTx(ctx, s.q, projectID, agentID, turnID, beforeSequence, 1)
			if err != nil {
				return oteltrace.Turn{}, err
			}
			turn.Truncated = len(older) > 0
		}
	}
	for i := len(pages) - 1; i >= 0; i-- {
		for _, event := range pages[i] {
			turn.Events = append(turn.Events, oteltrace.Event{
				Kind: events.Kind(event.EventKind), IsOpening: event.IsOpeningEvent,
				ModelCallID: event.ModelCallContextID, ToolCallID: event.ToolCallID,
				StopReason: event.ModelStopReason, ContentBlocks: event.ContentBlocks,
				CreatedAt: event.CreatedAt,
			})
		}
	}
	return turn, nil
}

// traceLinkSource reads where an input came from out of its idempotency key.
// Inputs a person or integration sent have no source in another trace.
func traceLinkSource(scope, key string) (kind string, id uuid.UUID, fromToolCall, ok bool) {
	var raw string
	switch {
	case scope == "content_input" && strings.HasPrefix(key, "launch:spawn:") && strings.HasSuffix(key, ":content-input"):
		kind, fromToolCall = oteltrace.LinkSpawnedBy, true
		raw = strings.TrimSuffix(strings.TrimPrefix(key, "launch:spawn:"), ":content-input")
	case scope == subagentMessageIdempotencyScope && strings.HasPrefix(key, "tool_call:"):
		kind, fromToolCall = oteltrace.LinkMessageFrom, true
		raw = strings.TrimPrefix(key, "tool_call:")
	case scope == subagentMessageIdempotencyScope && strings.HasPrefix(key, "model_output:"):
		kind = oteltrace.LinkResultFrom
		raw = strings.TrimPrefix(key, "model_output:")
	default:
		return "", uuid.Nil, false, false
	}
	id, err := uuid.Parse(raw)
	return kind, id, fromToolCall, err == nil
}

// loadTurnTraceLinks connects the turn to the other agents' turns it exchanged
// input with: on the turn for input it received, on its tool calls for input
// they sent. A source or recipient that no longer exists is left unlinked.
func (s *Store) loadTurnTraceLinks(
	ctx context.Context, turn *oteltrace.Turn, projectID, agentID, turnID uuid.UUID,
) error {
	sources, err := s.q.ListTurnTraceInputSources(ctx, dbsqlc.ListTurnTraceInputSourcesParams{
		AgentID: agentID, TurnID: turnID, RowLimit: maxTurnTraceInputLinks,
	})
	if err != nil {
		return fmt.Errorf("list turn trace input sources: %w", err)
	}
	for _, source := range sources {
		kind, id, fromToolCall, ok := traceLinkSource(
			storeutil.TextOrEmpty(source.IdempotencyScope), storeutil.TextOrEmpty(source.InputIdempotencyKey))
		if !ok {
			continue
		}
		link := oteltrace.Link{Kind: kind}
		var sourceAgentID uuid.UUID
		if fromToolCall {
			var row dbsqlc.GetTraceLinkToolCallRow
			row, err = s.q.GetTraceLinkToolCall(ctx, dbsqlc.GetTraceLinkToolCallParams{
				ProjectID: projectID, ToolCallID: id,
			})
			sourceAgentID, link.TurnID, link.ToolCallID = row.AgentID, row.TurnID, id
		} else {
			var row dbsqlc.GetTraceLinkModelOutputRow
			row, err = s.q.GetTraceLinkModelOutput(ctx, dbsqlc.GetTraceLinkModelOutputParams{
				ProjectID: projectID, ModelOutputID: id,
			})
			sourceAgentID, link.TurnID = row.AgentID, row.TurnID
		}
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("load turn trace link source: %w", err)
		}
		if link.AgentID, err = publicid.Encode(publicid.KindAgent, sourceAgentID); err != nil {
			return err
		}
		turn.Links = append(turn.Links, link)
	}

	sent, err := s.q.ListTurnTraceSentInputs(ctx, dbsqlc.ListTurnTraceSentInputsParams{AgentID: agentID, TurnID: turnID})
	if err != nil {
		return fmt.Errorf("list turn trace sent inputs: %w", err)
	}
	for _, row := range sent {
		link := oteltrace.Link{Kind: oteltrace.LinkMessageTo, TurnID: row.TurnID}
		if row.ToolName == "spawn_agent" {
			link.Kind = oteltrace.LinkSpawned
		}
		if link.AgentID, err = publicid.Encode(publicid.KindAgent, row.AgentID); err != nil {
			return err
		}
		for i := range turn.ToolCalls {
			if turn.ToolCalls[i].ID == row.ToolCallID {
				turn.ToolCalls[i].Links = append(turn.ToolCalls[i].Links, link)
			}
		}
	}
	return nil
}
