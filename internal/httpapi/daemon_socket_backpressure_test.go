package httpapi

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/stretchr/testify/require"
)

// heldWire is a websocket whose every write waits for the test to release it.
type heldWire struct {
	writing chan daemonprotocol.Message
	release chan struct{}
}

func newHeldWire() *heldWire {
	return &heldWire{writing: make(chan daemonprotocol.Message, 1024), release: make(chan struct{})}
}

func (w *heldWire) Read(ctx context.Context) (daemonprotocol.Message, error) {
	<-ctx.Done()
	return daemonprotocol.Message{}, ctx.Err()
}

func (w *heldWire) Write(ctx context.Context, msg daemonprotocol.Message) error {
	w.writing <- msg
	select {
	case <-w.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *heldWire) Ping(context.Context) error { return nil }

func (w *heldWire) Close(websocket.StatusCode, string) error { return nil }

func (w *heldWire) next(t *testing.T) daemonprotocol.Message {
	t.Helper()
	select {
	case msg := <-w.writing:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was written")
		return daemonprotocol.Message{}
	}
}

// A burst of tool call reports bigger than the send queue, arriving while the writer is stuck on a
// large report, waits its turn: it neither closes the socket nor holds up heartbeat acknowledgments.
func TestDaemonSocketToolCallReportBurstLeavesControlTrafficOpen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	wire := newHeldWire()
	socket := newDaemonSocket(&Server{}, wire, uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), false)
	writer := make(chan error, 1)
	go func() { writer <- socket.writeLoop(ctx) }()

	large := daemonprotocol.ToolCallOffer{ProcessID: "prc", OfferID: "large"}
	large.Key = "large"
	socket.reportToolCall(ctx, large, daemonprotocol.ToolCallReport{})
	require.Equal(t, "large", wire.next(t).ToolCallReport.OfferID, "the writer is stuck on the large report")

	const burst = 5 * 16
	require.Greater(t, burst, daemonSocketQueueSize)
	var reporters sync.WaitGroup
	for i := range burst {
		reporters.Go(func() {
			offer := daemonprotocol.ToolCallOffer{ProcessID: "prc", OfferID: strconv.Itoa(i)}
			offer.Key = strconv.Itoa(i)
			socket.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{
				ProgramToolCallResult: daemonprotocol.ProgramToolCallResult{
					State: daemonprotocol.ProgramToolCallStateCompleted}})
		})
	}
	require.Eventually(t, func() bool { return len(socket.toolSend) == cap(socket.toolSend) },
		5*time.Second, time.Millisecond, "the reports fill their queue")

	// The read loop is not held up either: an offer over the connection's limit is refused without
	// waiting for room in the report queue.
	socket.workMu.Lock()
	socket.toolCallsActive = daemonToolCallLimit
	socket.workMu.Unlock()
	refused := daemonprotocol.ToolCallOffer{ProcessID: "prc", OfferID: "refused"}
	refused.Key = "refused"
	handled := make(chan error, 1)
	go func() { handled <- socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &refused}) }()
	select {
	case err := <-handled:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the read loop waited on the report queue")
	}

	const heartbeats = 3
	for range heartbeats {
		require.True(t, socket.enqueueOrClose(daemonprotocol.Message{Type: daemonprotocol.MessageHeartbeatAck}),
			"a heartbeat acknowledgment still has room")
	}
	select {
	case <-socket.done:
		t.Fatal("a burst of tool call reports closed the socket")
	default:
	}

	wire.release <- struct{}{}
	for range heartbeats {
		require.Equal(t, daemonprotocol.MessageHeartbeatAck, wire.next(t).Type,
			"control traffic is written before the reports ahead of it")
		wire.release <- struct{}{}
	}
	close(wire.release)
	reported := map[string]bool{}
	for range burst + 1 {
		msg := wire.next(t)
		require.Equal(t, daemonprotocol.MessageToolCallReport, msg.Type)
		reported[msg.ToolCallReport.OfferID] = true
	}
	require.Len(t, reported, burst+1, "every report is written once")
	require.True(t, reported["refused"])
	reporters.Wait()
	socket.toolCallsDone.Wait()
	select {
	case <-socket.done:
		t.Fatal("the socket closed")
	case err := <-writer:
		t.Fatalf("the writer stopped: %v", err)
	default:
	}
}
