package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	maxTurnTraceEvents = 5000
	// maxTurnTraceCalls bounds the model calls and the tool calls a trace
	// reads. A turn with more keeps its most recent ones and is marked truncated.
	maxTurnTraceCalls = 2000
	// maxTurnTraceInputLinks bounds the links on a turn's root span.
	maxTurnTraceInputLinks = 50
	// maxTurnTraceSentLinks bounds cross-agent sent links loaded for retained calls.
	maxTurnTraceSentLinks = 500
	// Pre-allocation content read budgets enforced in SQL before rows leave
	// Postgres, preventing large tool inputs or results from blowing up worker
	// memory when multiple deliveries are in flight.
	maxTraceReadValueBytes = 256 * 1024
	// maxTraceReadValueChars ensures UTF-8 left(text, n) stays within 256 KiB even for 4-byte runes.
	maxTraceReadValueChars = maxTraceReadValueBytes / 4
	maxTraceReadEventBytes = 512 * 1024
	maxTraceReadTotalBytes = 8 * 1024 * 1024
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
		AgentID: agentID, TurnID: turnID, RowLimit: maxTurnTraceCalls + 1,
	})
	if err != nil {
		return oteltrace.Turn{}, fmt.Errorf("list turn trace model calls: %w", err)
	}
	if len(modelCalls) > maxTurnTraceCalls {
		modelCalls, turn.Truncated = modelCalls[:maxTurnTraceCalls], true
	}
	slices.Reverse(modelCalls)
	turn.ModelCalls = make([]oteltrace.ModelCall, 0, len(modelCalls))
	for _, call := range modelCalls {
		turn.ModelCalls = append(turn.ModelCalls, oteltrace.ModelCall{
			ID: call.ID, Operation: call.OperationKind, Attempt: call.AttemptNumber, State: call.State,
			APIFormat: call.ApiFormat, RequestModel: call.ProviderModelSlug, ResponseModel: call.ServedProviderModelSlug,
			ResponseID: call.ProviderResponseID, StopReason: call.StopReason, CostUSD: call.ProviderReportedCostUsd,
			ErrorKind: call.ErrorKind, ErrorMessage: call.ErrorMessage,
			StartedAt: call.CreatedAt, CompletedAt: storeutil.TimeOrZero(call.CompletedAt),
			InputTokens: call.InputTokensTotal, OutputTokens: call.OutputTokensTotal,
			CacheReadTokens:  tokenCount(call.CacheReadInputTokens),
			CacheWriteTokens: tokenCount(call.CacheWriteInputTokens),
			ReasoningTokens:  tokenCount(call.ReasoningOutputTokens),
		})
	}

	toolCalls, err := s.q.ListTurnTraceToolCalls(ctx, dbsqlc.ListTurnTraceToolCallsParams{
		AgentID: agentID, TurnID: turnID, RowLimit: maxTurnTraceCalls + 1,
		MaxValueBytes: maxTraceReadValueBytes, MaxValueChars: maxTraceReadValueChars,
		MaxTotalBytes: maxTraceReadTotalBytes,
	})
	if err != nil {
		return oteltrace.Turn{}, fmt.Errorf("list turn trace tool calls: %w", err)
	}
	if len(toolCalls) > maxTurnTraceCalls {
		toolCalls, turn.Truncated = toolCalls[:maxTurnTraceCalls], true
	}
	slices.Reverse(toolCalls)
	turn.ToolCalls = make([]oteltrace.ToolCall, 0, len(toolCalls))
	for _, call := range toolCalls {
		if call.InputTruncated {
			turn.ContentTruncated = true
		}
		turn.ToolCalls = append(turn.ToolCalls, oteltrace.ToolCall{
			ID: call.ID, Name: call.Name, ProviderCallID: call.ProviderCallID, Input: json.RawMessage(call.InputText),
			Outcome: call.Outcome, StartedAt: call.CreatedAt, CompletedAt: call.CompletedAt,
		})
	}

	linkedAgentIDs, err := s.loadTurnTraceLinks(ctx, &turn, projectID, agentID, turnID)
	if err != nil {
		return oteltrace.Turn{}, err
	}
	if err := s.loadTurnTraceParents(ctx, &turn, append(linkedAgentIDs, agentID)); err != nil {
		return oteltrace.Turn{}, err
	}

	traceEvents, err := s.q.ListTurnTraceEvents(ctx, dbsqlc.ListTurnTraceEventsParams{
		ProjectID: projectID, AgentID: agentID, TurnID: turnID, RowLimit: maxTurnTraceEvents + 1,
		MaxEventBytes: maxTraceReadEventBytes, MaxValueChars: maxTraceReadValueChars,
		MaxTotalBytes: maxTraceReadTotalBytes,
	})
	if err != nil {
		return oteltrace.Turn{}, fmt.Errorf("list turn trace events: %w", err)
	}
	if len(traceEvents) > maxTurnTraceEvents {
		traceEvents, turn.Truncated = traceEvents[:maxTurnTraceEvents], true
	}
	slices.Reverse(traceEvents)
	turn.Events = make([]oteltrace.Event, 0, len(traceEvents))
	for _, event := range traceEvents {
		if event.ContentTruncated {
			turn.ContentTruncated = true
		}
		turn.Events = append(turn.Events, oteltrace.Event{
			Kind: events.Kind(event.EventKind), IsOpening: event.IsOpeningEvent,
			ModelCallID:   storeutil.IDFromPtr(event.ModelCallContextID),
			ToolCallID:    storeutil.IDFromPtr(event.ToolCallID),
			StopReason:    modelenvelope.StopReason(storeutil.TextOrEmpty(event.ModelStopReason)),
			ContentBlocks: event.ContentBlocks,
			CreatedAt:     event.CreatedAt,
		})
	}
	return turn, nil
}

