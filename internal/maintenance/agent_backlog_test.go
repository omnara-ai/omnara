package maintenance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestAgentBacklogRecoversAndCancelsSample(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var sampleDone <-chan struct{}
	_, err := sampleAgentBacklog(t.Context(), logger, func(ctx context.Context) (executionstore.ReadyAgentBacklog, error) {
		sampleDone = ctx.Done()
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("sample requires a deadline")
		}
		panic("test failure")
	})
	if err == nil || !strings.Contains(err.Error(), "test failure") {
		t.Fatalf("panic was not returned as a failed sample: %v", err)
	}
	select {
	case <-sampleDone:
	default:
		t.Fatal("sample context was not canceled after panic")
	}
	want := executionstore.ReadyAgentBacklog{Ready: 3, OldestReadyAge: time.Second}
	backlog, err := sampleAgentBacklog(t.Context(), logger,
		func(context.Context) (executionstore.ReadyAgentBacklog, error) { return want, nil })
	if err != nil || backlog != want {
		t.Fatalf("next sample failed after recovery: backlog=%+v, err=%v", backlog, err)
	}
}

func TestAgentBacklogJobReturnsSampleFailure(t *testing.T) {
	failure := errors.New("store failed")
	run := func(ctx context.Context, err error) maintenanceResult {
		return recordAgentBacklog(slog.New(slog.DiscardHandler),
			func(context.Context) (executionstore.ReadyAgentBacklog, error) {
				return executionstore.ReadyAgentBacklog{}, err
			}, nil)(ctx)
	}
	if result := run(t.Context(), nil); result.err != nil {
		t.Fatalf("successful sample returned %v", result.err)
	}
	if result := run(t.Context(), failure); !errors.Is(result.err, failure) {
		t.Fatalf("failed sample returned %v, want %v", result.err, failure)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if result := run(canceled, context.Canceled); result.err != nil {
		t.Fatalf("shutdown returned %v, want nil", result.err)
	}
}
