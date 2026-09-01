package httpjson

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteSetsJSONContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, http.StatusCreated, map[string]string{"ok": "yes"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["ok"] != "yes" {
		t.Fatalf("body = %#v", body)
	}
}

func TestDecodeStrictRequiredBytesRejectsUnknownField(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	err := DecodeStrictRequiredBytes([]byte(`{"name":"omnara","extra":true}`), &dst)
	if err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func TestDecodeStrictRequiredBytesRejectsTrailingValue(t *testing.T) {
	var dst struct {
		Name string `json:"name"`
	}
	err := DecodeStrictRequiredBytes([]byte(`{"name":"omnara"} true`), &dst)
	if err == nil || !strings.Contains(err.Error(), "single JSON value") {
		t.Fatalf("err = %v, want single JSON value", err)
	}
}

func TestDecodeAllowedRawEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
	raw, err := DecodeAllowedRaw(req, map[string]bool{"name": true}, nil)
	if err != nil {
		t.Fatalf("DecodeAllowedRaw: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("raw = %#v, want empty", raw)
	}
}

func TestDecodeAllowedRawRejectsPathField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"id":"agt_1"}`))
	_, err := DecodeAllowedRaw(req, map[string]bool{"name": true}, map[string]bool{"id": true})
	if err == nil || !strings.Contains(err.Error(), "belongs in the request path") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeAllowedRawRejectsUnknownField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"nope":1}`))
	_, err := DecodeAllowedRaw(req, map[string]bool{"name": true}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown field: nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeAllowedRawRequiresObjectField(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"meta":[]}`))
	_, err := DecodeAllowedRaw(req, map[string]bool{"meta": true}, nil, "meta")
	if err == nil || err.Error() != "meta must be a JSON object" {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeAllowedRawRejectsNonObjectBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`[]`))
	_, err := DecodeAllowedRaw(req, map[string]bool{"name": true}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
