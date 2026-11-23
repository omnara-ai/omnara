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
	socket.toolCallsActive = daemonToolCallLimit
	for i := range daemonToolCallCancelQueueLimit + 8 {
		socket.cancelToolCall(t.Context(), daemonprotocol.ToolCallCancel{ProcessID: "prc", Key: strconv.Itoa(i)})
	}
	require.Len(t, socket.toolCallCancelsQueued, daemonToolCallCancelQueueLimit,
		"past the bound a cancel is left for the daemon to send again")
	require.Empty(t, socket.send)

	// A freed slot hands over the oldest held cancel.
	next := socket.releaseToolCallSlotLocked()
	require.NotNil(t, next)
	require.Equal(t, "0", next.Key)
	require.Equal(t, daemonToolCallLimit-1, socket.toolCallsActive)
	require.Len(t, socket.toolCallCancelsQueued, daemonToolCallCancelQueueLimit-1)
}
