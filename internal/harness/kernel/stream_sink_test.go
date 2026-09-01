package kernel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
)

type capturingStreamPublisher struct {
	mu       sync.Mutex
	payloads []json.RawMessage
	panicOn  bool
}

func (p *capturingStreamPublisher) PublishAgentStreamDelta(
	ctx context.Context,
	_ uuid.UUID,
	payload json.RawMessage,
) error {
	if p.panicOn {
		panic("publisher failure")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.payloads = append(p.payloads, payload)
	return nil
}

func (p *capturingStreamPublisher) envelopes(t *testing.T) []streamEnvelope {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	envelopes := make([]streamEnvelope, 0, len(p.payloads))
	for _, payload := range p.payloads {
		var envelope streamEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode published envelope: %v", err)
		}
		envelopes = append(envelopes, envelope)
	}
	return envelopes
}

func newTestStreamSink(publisher *capturingStreamPublisher) *harnessStreamSink {
	sink := newUnstartedTestStreamSink(publisher)
	go sink.run(context.Background())
	return sink
}

func newUnstartedTestStreamSink(publisher *capturingStreamPublisher) *harnessStreamSink {
	sink := &harnessStreamSink{
		publisher:          publisher,
		agentID:            uuid.New(),
		turnID:             "trn_test",
		modelCallContextID: "mcc_test",
		toolCalls:          make(map[string]streamToolCall),
		events:             make(chan streamEnvelope, streamSinkBufferSize),
		done:               make(chan struct{}),
	}
	return sink
}

func TestHarnessStreamSinkContainsPublisherPanic(t *testing.T) {
	sink := newTestStreamSink(&capturingStreamPublisher{panicOn: true})
	sink.Emit(context.Background(), model.StreamEvent{
		Kind:       model.StreamEventTextDelta,
		BlockIndex: 0,
		Delta:      "hello",
	})
	sink.Close()
}

func TestHarnessStreamSinkPublishesInOrderAndCloseFlushes(t *testing.T) {
	publisher := &capturingStreamPublisher{}
	sink := newTestStreamSink(publisher)
	ctx := context.Background()
	sink.Emit(ctx, model.StreamEvent{
		Kind:       model.StreamEventBlockStart,
		BlockIndex: 0,
		Block:      &model.StreamBlock{Kind: model.StreamBlockText},
	})
	sink.Emit(ctx, model.StreamEvent{Kind: model.StreamEventTextDelta, BlockIndex: 0, Delta: "hello"})
	sink.Emit(ctx, model.StreamEvent{Kind: model.StreamEventBlockStop, BlockIndex: 0})
	sink.Close()
	envelopes := publisher.envelopes(t)
	if len(envelopes) == 0 {
		t.Fatal("expected published envelopes after close")
	}
	var text string
	lastSeq := uint64(0)
	for _, envelope := range envelopes {
		if envelope.TurnID != "trn_test" {
			t.Fatalf("envelope turn id = %q, want %q", envelope.TurnID, "trn_test")
		}
		if envelope.Seq <= lastSeq {
			t.Fatalf("sequence not increasing: %+v", envelopes)
		}
		lastSeq = envelope.Seq
		if envelope.SourceSeqStart == 0 || envelope.SourceSeqEnd < envelope.SourceSeqStart {
			t.Fatalf("invalid source sequence range: %+v", envelope)
		}
		if envelope.Event.Kind == model.StreamEventTextDelta {
			text += envelope.Event.Delta
		}
	}
	if text != "hello" {
		t.Fatalf("expected flushed text delta, got %q", text)
	}
	if envelopes[len(envelopes)-1].Event.Kind != model.StreamEventBlockStop {
		t.Fatalf("expected block stop last, got %+v", envelopes[len(envelopes)-1])
	}
}

func TestHarnessStreamSinkIgnoresEmitAfterClose(t *testing.T) {
	publisher := &capturingStreamPublisher{}
	sink := newTestStreamSink(publisher)
	sink.Close()
	sink.Emit(context.Background(), model.StreamEvent{
		Kind:       model.StreamEventTextDelta,
		BlockIndex: 0,
		Delta:      "late",
	})
	sink.Close()
	if envelopes := publisher.envelopes(t); len(envelopes) != 0 {
		t.Fatalf("published after close: %+v", envelopes)
	}
}

