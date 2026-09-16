package channelconnector

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNormalizeOpaqueObject(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     json.RawMessage
		want    string
		wantErr bool
	}{
		{name: "empty", want: `{}`},
		{name: "canonical object", raw: json.RawMessage(`{"z":1,"a":"ok"}`), want: `{"a":"ok","z":1}`},
		{name: "non-object", raw: json.RawMessage(`[]`), wantErr: true},
		{name: "null", raw: json.RawMessage(`null`), wantErr: true},
		{name: "duplicate key", raw: json.RawMessage(`{"x":1,"x":2}`), wantErr: true},
		{name: "nested escaped duplicate", raw: json.RawMessage(`{"a":[{"x":1,"\u0078":2}]}`), wantErr: true},
		{name: "invalid UTF-8", raw: json.RawMessage{'{', '"', 'x', '"', ':', '"', 255, '"', '}'}, wantErr: true},
		{name: "exact integer", raw: json.RawMessage(`{"x":9007199254740993}`), want: `{"x":9007199254740993}`},
		{name: "NUL key", raw: json.RawMessage(`{"bad\u0000key":true}`), wantErr: true},
		{name: "NUL value", raw: json.RawMessage(`{"value":"bad\u0000value"}`), wantErr: true},
		{name: "PostgreSQL numeric overflow", raw: json.RawMessage(`{"value":1e1000000}`), wantErr: true},
		{
			name:    "oversized",
			raw:     append(append([]byte(`{"value":"`), bytes.Repeat([]byte{'a'}, MaxMetadataBytes)...), []byte(`"}`)...),
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeOpaqueObject(test.raw)
			if (err != nil) != test.wantErr {
				t.Fatalf("NormalizeOpaqueObject() error = %v, wantErr %t", err, test.wantErr)
			}
			if !test.wantErr && string(got) != test.want {
				t.Fatalf("NormalizeOpaqueObject() = %s, want %s", got, test.want)
			}
		})
	}
}
