// Package httprequest completes the telemetry for each inbound HTTP request:
// one wide event and one metrics observation, written from the same final
// status so logs and metrics agree even when a handler panics.
package httprequest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/omnara-ai/omnara/observability/wideevent"
)

type Config struct {
	Logger *slog.Logger
	// Metrics may be nil.
	Metrics *metrics.HTTPRecorder
	// Mux labels requests that a middleware answered before the mux routed
	// them, such as an authentication failure. It may be nil.
	Mux *http.ServeMux
	// WriteInternalError writes the response for a handler that panicked
	// before writing one. Nil writes a plain-text 500.
	WriteInternalError func(http.ResponseWriter)
}

func Handler(config Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observe := config.Metrics.Start()
		ctx := wideevent.WithLogger(request.Context(), config.Logger)
		ctx, recorder, event := wideevent.HTTPRequest(ctx, w, request)
		routed := request.WithContext(ctx)
		defer func() {
			pattern := config.route(routed)
			if pattern != "" {
				event.Attach(wideevent.Fields{"http.route": pattern})
			}
			event.Done(ctx)
			observe(request.Method, pattern, recorder.TelemetryStatusCode())
		}()
		defer config.recoverPanic(ctx, recorder)
		next.ServeHTTP(recorder, routed)
	})
}

func (config Config) route(request *http.Request) string {
	if request.Pattern != "" || config.Mux == nil {
		return request.Pattern
	}
	_, pattern := config.Mux.Handler(request)
	return pattern
}

// recoverPanic aborts a started response so a truncated body is not mistaken
// for a complete one.
func (config Config) recoverPanic(ctx context.Context, recorder *wideevent.ResponseRecorder) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		recorder.Abort()
		panic(recovered)
	}
	wideevent.Error(ctx, fmt.Errorf("http handler panicked: %v", recovered))
	wideevent.Attach(ctx, wideevent.Fields{"error.stack": string(debug.Stack())})
	wideevent.Escalate(ctx, wideevent.ErrorLevel)
	if recorder.Started() {
		recorder.Abort()
		panic(http.ErrAbortHandler)
	}
	if config.WriteInternalError == nil {
		http.Error(recorder, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	config.WriteInternalError(recorder)
}