func TestHarnessStreamSinkPreMintsPublicToolCallIDs(t *testing.T) {
	publisher := &capturingStreamPublisher{}
	sink := newTestStreamSink(publisher)
	ctx := context.Background()
	sink.Emit(ctx, model.StreamEvent{
		Kind:       model.StreamEventBlockStart,
		BlockIndex: 0,
		Block: &model.StreamBlock{
			Kind:       model.StreamBlockToolUse,
			ToolCallID: "provider-call-1",
			ToolName:   "shell",
		},
	})
	sink.Emit(ctx, model.StreamEvent{
		Kind:       model.StreamEventBlockStart,
		BlockIndex: 0,
		Block: &model.StreamBlock{
			Kind:       model.StreamBlockToolUse,
			ToolCallID: "provider-call-1",
			ToolName:   "shell",
		},
	})
	sink.Emit(ctx, model.StreamEvent{
		Kind:       model.StreamEventBlockStart,
		BlockIndex: 1,
		Block: &model.StreamBlock{
			Kind:       model.StreamBlockToolUse,
			ToolCallID: "provider-call-2",
			ToolName:   "web_search",
		},
	})
	sink.Close()

	envelopes := publisher.envelopes(t)
	frameToolCallIDs := make([]string, 0, len(envelopes))
	for _, envelope := range envelopes {
		if envelope.Event.Block == nil || envelope.Event.Block.Kind != model.StreamBlockToolUse {
			continue
		}
		frameToolCallIDs = append(frameToolCallIDs, envelope.Event.Block.ToolCallID)
	}
	if len(frameToolCallIDs) != 3 {
		t.Fatalf("expected 3 tool_use frames, got %+v", frameToolCallIDs)
	}
	if frameToolCallIDs[0] != frameToolCallIDs[1] {
		t.Fatalf(
			"repeated provider call minted different ids: %q vs %q",
			frameToolCallIDs[0],
			frameToolCallIDs[1],
		)
	}
	if frameToolCallIDs[0] == frameToolCallIDs[2] {
		t.Fatalf("distinct provider calls share id %q", frameToolCallIDs[0])
	}

	minted := sink.ToolCallIDs()
	if len(minted) != 2 {
		t.Fatalf("minted ids = %+v, want 2 entries", minted)
	}
	for providerCallID, id := range minted {
		publicID, err := publicid.Encode(publicid.KindToolCall, id)
		if err != nil {
			t.Fatalf("encode minted id: %v", err)
		}
		var want string
		if providerCallID == "provider-call-1" {
			want = frameToolCallIDs[0]
		} else {
			want = frameToolCallIDs[2]
		}
		if publicID != want {
			t.Fatalf("minted id for %q encodes to %q, want frame id %q", providerCallID, publicID, want)
		}
	}
}

func TestHarnessStreamSinkEmitNeverBlocksAndDropsWhenQueueIsFull(t *testing.T) {
	publisher := &capturingStreamPublisher{}
	sink := newUnstartedTestStreamSink(publisher)
	ctx := context.Background()
	emitted := streamSinkBufferSize * 3
	emitDone := make(chan struct{})
	go func() {
		defer close(emitDone)
		for i := range emitted {
			sink.Emit(ctx, model.StreamEvent{
				Kind:       model.StreamEventBlockStart,
				BlockIndex: i,
				Block:      &model.StreamBlock{Kind: model.StreamBlockText},
			})
		}
	}()
	select {
	case <-emitDone:
	case <-time.After(5 * time.Second):
		go sink.run(context.Background())
		<-emitDone
		sink.Close()
		t.Fatal("emit blocked while the stream queue was full")
	}
	go sink.run(context.Background())
	sink.Close()
	envelopes := publisher.envelopes(t)
	if len(envelopes) != streamSinkBufferSize {
		t.Fatalf("published envelopes = %d, want queue capacity %d", len(envelopes), streamSinkBufferSize)
	}
	lastSeq := uint64(0)
	lastSourceSeqEnd := uint64(0)
	for _, envelope := range envelopes {
		if envelope.Seq <= lastSeq {
			t.Fatalf("sequence not increasing: %+v", envelopes)
		}
		lastSeq = envelope.Seq
		if envelope.SourceSeqEnd <= lastSourceSeqEnd {
			t.Fatalf("source sequence not increasing: %+v", envelopes)
		}
		lastSourceSeqEnd = envelope.SourceSeqEnd
	}
	if lastSourceSeqEnd != uint64(streamSinkBufferSize) {
		t.Fatalf("last published source sequence = %d, want %d", lastSourceSeqEnd, streamSinkBufferSize)
	}
}

func TestHarnessStreamSinkCoalescesQueuedDeltas(t *testing.T) {
	publisher := &capturingStreamPublisher{}
	sink := newUnstartedTestStreamSink(publisher)
	ctx := context.Background()
	sink.Emit(ctx, model.StreamEvent{Kind: model.StreamEventTextDelta, BlockIndex: 0, Delta: "a"})
	sink.Emit(ctx, model.StreamEvent{Kind: model.StreamEventTextDelta, BlockIndex: 0, Delta: "b"})
	sink.Emit(ctx, model.StreamEvent{Kind: model.StreamEventTextDelta, BlockIndex: 0, Delta: "c"})
	go sink.run(ctx)
	sink.Close()
	envelopes := publisher.envelopes(t)
	var text string
	for _, envelope := range envelopes {
		if envelope.Event.Kind == model.StreamEventTextDelta {
			text += envelope.Event.Delta
		}
	}
	if text != "abc" {
		t.Fatalf("expected coalesced deltas to preserve content, got %q", text)
	}
	if len(envelopes) != 1 {
		t.Fatalf("expected one coalesced envelope, got %+v", envelopes)
	}
	if envelopes[0].CoalescedCount != 3 {
		t.Fatalf("coalesced count = %d, want 3", envelopes[0].CoalescedCount)
	}
	if envelopes[0].SourceSeqStart != 1 || envelopes[0].SourceSeqEnd != 3 {
		t.Fatalf("source range = %d-%d, want 1-3", envelopes[0].SourceSeqStart, envelopes[0].SourceSeqEnd)
	}
}
