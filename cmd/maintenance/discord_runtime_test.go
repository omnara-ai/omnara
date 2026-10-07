package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestDiscordRuntimeDemandRecoversAndCancelsSample(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var sampleDone <-chan struct{}
	_, err := sampleDiscordRuntimeDemand(t.Context(), logger, func(ctx context.Context) (int64, error) {
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
	count, err := sampleDiscordRuntimeDemand(t.Context(), logger, func(context.Context) (int64, error) {
		return 3, nil
	})
	if err != nil || count != 3 {
		t.Fatalf("next sample failed after recovery: count=%d, err=%v", count, err)
	}
}
