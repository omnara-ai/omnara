package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/stretchr/testify/require"
)

// Fill before the consumer starts, making buffer overflow deterministic without
// depending on socket scheduling or Redis. Both callbacks are the route's real
// subscription callbacks.
type overflowingAgentSubscriber struct {
	noopAgentNotificationSubscriber
}

func (overflowingAgentSubscriber) SubscribeAgentToolCallUpdates(
	ctx context.Context, agentID uuid.UUID, receive func(context.Context, notifications.ToolCallUpdatedCommitted),
) (notifications.Subscription, error) {
	for range streamFrameChannelSize + 2 {
		receive(ctx, notifications.ToolCallUpdatedCommitted{AgentID: agentID})
	}
	return noopSubscription{}, nil
}

func (overflowingAgentSubscriber) SubscribeAgentStreamDeltas(
	ctx context.Context, _ uuid.UUID, receive func(context.Context, json.RawMessage),
) (notifications.Subscription, error) {
	for range streamFrameChannelSize + 3 {
		receive(ctx, json.RawMessage(`{}`))
	}
	return noopSubscription{}, nil
}

func TestAgentStreamCountsFullSubscriberBuffers(t *testing.T) {
	set := metrics.New()
	subscriber := overflowingAgentSubscriber{}
	server := &Server{
		log: slog.New(slog.DiscardHandler), agentStreamRecorder: metrics.NewAgentStreamRecorder(set),
		agentEventWakeupSubscriber: subscriber, agentToolCallUpdateSubscriber: subscriber,
		agentStreamDeltaSubscriber: subscriber,
	}
	server.streamAgentEvents(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/events", nil),
		identitystore.ProjectRecord{}, executionstore.AgentRecord{}, 0, true)
	scrape := httptest.NewRecorder()
	set.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, scrape.Body.String(), `omnara_agent_stream_buffer_dropped_total{frame="tool_call_update"} 2`)
	require.Contains(t, scrape.Body.String(), `omnara_agent_stream_buffer_dropped_total{frame="model_output_delta"} 3`)
}
