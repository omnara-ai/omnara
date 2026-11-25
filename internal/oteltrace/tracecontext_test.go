package oteltrace

import "testing"

const (
	testTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	testTraceState  = "vendor=value,other=1"
)

func TestParseRemoteParent(t *testing.T) {
	parent, ok := ParseRemoteParent(" "+testTraceParent+" ", testTraceState)
	if !ok {
		t.Fatal("a valid traceparent parses")
	}
	if parent.TraceID.String() != "4bf92f3577b34da6a3ce929d0e0e4736" || parent.SpanID.String() != "00f067aa0ba902b7" ||
		parent.Flags != 1 || parent.State != testTraceState {
		t.Fatalf("unexpected parent: %+v", parent)
	}
	// A later version may carry fields this one does not know.
	if _, ok := ParseRemoteParent("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra", ""); !ok {
		t.Fatal("a later version with extra fields parses")
	}
	for name, traceparent := range map[string]string{
		"empty":             "",
		"too short":         "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
		"zero trace id":     "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"zero span id":      "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"uppercase":         "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"not hex":           "00-4bf92f3577b34da6a3ce929d0e0e473g-00f067aa0ba902b7-01",
		"version ff":        "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"version 00 extras": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
		"trailing junk":     "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01extra",
		"wrong separators":  "00_4bf92f3577b34da6a3ce929d0e0e4736_00f067aa0ba902b7_01",
	} {
		if _, ok := ParseRemoteParent(traceparent, testTraceState); ok {
			t.Errorf("%s: %q must not parse", name, traceparent)
		}
	}
}

func TestNormalizeTraceContext(t *testing.T) {
	parent, state := NormalizeTraceContext("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0a-extra", testTraceState)
	if parent != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0a" || state != testTraceState {
		t.Fatalf("unexpected normalized context: %q %q", parent, state)
	}
	// The tracestate means nothing without the traceparent it belongs to.
	if parent, state := NormalizeTraceContext("garbage", testTraceState); parent != "" || state != "" {
		t.Fatalf("a malformed traceparent is ignored with its tracestate, got %q %q", parent, state)
	}
	for name, tracestate := range map[string]string{
		"too long":      string(make([]byte, maxTraceStateBytes+1)),
		"not printable": "vendor=va\nlue",
	} {
		if parent, state := NormalizeTraceContext(testTraceParent, tracestate); parent != testTraceParent || state != "" {
			t.Errorf("%s: an unusable tracestate is dropped alone, got %q %q", name, parent, state)
		}
	}
}
