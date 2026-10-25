package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type readDeadlineRecorder struct {
	http.ResponseWriter
	deadlines []time.Time
}

func (w *readDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestIntegrationCallbackBodyClearsReadDeadlineAfterCompleteRead(t *testing.T) {
	t.Parallel()
	w := &readDeadlineRecorder{ResponseWriter: httptest.NewRecorder()}
	raw, ok := readIntegrationCallbackBody(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")), 2)
	if !ok || string(raw) != "{}" || len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero() {
		t.Fatalf("raw=%q ok=%v deadlines=%v", raw, ok, w.deadlines)
	}
}
