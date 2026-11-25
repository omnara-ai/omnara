package oteltrace

import (
	"encoding/hex"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// maxTraceStateBytes is the tracestate length the W3C Trace Context
// specification asks every participant to be able to propagate.
const maxTraceStateBytes = 512

// RemoteParent is the W3C trace context a caller sent with the request that
// opened a turn. A turn that has one joins the caller's trace instead of
// starting its own.
type RemoteParent struct {
	TraceID pcommon.TraceID
	SpanID  pcommon.SpanID
	Flags   byte
	State   string
}

// IsZero reports whether the turn has no remote parent.
func (parent RemoteParent) IsZero() bool {
	return parent.TraceID.IsEmpty()
}

// NormalizeTraceContext returns the traceparent and tracestate header values
// in the form they are stored in. As the specification requires of a
// malformed traceparent, it is ignored together with its tracestate rather
// than rejected; an unusable tracestate is dropped on its own.
func NormalizeTraceContext(traceparent, tracestate string) (parent, state string) {
	parsed, ok := ParseRemoteParent(traceparent, tracestate)
	if !ok {
		return "", ""
	}
	return "00-" + parsed.TraceID.String() + "-" + parsed.SpanID.String() + "-" + hex.EncodeToString([]byte{parsed.Flags}),
		parsed.State
}

// ParseRemoteParent reads W3C traceparent and tracestate header values.
func ParseRemoteParent(traceparent, tracestate string) (RemoteParent, bool) {
	traceparent = strings.TrimSpace(traceparent)
	// version "-" trace-id "-" parent-id "-" trace-flags
	const length = 2 + 1 + 32 + 1 + 16 + 1 + 2
	if len(traceparent) < length || traceparent[2] != '-' || traceparent[35] != '-' || traceparent[52] != '-' {
		return RemoteParent{}, false
	}
	var version, flags [1]byte
	var parent RemoteParent
	if !decodeLowerHex(version[:], traceparent[:2]) ||
		!decodeLowerHex(parent.TraceID[:], traceparent[3:35]) ||
		!decodeLowerHex(parent.SpanID[:], traceparent[36:52]) ||
		!decodeLowerHex(flags[:], traceparent[53:55]) {
		return RemoteParent{}, false
	}
	switch {
	case version[0] == 0xff:
		return RemoteParent{}, false
	case version[0] == 0 && len(traceparent) != length:
		return RemoteParent{}, false
	// A later version may append fields after the ones this version defines.
	case len(traceparent) > length && traceparent[length] != '-':
		return RemoteParent{}, false
	}
	if parent.TraceID.IsEmpty() || parent.SpanID.IsEmpty() {
		return RemoteParent{}, false
	}
	parent.Flags = flags[0]
	parent.State = normalizeTraceState(tracestate)
	return parent, true
}

func decodeLowerHex(target []byte, text string) bool {
	// The specification allows lowercase hex digits only.
	if strings.ToLower(text) != text {
		return false
	}
	_, err := hex.Decode(target, []byte(text))
	return err == nil
}

func normalizeTraceState(tracestate string) string {
	tracestate = strings.TrimSpace(tracestate)
	if len(tracestate) > maxTraceStateBytes {
		return ""
	}
	for i := range len(tracestate) {
		// List members are printable ASCII.
		if tracestate[i] < 0x20 || tracestate[i] > 0x7e {
			return ""
		}
	}
	return tracestate
}
