package httpapi

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

type taskLogHandler struct {
	records chan slog.Record
}

func (h taskLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h taskLogHandler) Handle(_ context.Context, record slog.Record) error {
	h.records <- record.Clone()
	return nil
}

func (h taskLogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h taskLogHandler) WithGroup(string) slog.Handler {
	return h
}

func TestStartMachinePoolTaskDetachesFromRequest(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	type observation struct {
		err         error
		hasDeadline bool
		hasLogger   bool
	}
	observed := make(chan observation, 1)
	startMachinePoolTask(parent, nil, time.Minute, "test", func(ctx context.Context, logger *slog.Logger) {
		_, hasDeadline := ctx.Deadline()
		observed <- observation{err: ctx.Err(), hasDeadline: hasDeadline, hasLogger: logger != nil}
	})

	select {
	case result := <-observed:
		if result.err != nil {
			t.Fatalf("detached task context error = %v, want nil", result.err)
		}
		if !result.hasDeadline {
			t.Fatal("detached task context has no deadline")
		}
		if !result.hasLogger {
			t.Fatal("detached task has no logger")
		}
	case <-time.After(time.Second):
		t.Fatal("detached task did not run")
	}
}

func TestStartMachinePoolTaskRecoversPanic(t *testing.T) {
	records := make(chan slog.Record, 1)
	logger := slog.New(taskLogHandler{records: records})
	startMachinePoolTask(context.Background(), logger, time.Minute, "test panic", func(context.Context, *slog.Logger) {
		panic("boom")
	})

	select {
	case record := <-records:
		if record.Level != slog.LevelError || record.Message != "machine pool background task panicked" {
			t.Fatalf("panic log = %s %q", record.Level, record.Message)
		}
		attributes := make(map[string]slog.Value)
		record.Attrs(func(attribute slog.Attr) bool {
			attributes[attribute.Key] = attribute.Value
			return true
		})
		if attributes["task"].String() != "test panic" || attributes["error"].Any() != "boom" {
			t.Fatalf("panic attributes = %+v", attributes)
		}
		if attributes["stack"].String() == "" {
			t.Fatal("panic log has no stack")
		}
	case <-time.After(time.Second):
		t.Fatal("panic was not recovered and logged")
	}
}
