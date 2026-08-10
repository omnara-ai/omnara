package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
)

func (e AgentExecutor) streamSinkForCall(
	ctx context.Context,
	agentID, turnID, modelCallContextID storage.ID,
) *harnessStreamSink {
	if e.StreamPublisher == nil {
		return nil
	}
	publicID, err := publicid.Encode(publicid.KindModelCallContext, modelCallContextID)
	if err != nil {
		if e.StreamLog != nil {
			e.StreamLog.Warn(
				"skip stream sink: encode public id failed",
				"model_call_context_id", modelCallContextID,
				"error", err,
			)
		}
		return nil
	}
	publicTurnID, err := publicid.Encode(publicid.KindAgentTurn, turnID)
	if err != nil {
		if e.StreamLog != nil {
			e.StreamLog.Warn(
				"skip stream sink: encode public turn id failed",
				"turn_id", turnID,
				"error", err,
			)
		}
		return nil
	}
	sink := &harnessStreamSink{
		publisher:          e.StreamPublisher,
		log:                e.StreamLog,
		agentID:            agentID,
		turnID:             publicTurnID,
		modelCallContextID: publicID,
		toolCalls:          make(map[string]streamToolCall),
		events:             make(chan streamEnvelope, streamSinkBufferSize),
		done:               make(chan struct{}),
	}
	go sink.run(ctx)
	return sink
}

const (
	streamSinkBufferSize     = 256
	streamSinkPublishTimeout = 250 * time.Millisecond
	streamSinkCloseTimeout   = 2 * time.Second
)

type harnessStreamSink struct {
	publisher          notifications.AgentStreamDeltaPublisher
	log                *slog.Logger
	agentID            uuid.UUID
	turnID             string
	modelCallContextID string
	frameSeq           atomic.Uint64
	sourceSeq          atomic.Uint64
	toolCallMu         sync.Mutex
	toolCalls          map[string]streamToolCall
	eventsMu           sync.RWMutex
	events             chan streamEnvelope
	done               chan struct{}
	closed             bool
}

type streamToolCall struct {
	id       storage.ID
	publicID string
}

type streamEnvelope struct {
	TurnID             string            `json:"turn_id"`
	ModelCallContextID string            `json:"model_call_context_id"`
	Seq                uint64            `json:"seq"`
	SourceSeqStart     uint64            `json:"source_seq_start"`
	SourceSeqEnd       uint64            `json:"source_seq_end"`
	CoalescedCount     uint64            `json:"coalesced_count"`
	Event              model.StreamEvent `json:"event"`
}

func (s *harnessStreamSink) Emit(ctx context.Context, event model.StreamEvent) {
	_ = ctx
	if s == nil || s.publisher == nil {
		return
	}
	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()
	if s.closed {
		return
	}
	if event.Kind == model.StreamEventBlockStart {
		if event.Block == nil {
			return
		}
		switch event.Block.Kind {
		case model.StreamBlockText, model.StreamBlockThinking:
			event.Block = &model.StreamBlock{Kind: event.Block.Kind}
		case model.StreamBlockToolUse:
			if event.Block.ToolCallID == "" || event.Block.ToolName == "" {
				return
			}
			publicID, ok := s.mintToolCallID(event.Block.ToolCallID)
			if !ok {
				return
			}
			block := *event.Block
			block.ToolCallID = publicID
			event.Block = &block
		default:
			return
		}
	}
	sourceSeq := s.sourceSeq.Add(1)
	envelope := streamEnvelope{
		TurnID:             s.turnID,
		ModelCallContextID: s.modelCallContextID,
		SourceSeqStart:     sourceSeq,
		SourceSeqEnd:       sourceSeq,
		CoalescedCount:     1,
		Event:              event,
	}
	select {
	case s.events <- envelope:
	default:
		if s.log != nil {
			s.log.Debug(
				"drop stream delta: publish buffer full",
				"agent_id", s.agentID,
				"model_call_context_id", s.modelCallContextID,
				"source_seq", envelope.SourceSeqStart,
			)
		}
	}
}

func (s *harnessStreamSink) mintToolCallID(providerCallID string) (string, bool) {
	s.toolCallMu.Lock()
	defer s.toolCallMu.Unlock()
	if existing, ok := s.toolCalls[providerCallID]; ok {
		return existing.publicID, true
	}
	id, err := uuid.NewV7()
	if err != nil {
		if s.log != nil {
			s.log.Warn("mint streamed tool call id failed", "error", err)
		}
		return "", false
	}
	publicID, err := publicid.Encode(publicid.KindToolCall, id)
	if err != nil {
		if s.log != nil {
			s.log.Warn("encode streamed tool call id failed", "error", err)
		}
		return "", false
	}
	s.toolCalls[providerCallID] = streamToolCall{id: id, publicID: publicID}
	return publicID, true
}