func tokenCount(count *int32) *int64 {
	if count == nil {
		return nil
	}
	return new(int64(*count))
}

// loadTurnTraceLinks connects the turn to the other agents' turns it exchanged
// input with: on the turn for input it received, on its tool calls for input
// they sent. Links come only from explicit source provenance columns recorded
// by the sending operation. A source or recipient that no longer exists is left unlinked.
// It returns the agents the linked turns belong to.
func (s *Store) loadTurnTraceLinks(
	ctx context.Context, turn *oteltrace.Turn, projectID, agentID, turnID uuid.UUID,
) (linkedAgentIDs []uuid.UUID, err error) {
	sources, err := s.q.ListTurnTraceInputSources(ctx, dbsqlc.ListTurnTraceInputSourcesParams{
		AgentID: agentID, TurnID: turnID, RowLimit: maxTurnTraceInputLinks,
	})
	if err != nil {
		return nil, fmt.Errorf("list turn trace input sources: %w", err)
	}
	for _, source := range sources {
		kind := storeutil.TextOrEmpty(source.SourceLinkKind)
		link := oteltrace.Link{Kind: kind}
		var sourceAgentID uuid.UUID
		switch {
		case source.SourceToolCallID != nil:
			var row dbsqlc.GetTraceLinkToolCallRow
			row, err = s.q.GetTraceLinkToolCall(ctx, dbsqlc.GetTraceLinkToolCallParams{
				ProjectID: projectID, ToolCallID: *source.SourceToolCallID,
			})
			sourceAgentID, link.TurnID, link.ToolCallID = row.AgentID, row.TurnID, *source.SourceToolCallID
		case source.SourceModelOutputID != nil:
			var row dbsqlc.GetTraceLinkModelOutputRow
			row, err = s.q.GetTraceLinkModelOutput(ctx, dbsqlc.GetTraceLinkModelOutputParams{
				ProjectID: projectID, ModelOutputID: *source.SourceModelOutputID,
			})
			sourceAgentID, link.TurnID = row.AgentID, row.TurnID
		default:
			continue
		}
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load turn trace link source: %w", err)
		}
		if link.AgentID, err = publicid.Encode(publicid.KindAgent, sourceAgentID); err != nil {
			return nil, err
		}
		if link.PublicTurnID, err = publicid.Encode(publicid.KindAgentTurn, link.TurnID); err != nil {
			return nil, err
		}
		turn.Links = append(turn.Links, link)
		linkedAgentIDs = append(linkedAgentIDs, sourceAgentID)
	}

	var sendingToolCallIDs []uuid.UUID
	for _, call := range turn.ToolCalls {
		if call.Name == "spawn_agent" || call.Name == "send_agent_message" {
			sendingToolCallIDs = append(sendingToolCallIDs, call.ID)
		}
	}
	if len(sendingToolCallIDs) == 0 {
		return linkedAgentIDs, nil
	}
	sent, err := s.q.ListTurnTraceSentInputs(ctx, dbsqlc.ListTurnTraceSentInputsParams{
		AgentID: &agentID, ToolCallIds: sendingToolCallIDs, RowLimit: maxTurnTraceSentLinks,
	})
	if err != nil {
		return nil, fmt.Errorf("list turn trace sent inputs: %w", err)
	}
	for _, row := range sent {
		if row.ToolCallID == nil {
			continue
		}
		link := oteltrace.Link{Kind: oteltrace.LinkMessageTo, TurnID: row.TurnID}
		if storeutil.TextOrEmpty(row.SourceLinkKind) == oteltrace.LinkSpawnedBy {
			link.Kind = oteltrace.LinkSpawned
		}
		if link.AgentID, err = publicid.Encode(publicid.KindAgent, row.AgentID); err != nil {
			return nil, err
		}
		if link.PublicTurnID, err = publicid.Encode(publicid.KindAgentTurn, row.TurnID); err != nil {
			return nil, err
		}
		linkedAgentIDs = append(linkedAgentIDs, row.AgentID)
		for i := range turn.ToolCalls {
			if turn.ToolCalls[i].ID == *row.ToolCallID {
				turn.ToolCalls[i].Links = append(turn.ToolCalls[i].Links, link)
			}
		}
	}
	return linkedAgentIDs, nil
}

// loadTurnTraceParents gives the turn, and each link to a turn, the caller's
// trace it joined, so the turn's spans and the links to them agree on a trace ID.
func (s *Store) loadTurnTraceParents(ctx context.Context, turn *oteltrace.Turn, agentIDs []uuid.UUID) error {
	turnIDs := []uuid.UUID{turn.TurnID}
	links := make([]*oteltrace.Link, 0, len(turn.Links))
	for i := range turn.Links {
		links = append(links, &turn.Links[i])
	}
	for i := range turn.ToolCalls {
		for j := range turn.ToolCalls[i].Links {
			links = append(links, &turn.ToolCalls[i].Links[j])
		}
	}
	for _, link := range links {
		turnIDs = append(turnIDs, link.TurnID)
	}
	rows, err := s.q.ListTurnTraceParents(ctx, dbsqlc.ListTurnTraceParentsParams{AgentIds: agentIDs, TurnIds: turnIDs})
	if err != nil {
		return fmt.Errorf("list turn trace parents: %w", err)
	}
	for _, row := range rows {
		parent, ok := oteltrace.ParseRemoteParent(row.TraceParent, row.TraceState)
		if !ok {
			continue
		}
		if row.TurnID == turn.TurnID {
			turn.Parent = parent
		}
		for _, link := range links {
			if link.TurnID == row.TurnID {
				link.TraceID = parent.TraceID
			}
		}
	}
	return nil
}
