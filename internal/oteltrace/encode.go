package oteltrace

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"strconv"
	"strings"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type Encoding string

const (
	EncodingJSON     Encoding = "json"
	EncodingProtobuf Encoding = "protobuf"
)

func ParseEncoding(value string) (Encoding, bool) {
	switch encoding := Encoding(value); encoding {
	case EncodingJSON, EncodingProtobuf:
		return encoding, true
	case "":
		return EncodingJSON, true
	default:
		return "", false
	}
}

func (export Export) Encode(encoding Encoding) (body []byte, contentType string, err error) {
	switch encoding {
	case EncodingProtobuf:
		body, err = export.marshalProto()
		return body, "application/x-protobuf", err
	case EncodingJSON, "":
		body, err = json.Marshal(export)
		return body, "application/json", err
	default:
		return nil, "", fmt.Errorf("unsupported otel encoding %q", encoding)
	}
}

// TracesData is wire-compatible with ExportTraceServiceRequest and avoids
// pulling in the gRPC service stubs.
func (export Export) marshalProto() ([]byte, error) {
	data := &tracepb.TracesData{ResourceSpans: make([]*tracepb.ResourceSpans, 0, len(export.ResourceSpans))}
	for _, resourceSpans := range export.ResourceSpans {
		encoded := &tracepb.ResourceSpans{
			Resource:   &resourcepb.Resource{Attributes: protoAttributes(resourceSpans.Resource.Attributes)},
			ScopeSpans: make([]*tracepb.ScopeSpans, 0, len(resourceSpans.ScopeSpans)),
		}
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			scope := &tracepb.ScopeSpans{
				Scope: &commonpb.InstrumentationScope{Name: scopeSpans.Scope.Name},
				Spans: make([]*tracepb.Span, 0, len(scopeSpans.Spans)),
			}
			for _, span := range scopeSpans.Spans {
				converted, err := protoSpan(span)
				if err != nil {
					return nil, err
				}
				scope.Spans = append(scope.Spans, converted)
			}
			encoded.ScopeSpans = append(encoded.ScopeSpans, scope)
		}
		data.ResourceSpans = append(data.ResourceSpans, encoded)
	}
	return proto.Marshal(data)
}

func protoSpan(span Span) (*tracepb.Span, error) {
	converted := &tracepb.Span{
		Name: span.Name, Kind: tracepb.Span_SpanKind(span.Kind), Attributes: protoAttributes(span.Attributes),
	}
	var err error
	if converted.TraceId, err = hex.DecodeString(span.TraceID); err != nil {
		return nil, fmt.Errorf("decode trace id: %w", err)
	}
	if converted.SpanId, err = hex.DecodeString(span.SpanID); err != nil {
		return nil, fmt.Errorf("decode span id: %w", err)
	}
	if converted.ParentSpanId, err = hex.DecodeString(span.ParentSpanID); err != nil {
		return nil, fmt.Errorf("decode parent span id: %w", err)
	}
	if converted.StartTimeUnixNano, err = strconv.ParseUint(span.StartTimeUnixNano, 10, 64); err != nil {
		return nil, fmt.Errorf("decode span start time: %w", err)
	}
	if converted.EndTimeUnixNano, err = strconv.ParseUint(span.EndTimeUnixNano, 10, 64); err != nil {
		return nil, fmt.Errorf("decode span end time: %w", err)
	}
	if span.Status != nil {
		converted.Status = &tracepb.Status{Code: tracepb.Status_StatusCode(span.Status.Code), Message: span.Status.Message}
	}
	return converted, nil
}

func protoAttributes(attributes []KeyValue) []*commonpb.KeyValue {
	converted := make([]*commonpb.KeyValue, len(attributes))
	for i, attribute := range attributes {
		converted[i] = &commonpb.KeyValue{Key: attribute.Key, Value: protoValue(attribute.Value)}
	}
	return converted
}

func protoValue(value AnyValue) *commonpb.AnyValue {
	switch {
	case value.StringValue != nil:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: *value.StringValue}}
	case value.IntValue != nil:
		parsed, _ := strconv.ParseInt(*value.IntValue, 10, 64)
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: parsed}}
	case value.BoolValue != nil:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: *value.BoolValue}}
	case value.ArrayValue != nil:
		array := &commonpb.ArrayValue{Values: make([]*commonpb.AnyValue, len(value.ArrayValue.Values))}
		for i, item := range value.ArrayValue.Values {
			array.Values[i] = protoValue(item)
		}
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: array}}
	default:
		return &commonpb.AnyValue{}
	}
}

// ParsePartialSuccess reads an OTLP export response and reports how many
// spans the endpoint rejected despite answering with success.
func ParsePartialSuccess(contentType string, body []byte) (rejectedSpans int64, message string) {
	if mediaType, _, _ := mime.ParseMediaType(contentType); mediaType == "application/x-protobuf" {
		// ExportTraceServiceResponse.partial_success is field 1; within it
		// rejected_spans is field 1 and error_message is field 2.
		partial, _ := protoField(body, 1, protowire.BytesType)
		rejected, _ := protoField(partial, 1, protowire.VarintType)
		text, _ := protoField(partial, 2, protowire.BytesType)
		count, _ := protowire.ConsumeVarint(rejected)
		return int64(count), string(text)
	}
	var response struct {
		PartialSuccess struct {
			// int64 is a string in OTLP/JSON, but some servers send a number.
			RejectedSpans json.RawMessage `json:"rejectedSpans"`
			ErrorMessage  string          `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	if json.Unmarshal(body, &response) != nil {
		return 0, ""
	}
	rejectedSpans, _ = strconv.ParseInt(strings.Trim(string(response.PartialSuccess.RejectedSpans), `"`), 10, 64)
	return rejectedSpans, response.PartialSuccess.ErrorMessage
}

// protoField returns the raw value of the last occurrence of one field.
func protoField(message []byte, field protowire.Number, kind protowire.Type) (value []byte, ok bool) {
	for len(message) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(message)
		if tagLength < 0 {
			return nil, false
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, message[tagLength:])
		if valueLength < 0 {
			return nil, false
		}
		if number == field && wireType == kind {
			value, ok = message[tagLength:tagLength+valueLength], true
			if kind == protowire.BytesType {
				value, _ = protowire.ConsumeBytes(value)
			}
		}
		message = message[tagLength+valueLength:]
	}
	return value, ok
}
