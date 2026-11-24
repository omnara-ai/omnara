package httpapi

import (
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/stretchr/testify/require"
)

func TestDaemonSocketToolCallCancelQueueIsBounded(t *testing.T) {
	socket := newDaemonSocket(nil, nil, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, false)
	socket.toolCallCancelsActive = daemonToolCallCancelLimit
	for i := range daemonToolCallCancelQueueLimit + 8 {
		socket.cancelToolCall(t.Context(), daemonprotocol.ToolCallCancel{ProcessID: "prc", Key: strconv.Itoa(i)})
	}
	require.Len(t, socket.toolCallCancelsQueued, daemonToolCallCancelQueueLimit,
		"past the bound a cancel is left for the daemon to send again")
	require.Empty(t, socket.toolSend)

	next := socket.releaseToolCallCancelSlotLocked()
	require.NotNil(t, next)
	require.Equal(t, "0", next.Key)
	require.Equal(t, daemonToolCallCancelLimit-1, socket.toolCallCancelsActive)
	require.Len(t, socket.toolCallCancelsQueued, daemonToolCallCancelQueueLimit-1)
}

func TestDaemonSocketToolCallCancelRunsWhenOfferSlotsAreFull(t *testing.T) {
	socket := newDaemonSocket(nil, nil, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, false)
	socket.toolCallsActive = daemonToolCallLimit
	cancel := daemonprotocol.ToolCallCancel{ProcessID: "not-a-process", Key: "k-1", OfferID: "o-1"}
	socket.cancelToolCall(t.Context(), cancel)
	socket.toolCallsDone.Wait()
	require.Len(t, socket.toolSend, 1)
	outbound := <-socket.toolSend
	require.NotNil(t, outbound.msg.ToolCallReport)
	require.True(t, outbound.msg.ToolCallReport.CancelRecorded)
	require.Equal(t, daemonToolCallLimit, socket.toolCallsActive)
	require.Zero(t, socket.toolCallCancelsActive)
	require.Empty(t, socket.toolCallCancelsQueued)
}

func TestDaemonSocketToolCallCancelsWaitForAReplacementOffer(t *testing.T) {
	const id = "prc\x00k-1"
	first := daemonprotocol.ToolCallOffer{ProcessID: "prc", OfferID: "o-a"}
	first.Key = "k-1"
	second := first
	second.OfferID = "o-b"
	cancelOf := func(offerID string, detach bool) daemonprotocol.ToolCallCancel {
		return daemonprotocol.ToolCallCancel{ProcessID: "prc", Key: "k-1", OfferID: offerID, DetachOnly: detach}
	}
	closed := func(ch chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	for name, tc := range map[string]struct {
		cancel                      daemonprotocol.ToolCallCancel
		adopted, detached, canceled bool
	}{
		"late detach from the replaced waiter leaves the replacement served": {
			cancel: cancelOf("o-a", true), adopted: true},
		"late cancel from the replaced waiter leaves the replacement served": {
			cancel: cancelOf("o-a", false), adopted: true},
		"detach from the replacement applies once it is served": {
			cancel: cancelOf("o-b", true), adopted: true, detached: true},
		"cancel from the replacement applies once it is served": {
			cancel: cancelOf("o-b", false), adopted: true, canceled: true},
		"detach from the first waiter applies when the replacement is turned away": {
			cancel: cancelOf("o-a", true), detached: true},
		"cancel from the first waiter applies when the replacement is turned away": {
			cancel: cancelOf("o-a", false), canceled: true},
		"detach from a replacement that was turned away is dropped": {
			cancel: cancelOf("o-b", true)},
	} {
		t.Run(name, func(t *testing.T) {
			socket := newDaemonSocket(nil, nil, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, uuid.Nil, false)
			call := newServedToolCall(first)
			socket.toolCalls[id] = call
			require.NoError(t, socket.handleToolCallOffer(t.Context(), daemonprotocol.Message{ToolCallOffer: &second}))
			require.Equal(t, "o-b", socket.nextToolCallOffer(call).OfferID)

			socket.cancelToolCall(t.Context(), tc.cancel)
			socket.cancelToolCall(t.Context(), tc.cancel)
			require.Len(t, call.held, 1, "a cancel is held while the replacement offer is being stored")
			require.False(t, closed(call.detached))
			require.False(t, closed(call.canceled))
			require.Same(t, call, socket.toolCalls[id])

			socket.adoptToolCallOffer(t.Context(), call, tc.adopted)
			socket.toolCallsDone.Wait()
			require.Nil(t, call.adopting)
			require.Empty(t, call.held)
			require.Equal(t, tc.detached, closed(call.detached))
			require.Equal(t, tc.canceled, closed(call.canceled))
			if tc.adopted {
				require.Equal(t, "o-b", call.offerID)
			} else {
				require.Equal(t, "o-a", call.offerID)
			}
			if tc.detached || tc.canceled {
				require.NotContains(t, socket.toolCalls, id)
			} else {
				require.Same(t, call, socket.toolCalls[id])
			}
		})
	}
}
