package log

import (
	"context"
	"errors"
	"net/http"

	"github.com/omnara-ai/omnara/internal/errutil"
)

const statusClientClosedRequest = 499

func HTTPRequest(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
) (context.Context, *ResponseRecorder, *Event) {
	event := NewEvent(ctx, "http.request", Fields{
		"http.method":     r.Method,
		"http.path":       r.URL.Path,
		"http.user_agent": r.UserAgent(),
	})
	rec := NewResponseRecorder(w)
	event.beforeDoneFunc(func(f Finalizer) {
		status := rec.StatusCode()
		if errors.Is(ctx.Err(), context.Canceled) {
			fields := Fields{
				"http.request.cancel_cause":  context.Cause(ctx).Error(),
				"http.request.cancel_source": "request_context",
			}
			if deadline, ok := ctx.Deadline(); ok {
				fields["http.request.deadline"] = deadline.UTC()
			}
			f.Attach(fields)
			if errutil.OnlyMatches(f.e.err, context.Canceled) {
				status = statusClientClosedRequest
				f.e.err = nil
				f.e.level = InfoLevel
				f.e.levelSet = true
			}
		}
		rec.telemetryStatus = status
		f.Attach(Fields{
			"http.status_code":    status,
			"http.response_bytes": rec.BytesWritten(),
		})
		switch {
		case status >= http.StatusInternalServerError:
			f.Level(ErrorLevel)
		case status >= http.StatusBadRequest:
			f.Level(WarnLevel)
		}
	})
	return WithEvent(ctx, event), rec, event
}

type ResponseRecorder struct {
	http.ResponseWriter
	status          int
	telemetryStatus int
	bytes           int64
}

func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
	return &ResponseRecorder{ResponseWriter: w}
}

func (r *ResponseRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *ResponseRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *ResponseRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(body)
	r.bytes += int64(n)
	return n, err
}

func (r *ResponseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
		return
	}
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *ResponseRecorder) Started() bool { return r.status != 0 }

func (r *ResponseRecorder) StatusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *ResponseRecorder) TelemetryStatusCode() int {
	if r.telemetryStatus == 0 {
		return r.StatusCode()
	}
	return r.telemetryStatus
}

func (r *ResponseRecorder) BytesWritten() int64 { return r.bytes }
