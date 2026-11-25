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
// spans the endpoint rejected despite answering with success. An empty body
// acknowledges every span. A response without a content type is read in the
// encoding the request was sent in. The response's error message is left
// unread: an endpoint may echo span content in it.
func ParsePartialSuccess(contentType string, requestEncoding Encoding, body []byte) (rejectedSpans int64, err error) {
	if len(body) == 0 {
		return 0, nil
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "" && requestEncoding == EncodingProtobuf {
		mediaType = contentTypeProtobuf
	}
	response := ptraceotlp.NewExportResponse()
	if mediaType == contentTypeProtobuf {
		err = response.UnmarshalProto(body)
	} else {
		err = response.UnmarshalJSON(body)
	}
	if err != nil {
		return 0, fmt.Errorf("decode otel export response: %w", err)
	}
	return response.PartialSuccess().RejectedSpans(), nil
}
