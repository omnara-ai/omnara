package log

import (
	"context"
	"log/slog"
	"os"
)

type eventContextKey struct{}
type loggerContextKey struct{}

var Default = slog.New(slog.NewJSONHandler(os.Stdout, jsonHandlerOptions(false)))

func WithLogger(ctx context.Context, log *slog.Logger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerContextKey{}, log)
}

func LoggerFromContext(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && log != nil {
		return log
	}
	return Default
}

func WithEvent(ctx context.Context, event *Event) context.Context {
	if event == nil {
		return ctx
	}
	return context.WithValue(ctx, eventContextKey{}, event)
}

func FromContext(ctx context.Context) (*Event, bool) {
	event, ok := ctx.Value(eventContextKey{}).(*Event)
	return event, ok && event != nil
}

func Attach(ctx context.Context, fieldSets ...Fields) {
	if event, ok := FromContext(ctx); ok {
		event.Attach(fieldSets...)
	}
}

func Error(ctx context.Context, err error) {
	if event, ok := FromContext(ctx); ok {
		event.Error(err)
	}
}

func Level(ctx context.Context, level EventLevel) {
	if event, ok := FromContext(ctx); ok {
		event.Level(level)
	}
}
