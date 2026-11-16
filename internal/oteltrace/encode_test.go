package oteltrace

import (
	"encoding/hex"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestEncodeProtobufCarriesTheSameSpansAsJSON(t *testing.T) {
	turn := testTurn()
	spans := Build(turn, VariantOTel)
	body, contentType, err := NewExport(spans).Encode(EncodingProtobuf)
	if err != nil || contentType != "application/x-protobuf" {
		t.Fatalf("encode protobuf: %v, content type %q", err, contentType)
	}
	var decoded tracepb.TracesData
	if err := proto.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	resource := decoded.GetResourceSpans()[0]
	if got := resource.GetResource().GetAttributes()[0]; got.GetKey() != "service.name" ||
		got.GetValue().GetStringValue() != "omnara" {
		t.Fatalf("unexpected resource attribute: %v", got)
	}
	got := resource.GetScopeSpans()[0].GetSpans()
	if len(got) != len(spans) {
		t.Fatalf("expected %d spans, got %d", len(spans), len(got))
	}
	for i, span := range spans {
		if hex.EncodeToString(got[i].GetTraceId()) != span.TraceID || hex.EncodeToString(got[i].GetSpanId()) != span.SpanID ||
			hex.EncodeToString(got[i].GetParentSpanId()) != span.ParentSpanID || got[i].GetName() != span.Name ||
			int(got[i].GetKind()) != span.Kind || len(got[i].GetAttributes()) != len(span.Attributes) {
			t.Fatalf("span %d differs: %v", i, got[i])
		}
	}
	root, failed, chat := got[0], got[1], got[2]
	if len(root.GetTraceId()) != 16 || len(root.GetSpanId()) != 8 || len(root.GetParentSpanId()) != 0 {
		t.Fatalf("unexpected root identity sizes: %v", root)
	}
	if root.GetStartTimeUnixNano() != 1794830400000000000 || root.GetEndTimeUnixNano() != 1794830408000000000 {
		t.Fatalf("unexpected root times: %d to %d", root.GetStartTimeUnixNano(), root.GetEndTimeUnixNano())
	}
	if status := failed.GetStatus(); status.GetCode() != tracepb.Status_STATUS_CODE_ERROR ||
		status.GetMessage() != "overloaded" {
		t.Fatalf("unexpected failed status: %v", failed.GetStatus())
	}
	var inputTokens int64
	var finishReasons []string
	for _, attribute := range chat.GetAttributes() {
		switch attribute.GetKey() {
		case "gen_ai.usage.input_tokens":
			inputTokens = attribute.GetValue().GetIntValue()
		case "gen_ai.response.finish_reasons":
			for _, value := range attribute.GetValue().GetArrayValue().GetValues() {
				finishReasons = append(finishReasons, value.GetStringValue())
			}
		}
	}
	if inputTokens != 100 || len(finishReasons) != 1 || finishReasons[0] != "tool_use" {
		t.Fatalf("unexpected typed attributes: %d %v", inputTokens, finishReasons)
	}
}

func TestEncodeJSONAndParseEncoding(t *testing.T) {
	body, contentType, err := NewExport(nil).Encode(EncodingJSON)
	if err != nil || contentType != "application/json" || len(body) == 0 || body[0] != '{' {
		t.Fatalf("encode json: %v %q %s", err, contentType, body)
	}
	if _, _, err := NewExport(nil).Encode("msgpack"); err == nil {
		t.Fatal("unknown encoding must fail")
	}
	if encoding, ok := ParseEncoding(""); !ok || encoding != EncodingJSON {
		t.Fatalf("empty encoding should default to json, got %q %v", encoding, ok)
	}
	if _, ok := ParseEncoding("grpc"); ok {
		t.Fatal("unknown encoding must be rejected")
	}
}
