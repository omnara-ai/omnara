package oteltrace

import (
	"fmt"
	"mime"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

type Encoding string

const (
	EncodingProtobuf Encoding = "protobuf"
	EncodingJSON     Encoding = "json"

	contentTypeProtobuf = "application/x-protobuf"
	contentTypeJSON     = "application/json"
)

// ParseEncoding defaults to protobuf, the encoding every OTLP/HTTP endpoint
// accepts.
func ParseEncoding(value string) (Encoding, bool) {
	switch encoding := Encoding(value); encoding {
	case EncodingProtobuf, EncodingJSON:
		return encoding, true
	case "":
		return EncodingProtobuf, true
	default:
		return "", false
	}
}

// Encodings lists the OTLP/HTTP encodings the variant's backend documents for
// traces, its default first. Braintrust and LangSmith document protobuf only.
func (variant Variant) Encodings() []Encoding {
	switch variant {
	case VariantBraintrust, VariantLangSmith:
		return []Encoding{EncodingProtobuf}
	default:
		return []Encoding{EncodingProtobuf, EncodingJSON}
	}
}

// Encode renders the traces as an OTLP/HTTP export request body.
func Encode(traces ptrace.Traces, encoding Encoding) (body []byte, contentType string, err error) {
	switch encoding {
	case EncodingProtobuf:
		body, err = (&ptrace.ProtoMarshaler{}).MarshalTraces(traces)
		return body, contentTypeProtobuf, err
	case EncodingJSON:
		body, err = (&ptrace.JSONMarshaler{}).MarshalTraces(traces)
		return body, contentTypeJSON, err
	default:
		return nil, "", fmt.Errorf("unsupported otel encoding %q", encoding)
	}
}

// ParsePartialSuccess reads an OTLP export response and reports how many
// spans the endpoint rejected despite answering with success.
func ParsePartialSuccess(contentType string, body []byte) (rejectedSpans int64, message string) {
	response := ptraceotlp.NewExportResponse()
	var err error
	if mediaType, _, _ := mime.ParseMediaType(contentType); mediaType == contentTypeProtobuf {
		err = response.UnmarshalProto(body)
	} else {
		err = response.UnmarshalJSON(body)
	}
	if err != nil {
		return 0, ""
	}
	return response.PartialSuccess().RejectedSpans(), response.PartialSuccess().ErrorMessage()
}