func (s *harnessStreamSink) ToolCallIDs() map[string]storage.ID {
	if s == nil {
		return nil
	}
	s.toolCallMu.Lock()
	defer s.toolCallMu.Unlock()
	ids := make(map[string]storage.ID, len(s.toolCalls))
	for providerCallID, call := range s.toolCalls {
		ids[providerCallID] = call.id
	}
	return ids
}

func (s *harnessStreamSink) Close() {
	if s == nil {
		return
	}
	s.eventsMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	s.eventsMu.Unlock()
	select {
	case <-s.done:
	case <-time.After(streamSinkCloseTimeout):
	}
}

func (s *harnessStreamSink) run(ctx context.Context) {
	defer close(s.done)
	for envelope, ok := s.nextCoalesced(ctx); ok; envelope, ok = s.nextCoalesced(ctx) {
		s.publish(ctx, envelope)
	}
}

func (s *harnessStreamSink) nextCoalesced(ctx context.Context) (streamEnvelope, bool) {
	envelope, ok := <-s.events
	if !ok {
		return streamEnvelope{}, false
	}
	if !coalescableStreamEvent(envelope.Event.Kind) {
		return envelope, true
	}
	for {
		select {
		case next, ok := <-s.events:
			if !ok {
				return envelope, true
			}
			if next.Event.Kind == envelope.Event.Kind && next.Event.BlockIndex == envelope.Event.BlockIndex {
				envelope.Event.Delta += next.Event.Delta
				envelope.SourceSeqEnd = next.SourceSeqEnd
				envelope.CoalescedCount += next.CoalescedCount
				continue
			}
			s.publish(ctx, envelope)
			if !coalescableStreamEvent(next.Event.Kind) {
				return next, true
			}
			envelope = next
		default:
			return envelope, true
		}
	}
}

func coalescableStreamEvent(kind model.StreamEventKind) bool {
	switch kind {
	case model.StreamEventTextDelta, model.StreamEventThinkingDelta, model.StreamEventToolArgsDelta:
		return true
	default:
		return false
	}
}

func (s *harnessStreamSink) publish(ctx context.Context, envelope streamEnvelope) {
	envelope.Seq = s.frameSeq.Add(1)
	event, err := publicStreamDelta(envelope.Event)
	if err != nil {
		s.logDroppedFrame(ctx, envelope, err)
		return
	}
	payload, err := json.Marshal(streamDeltaEnvelope{
		TurnID:             envelope.TurnID,
		ModelCallContextID: envelope.ModelCallContextID,
		Seq:                envelope.Seq,
		SourceSeqStart:     envelope.SourceSeqStart,
		SourceSeqEnd:       envelope.SourceSeqEnd,
		CoalescedCount:     envelope.CoalescedCount,
		Event:              event,
	})
	if err != nil {
		s.logDroppedFrame(ctx, envelope, err)
		return
	}
	publishCtx, cancel := context.WithTimeout(ctx, streamSinkPublishTimeout)
	defer cancel()
	err = publishStreamDelta(publishCtx, s.publisher, s.agentID, payload)
	if err != nil && s.log != nil {
		s.log.Debug(
			"publish stream delta failed",
			"agent_id", s.agentID,
			"model_call_context_id", s.modelCallContextID,
			"seq", envelope.Seq,
			"source_seq_start", envelope.SourceSeqStart,
			"source_seq_end", envelope.SourceSeqEnd,
			"error", err,
		)
	}
}

func (s *harnessStreamSink) logDroppedFrame(ctx context.Context, envelope streamEnvelope, err error) {
	event := logpkg.NewEvent(ctx, "agent.stream_delta.dropped", logpkg.Fields{
		"agent.id":                s.agentID,
		"turn.id":                 envelope.TurnID,
		"model_call_context.id":   envelope.ModelCallContextID,
		"stream.seq":              envelope.Seq,
		"stream.source_seq_start": envelope.SourceSeqStart,
		"stream.source_seq_end":   envelope.SourceSeqEnd,
		"stream.event_kind":       string(envelope.Event.Kind),
	})
	event.Error(err)
	event.Done(ctx)
}

func publishStreamDelta(
	ctx context.Context,
	publisher notifications.AgentStreamDeltaPublisher,
	agentID storage.ID,
	payload json.RawMessage,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("stream publisher panic: %v", recovered)
		}
	}()
	return publisher.PublishAgentStreamDelta(ctx, agentID, payload)
}
