package wideevent

import (
	"context"
	"errors"
	"net/http"
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
			if OnlyCanceled(f.e.err) {
				status = statusClientClosedRequest
				f.e.err = nil
				f.e.level = InfoLevel
				f.e.levelSet = true
			}
		}
		if rec.aborted {
			// The client received a truncated response whatever status was
			// already written, so telemetry reports a server failure.
			f.Attach(Fields{
				"http.response_aborted":    true,
				"http.written_status_code": rec.StatusCode(),
			})
			status = http.StatusInternalServerError
		}
		rec.telemetryStatus = status
		f.Attach(Fields{
			"http.status_code":    status,
			"http.response_bytes": rec.BytesWritten(),
		})
		switch {
		case status >= http.StatusInternalServerError:
			f.Escalate(ErrorLevel)
		case status >= http.StatusBadRequest:
			f.Level(WarnLevel)
		}
	})
	event.requestCanceled = func() bool { return ctx.Err() != nil }
	return WithEvent(ctx, event), rec, event
}

type ResponseRecorder struct {
	http.ResponseWriter
	status          int
	telemetryStatus int
	bytes           int64
	aborted         bool
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

// Abort records that the handler abandoned the response, so the event and
// metrics report a server error instead of the status already written.
func (r *ResponseRecorder) Abort() { r.aborted = true }

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

// OnlyCanceled reports whether err is nothing but context cancellation, so
// shutdown can be told apart from a failure that happened to race it.
func OnlyCanceled(err error) bool { return onlyMatches(err, context.Canceled) }

// onlyMatches reports whether every leaf of err's wrap tree matches target, so
// a cancellation joined with an unrelated failure still reports the failure.
func onlyMatches(err, target error) bool {
	if err == nil || target == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyMatches(cause, target) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyMatches(wrapped.Unwrap(), target)
	}
	return errors.Is(err, target)
}
