package providererrors

import (
	"encoding/json"
	"testing"
)

func TestOpenRouterRawErrorMessageShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "plain string", raw: json.RawMessage(`"plain failure"`), want: "plain failure"},
		{name: "malformed-looking string", raw: json.RawMessage(`"{"`), want: "{"},
		{name: "JSON string", raw: json.RawMessage(`"{\"msg\":\"encoded failure\"}"`), want: "encoded failure"},
		{name: "message", raw: json.RawMessage(`{"message":"message failure"}`), want: "message failure"},
		{name: "error string", raw: json.RawMessage(`{"error":"error failure"}`), want: "error failure"},
		{name: "detail", raw: json.RawMessage(`{"detail":"detail failure"}`), want: "detail failure"},
		{name: "details", raw: json.RawMessage(`{"details":"details failure"}`), want: "details failure"},
		{name: "msg", raw: json.RawMessage(`{"msg":"msg failure"}`), want: "msg failure"},
		{name: "nested", raw: json.RawMessage(`{"error":{"detail":"nested failure"}}`), want: "nested failure"},
		{name: "unrecognized", raw: json.RawMessage(`{"status":"failed"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := openRouterRawErrorMessage(test.raw); got != test.want {
				t.Fatalf("raw provider message = %q, want %q", got, test.want)
			}
		})
	}
}

func openRouterEvidence(t *testing.T, fields openRouterError) OpenRouterError {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return OpenRouterMetadata(raw)
}
