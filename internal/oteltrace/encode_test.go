package oteltrace

import (
	"encoding/hex"
	"google.golang.org/protobuf/encoding/protowire"
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

func TestParsePartialSuccess(t *testing.T) {
	// partial_success{rejected_spans: 3, error_message: "too big"}
	inner := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), 3)
	inner = protowire.AppendString(protowire.AppendTag(inner, 2, protowire.BytesType), "too big")
	encoded := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), inner)
	for _, test := range []struct {
		name, contentType string
		body              []byte
		rejected          int64
		message           string
	}{
		{"json string count", "application/json",
			[]byte(`{"partialSuccess":{"rejectedSpans":"3","errorMessage":"too big"}}`), 3, "too big"},
		{"json number count", "application/json; charset=utf-8", []byte(`{"partialSuccess":{"rejectedSpans":3}}`), 3, ""},
		{"json full success", "application/json", []byte(`{}`), 0, ""},
		{"empty body", "", nil, 0, ""},
		{"not json", "text/html", []byte(`<html>ok</html>`), 0, ""},
		{"protobuf", "application/x-protobuf", encoded, 3, "too big"},
		{"protobuf full success", "application/x-protobuf", nil, 0, ""},
		{"protobuf garbage", "application/x-protobuf", []byte{0xff, 0xff, 0xff}, 0, ""},
	} {
		rejected, message := ParsePartialSuccess(test.contentType, test.body)
		if rejected != test.rejected || message != test.message {
			t.Errorf("%s: got %d %q, want %d %q", test.name, rejected, message, test.rejected, test.message)
		}
	}
}
