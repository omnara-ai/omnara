package providers

import (
	"encoding/json"
	"testing"
)

func TestDecodeStrictJSONRejectsTrailingValue(t *testing.T) {
	var value struct {
		Name string `json:"name"`
	}
	if err := DecodeStrictJSON(json.RawMessage(`{"name":"value"} {}`), &value); err == nil {
		t.Fatal("expected trailing JSON value to fail")
	}
}
