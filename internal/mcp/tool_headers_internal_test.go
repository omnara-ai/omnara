package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolHeadersCollectsStaticallyReachableAnnotations(t *testing.T) {
	tool := &sdkmcp.Tool{Name: "execute_sql", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
			"query":  map[string]any{"type": "string"},
			"options": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "x-mcp-header": "Limit"},
				},
			},
		},
	}}
	headers, err := ToolHeaders(tool)
	if err != nil {
		t.Fatalf("tool headers: %v", err)
	}
	got := map[string]string{}
	for _, header := range headers {
		got[header.Name] = strings.Join(header.Path, ".")
	}
	if len(got) != 2 || got["Region"] != "region" || got["Limit"] != "options.limit" {
		t.Fatalf("headers = %v", got)
	}
}

func TestToolHeadersRejectsInvalidAnnotations(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{
			name: "empty name",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"region": map[string]any{"type": "string", "x-mcp-header": ""},
			}},
			want: "non-empty string",
		},
		{
			name: "invalid token",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"region": map[string]any{"type": "string", "x-mcp-header": "Reg ion"},
			}},
			want: "not a valid header token",
		},
		{
			name: "duplicate case-insensitive",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"a": map[string]any{"type": "string", "x-mcp-header": "Region"},
				"b": map[string]any{"type": "string", "x-mcp-header": "region"},
			}},
			want: "more than once",
		},
		{
			name: "number type",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"ratio": map[string]any{"type": "number", "x-mcp-header": "Ratio"},
			}},
			want: "unsupported type",
		},
		{
			name: "inside items",
			schema: map[string]any{"type": "object", "properties": map[string]any{
				"regions": map[string]any{"type": "array", "items": map[string]any{
					"type": "string", "x-mcp-header": "Region",
				}},
			}},
			want: "not statically reachable",
		},
		{
			name: "inside oneOf",
			schema: map[string]any{"type": "object", "oneOf": []any{
				map[string]any{"properties": map[string]any{
					"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
				}},
			}},
			want: "not statically reachable",
		},
		{
			name:   "on root",
			schema: map[string]any{"type": "object", "x-mcp-header": "Root"},
			want:   "not the schema root",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ToolHeaders(&sdkmcp.Tool{Name: "tool", InputSchema: tt.schema})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestToolHeaderValues(t *testing.T) {
	headers := []ToolHeader{
		{Name: "Region", Path: []string{"region"}},
		{Name: "Limit", Path: []string{"options", "limit"}},
		{Name: "Flag", Path: []string{"flag"}},
		{Name: "Null", Path: []string{"nothing"}},
		{Name: "Absent", Path: []string{"missing"}},
	}
	arguments := json.RawMessage(`{"region":"eu","options":{"limit":9007199254740991},"flag":false,"nothing":null}`)
	values, err := toolHeaderValues(headers, arguments)
	if err != nil {
		t.Fatalf("header values: %v", err)
	}
	if len(values) != 3 || values["Region"] != "eu" || values["Limit"] != "9007199254740991" || values["Flag"] != "false" {
		t.Fatalf("values = %v", values)
	}
	if _, err := toolHeaderValues(headers, json.RawMessage(`{"region":{"nested":true}}`)); err == nil {
		t.Fatal("expected object value to be rejected")
	}
	if _, err := toolHeaderValues(headers, json.RawMessage(`{"options":{"limit":1.5}}`)); err == nil {
		t.Fatal("expected fractional value to be rejected")
	}
	if _, err := toolHeaderValues(headers, json.RawMessage(`{"options":{"limit":9007199254740992}}`)); err == nil {
		t.Fatal("expected unsafe integer to be rejected")
	}
}

func TestEncodeHeaderValue(t *testing.T) {
	tests := map[string]string{
		"us-west1":           "us-west1",
		"Hello, 世界":          "=?base64?SGVsbG8sIOS4lueVjA==?=",
		" padded ":           "=?base64?IHBhZGRlZCA=?=",
		"line1\nline2":       "=?base64?bGluZTEKbGluZTI=?=",
		"=?base64?literal?=": "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?=",
		"with space inside":  "with space inside",
		"":                   "=?base64??=",
	}
	for input, want := range tests {
		if got := encodeHeaderValue(input); got != want {
			t.Errorf("encodeHeaderValue(%q) = %q, want %q", input, got, want)
		}
	}
}
