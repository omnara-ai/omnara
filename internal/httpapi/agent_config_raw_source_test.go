package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestAgentConfigSourceUsesExactOriginalField(t *testing.T) {
	const source = `{ "model": {"name":"gpt-test", "provider_config":"openai-prod"},
  "instruction": "Preserve \\u0061 formatting.", "tool_default": 9007199254740993 }`
	const normalized = `{"instruction":"normalized"}`
	for _, test := range []struct {
		name      string
		body      string
		container []string
		fallback  json.RawMessage
		want      string
	}{
		{
			name:     "root source",
			body:     `{"source_format":"json","source":` + source + `,"SOURCE":{"instruction":"wrong"}}`,
			fallback: json.RawMessage(normalized),
			want:     source,
		},
		{
			name: "inline source",
			body: `{"config_source":{"source_format":"json","source":` + source + `},` +
				`"CONFIG_SOURCE":{"source":{"instruction":"wrong"}}}`,
			container: []string{"config_source"},
			fallback:  json.RawMessage(normalized),
			want:      source,
		},
		{
			name: "case variant cannot supply source",
			body: `{"SOURCE":` + source + `}`,
		},
		{
			name:      "case variant cannot supply inline source",
			body:      `{"CONFIG_SOURCE":{"source":` + source + `}}`,
			container: []string{"config_source"},
		},
		{
			name:     "without HTTP source context",
			fallback: json.RawMessage(normalized),
			want:     normalized,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.body != "" {
				ctx = context.WithValue(ctx, originalJSONRequestBodyKey{}, []byte(test.body))
			}
			got, err := originalAgentConfigSource(ctx, test.fallback, test.container...)
			if err != nil || string(got) != test.want {
				t.Fatalf("source=%s err=%v, want %s", got, err, test.want)
			}
		})
	}
}

func TestAgentConfigSourceFormatComesFromValidatedInput(t *testing.T) {
	const source = `{ "instruction": "Keep formatting" }`
	ctx := context.WithValue(t.Context(), originalJSONRequestBodyKey{},
		[]byte(`{"source_format":"yaml","source":`+source+`}`))
	format, text, err := agentConfigSourceInput(ctx, "json", nil)
	if err != nil || format != agentconfig.SourceFormatJSON || text != source {
		t.Fatalf("format=%s source=%s err=%v", format, text, err)
	}
}

func TestOpenAPIRequestValidatorCapturesOnlyConfigSourceBodies(t *testing.T) {
	validator, err := newOpenAPIRequestValidator()
	if err != nil {
		t.Fatal(err)
	}
	orgID := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	projectID := testPublicID(t, publicid.KindProject, httpTestProjectID)
	agentID := testPublicID(t, publicid.KindAgent, testHTTPID(42))
	root := openAPIBasePath + "/orgs/" + orgID + "/projects/" + projectID
	const config = `{"source_format":"json","source":{ "instruction":"Keep source bytes" }}`
	for _, test := range []struct {
		method string
		path   string
		body   string
		keep   bool
	}{
		{http.MethodPost, root + "/agent-configs", config, true},
		{http.MethodPost, root + "/agents", `{"config_source":` + config + `}`, true},
		{http.MethodPost, root + "/agents/" + agentID + "/config", config, true},
		{http.MethodPost, root + "/agent-configs/tools", config, true},
		{http.MethodPost, root + "/memory-stores", `{"name":"docs"}`, false},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			handler := validator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, ok := r.Context().Value(originalJSONRequestBodyKey{}).([]byte)
				if ok != test.keep || (ok && string(raw) != test.body) {
					t.Fatalf("captured=%t body=%s", ok, raw)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
