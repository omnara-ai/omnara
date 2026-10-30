package kernel

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/stretchr/testify/require"
)

func TestStreamCoalescingTimerAndBoundaryLatency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		publisher := &capturingStreamPublisher{}
		sink := newTestStreamSink(publisher)
		defer sink.Close()
		emit := func(text string) {
			sink.Emit(t.Context(), model.StreamEvent{Kind: model.StreamEventTextDelta, Delta: text})
			synctest.Wait()
		}
		emit("a")
		<-time.After(50 * time.Millisecond)
		emit("b")
		require.Empty(t, publisher.envelopes(t))
		<-time.After(50 * time.Millisecond)
		synctest.Wait()
		frames := publisher.envelopes(t)
		require.Len(t, frames, 1)
		require.Equal(t, "ab", frames[0].Event.Delta)
		emit("c")
		<-time.After(99 * time.Millisecond)
		require.Len(t, publisher.envelopes(t), 1)
		sink.Emit(t.Context(), model.StreamEvent{Kind: model.StreamEventBlockStop})
		synctest.Wait()
		frames = publisher.envelopes(t)
		require.Len(t, frames, 3)
		require.Equal(t, "c", frames[1].Event.Delta)
		require.Equal(t, model.StreamEventBlockStop, frames[2].Event.Kind)
		<-time.After(time.Second)
		synctest.Wait()
		require.Len(t, publisher.envelopes(t), 3)
	})
}

func TestStreamCoalescingInterleavedBlocksAndDrain(t *testing.T) {
	publisher := &capturingStreamPublisher{}
	sink := newUnstartedTestStreamSink(publisher)
	events := []model.StreamEvent{
		{Kind: model.StreamEventToolArgsDelta, BlockIndex: 0, Delta: "a"},
		{Kind: model.StreamEventToolArgsDelta, BlockIndex: 0, Delta: "b"},
		{Kind: model.StreamEventToolArgsDelta, BlockIndex: 1, Delta: "c"},
		{Kind: model.StreamEventToolArgsDelta, BlockIndex: 0, Delta: "d"},
		{Kind: model.StreamEventThinkingDelta, BlockIndex: 0, Delta: "e"},
		{Kind: model.StreamEventTextDelta, BlockIndex: 0, Delta: "f"},
	}
	for _, event := range events {
		sink.Emit(t.Context(), event)
	}
	go sink.run(t.Context())
	sink.Close()
	frames := publisher.envelopes(t)
	require.Len(t, frames, 5)
	for i, want := range []string{"ab", "c", "d", "e", "f"} {
		require.Equal(t, want, frames[i].Event.Delta)
		require.Equal(t, uint64(i+1), frames[i].Seq)
		if i > 0 {
			require.Equal(t, frames[i-1].SourceSeqEnd+1, frames[i].SourceSeqStart)
		}
	}
	require.Equal(t, uint64(2), frames[0].CoalescedCount)
	require.Equal(t, uint64(6), frames[4].SourceSeqEnd)
}

type blockedStreamPublisher struct {
	captured capturingStreamPublisher
	entered  chan struct{}
	release  chan struct{}
}

func (p *blockedStreamPublisher) PublishAgentStreamDelta(
	ctx context.Context, id uuid.UUID, body json.RawMessage,
) error {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
		return p.captured.PublishAgentStreamDelta(ctx, id, body)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestStreamCoalescingBackpressureAndOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		publisher := &blockedStreamPublisher{entered: make(chan struct{}, 1), release: make(chan struct{})}
		sink := newUnstartedTestStreamSink(&capturingStreamPublisher{})
		sink.publisher = publisher
		go sink.run(t.Context())
		sink.Emit(t.Context(), model.StreamEvent{Kind: model.StreamEventBlockStop})
		<-publisher.entered
		for range streamSinkBufferSize + 10 {
			sink.Emit(t.Context(), model.StreamEvent{Kind: model.StreamEventTextDelta, Delta: "a"})
		}
		require.Equal(t, streamSinkBufferSize, len(sink.events))
		close(publisher.release)
		synctest.Wait()
		sink.Emit(t.Context(), model.StreamEvent{Kind: model.StreamEventTextDelta, Delta: "z"})
		sink.Close()
		frames := publisher.captured.envelopes(t)
		require.Len(t, frames, 3)
		require.Len(t, frames[1].Event.Delta, streamSinkBufferSize)
		require.Equal(t, uint64(streamSinkBufferSize), frames[1].CoalescedCount)
		require.Equal(t, frames[1].SourceSeqEnd+11, frames[2].SourceSeqStart)
		require.Equal(t, "z", frames[2].Event.Delta)
	})
}

func TestStreamCoalescingCloseFlushesPendingWithoutTimerDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		publisher := &capturingStreamPublisher{}
		sink := newTestStreamSink(publisher)
		sink.Emit(t.Context(), model.StreamEvent{Kind: model.StreamEventTextDelta, Delta: "pending"})
		synctest.Wait()
		require.Empty(t, publisher.envelopes(t))
		start := time.Now()
		sink.Close()
		require.Equal(t, start, time.Now())
		frames := publisher.envelopes(t)
		require.Len(t, frames, 1)
		require.Equal(t, "pending", frames[0].Event.Delta)
	})
}
