package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestModelCostBacklogSampleIsCappedBoundedAndRecovers(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	_, err := sampleModelCostBacklog(t.Context(), logger, func(ctx context.Context, limit int32) (int64, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("sample requires a deadline")
		}
		if limit != modelCostBacklogCeiling {
			t.Fatalf("limit = %d, want %d", limit, modelCostBacklogCeiling)
		}
		panic("test failure")
	})
	if err == nil || !strings.Contains(err.Error(), "test failure") {
		t.Fatalf("panic was not returned as a failed sample: %v", err)
	}
	pending, err := sampleModelCostBacklog(t.Context(), logger,
		func(context.Context, int32) (int64, error) { return 3, nil })
	if err != nil || pending != 3 {
		t.Fatalf("next sample = %d, %v; want 3", pending, err)
	}
}

func TestModelCostBacklogJobReturnsSampleFailure(t *testing.T) {
	failure := errors.New("store failed")
	run := func(ctx context.Context, err error) maintenanceResult {
		return recordModelCostBacklog(slog.New(slog.DiscardHandler),
			func(context.Context, int32) (int64, error) { return 0, err }, nil)(ctx)
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
