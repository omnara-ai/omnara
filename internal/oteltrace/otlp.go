// Package oteltrace renders one agent turn as an OpenTelemetry trace in the
// OTLP/JSON encoding, using the GenAI semantic conventions. It builds the wire
// payload directly and does not depend on the OpenTelemetry SDK: spans are
// reconstructed from stored rows with explicit IDs and timestamps.
package oteltrace

import (
	"strconv"
	"time"
)

// Span kinds and status codes from the OTLP trace protobuf.
const (
	SpanKindInternal = 1
	SpanKindClient   = 3

	StatusCodeOK    = 1
	StatusCodeError = 2
)

// Export is an OTLP ExportTraceServiceRequest in its JSON encoding.
type Export struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

type Resource struct {
	Attributes []KeyValue `json:"attributes"`
}

type ScopeSpans struct {
	Scope Scope  `json:"scope"`
	Spans []Span `json:"spans"`
}

type Scope struct {
	Name string `json:"name"`
}

// Span is one OTLP span. IDs are lowercase hex and timestamps are decimal
// strings of Unix nanoseconds, as OTLP/JSON requires.
type Span struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	ParentSpanID      string     `json:"parentSpanId,omitempty"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []KeyValue `json:"attributes"`
	Status            *Status    `json:"status,omitempty"`
}

type Status struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

// AnyValue holds exactly one value. 64-bit integers are strings in OTLP/JSON.
type AnyValue struct {
	StringValue *string     `json:"stringValue,omitempty"`
	IntValue    *string     `json:"intValue,omitempty"`
	ArrayValue  *ArrayValue `json:"arrayValue,omitempty"`
}

type ArrayValue struct {
	Values []AnyValue `json:"values"`
}

const (
	scopeName   = "omnara"
	serviceName = "omnara"
)

// NewExport wraps spans in the single resource and scope Omnara reports.
func NewExport(spans []Span) Export {
	if spans == nil {
		spans = []Span{}
	}
	return Export{ResourceSpans: []ResourceSpans{{
		Resource:   Resource{Attributes: []KeyValue{stringAttr("service.name", serviceName)}},
		ScopeSpans: []ScopeSpans{{Scope: Scope{Name: scopeName}, Spans: spans}},
	}}}
}

func stringAttr(key, value string) KeyValue {
	return KeyValue{Key: key, Value: AnyValue{StringValue: &value}}
}

func intAttr(key string, value int64) KeyValue {
	encoded := strconv.FormatInt(value, 10)
	return KeyValue{Key: key, Value: AnyValue{IntValue: &encoded}}
}

func stringsAttr(key string, values ...string) KeyValue {
	array := ArrayValue{Values: make([]AnyValue, len(values))}
	for i := range values {
		array.Values[i] = AnyValue{StringValue: &values[i]}
	}
	return KeyValue{Key: key, Value: AnyValue{ArrayValue: &array}}
}

func unixNano(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}
