package oteltrace

import (
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

func TestParseEncodingDefaultsToProtobuf(t *testing.T) {
	for value, want := range map[string]Encoding{
		"": EncodingProtobuf, "protobuf": EncodingProtobuf, "json": EncodingJSON,
	} {
		if got, ok := ParseEncoding(value); !ok || got != want {
			t.Errorf("ParseEncoding(%q) = %q, %v; want %q", value, got, ok, want)
		}
	}
	if _, ok := ParseEncoding("grpc"); ok {
		t.Error("grpc is not an OTLP/HTTP encoding")
	}
}

func TestEncodingsCarryTheSameSpans(t *testing.T) {
	traces := Build(testTurn(), VariantOTel)
	for encoding, test := range map[Encoding]struct {
		contentType string
		unmarshaler ptrace.Unmarshaler
	}{
		EncodingProtobuf: {"application/x-protobuf", &ptrace.ProtoUnmarshaler{}},
		EncodingJSON:     {"application/json", &ptrace.JSONUnmarshaler{}},
	} {
		body, contentType, err := Encode(traces, encoding)
		if err != nil || contentType != test.contentType {
			t.Fatalf("encode %s: %v, content type %q", encoding, err, contentType)
		}
		decoded, err := test.unmarshaler.UnmarshalTraces(body)
		if err != nil {
			t.Fatalf("decode %s: %v", encoding, err)
		}
		if got, want := mustProto(t, decoded), mustProto(t, traces); string(got) != string(want) {
			t.Errorf("%s did not round-trip the trace", encoding)
		}
	}
	body, _, err := Encode(traces, EncodingJSON)
	if err != nil || !strings.Contains(string(body), `"traceId":"01938a5c00007000800000000000abcd"`) ||
		!strings.Contains(string(body), `{"key":"gen_ai.usage.input_tokens","value":{"intValue":"100"}}`) {
		t.Errorf("OTLP/JSON writes IDs as hex and 64-bit integers as strings, got %s", body)
	}
	if _, _, err := Encode(traces, "grpc"); err == nil {
		t.Error("an unknown encoding must fail")
	}
}

func mustProto(t *testing.T, traces ptrace.Traces) []byte {
	t.Helper()
	body, err := (&ptrace.ProtoMarshaler{}).MarshalTraces(traces)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParsePartialSuccess(t *testing.T) {
	partial := ptraceotlp.NewExportResponse()
	partial.PartialSuccess().SetRejectedSpans(3)
	partial.PartialSuccess().SetErrorMessage("too big")
	encoded, err := partial.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, contentType string
		body              []byte
		rejected          int64
		unreadable        bool
	}{
		{"json string count", "application/json",
			[]byte(`{"partialSuccess":{"rejectedSpans":"3","errorMessage":"too big"}}`), 3, false},
		{"json number count", "application/json; charset=utf-8", []byte(`{"partialSuccess":{"rejectedSpans":3}}`), 3, false},
		{"json full success", "application/json", []byte(`{}`), 0, false},
		{"empty body", "", nil, 0, false},
		{"not json", "text/html", []byte(`<html>ok</html>`), 0, true},
		{"protobuf", "application/x-protobuf", encoded, 3, false},
		{"protobuf without content type", "", encoded, 3, false},
		{"protobuf full success", "application/x-protobuf", nil, 0, false},
		{"protobuf garbage", "application/x-protobuf", []byte{0xff, 0xff, 0xff}, 0, true},
	} {
		rejected, err := ParsePartialSuccess(test.contentType, EncodingProtobuf, test.body)
		if rejected != test.rejected || (err != nil) != test.unreadable {
			t.Errorf("%s: got %d %v, want %d unreadable=%t", test.name, rejected, err, test.rejected, test.unreadable)
		}
	}
}

func TestVariantsListTheirEncodingsDefaultFirst(t *testing.T) {
	for variant, want := range map[Variant][]Encoding{
		VariantOTel:       {EncodingProtobuf, EncodingJSON},
		VariantLangfuse:   {EncodingProtobuf, EncodingJSON},
		VariantBraintrust: {EncodingProtobuf},
		VariantLangSmith:  {EncodingProtobuf},
	} {
		if got := variant.Encodings(); !slices.Equal(got, want) {
			t.Errorf("%s encodings = %v, want %v", variant, got, want)
		}
	}
}
