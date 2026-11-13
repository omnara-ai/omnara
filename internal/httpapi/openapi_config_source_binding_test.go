package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestConfigSourceValidatorPreservesBytesWithClosedSchemas(t *testing.T) {
	validator, err := newOpenAPIRequestValidator()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	orgID := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	projectID := testPublicID(t, publicid.KindProject, httpTestProjectID)
	agentID := testPublicID(t, publicid.KindAgent, testHTTPID(42))
	root := openAPIBasePath + "/orgs/" + orgID + "/projects/" + projectID
	const source = `{ "model": {"provider_config":"openai-prod", "name":"gpt-test"},
 "instruction": "Keep \\u0061 formatting." }`
	const config = `{"source_format":"json","source":` + source + `}`
	for _, operation := range []struct {
		id, path, template string
		inline             bool
	}{
		{
			string(operationCreateAgentConfig), root + "/agent-configs",
			"/orgs/{orgID}/projects/{projectID}/agent-configs", false,
		},
		{string(operationCreateAgent), root + "/agents", "/orgs/{orgID}/projects/{projectID}/agents", true},
		{
			string(operationUpdateAgentConfig), root + "/agents/" + agentID + "/config",
			"/orgs/{orgID}/projects/{projectID}/agents/{agentID}/config", false,
		},
		{
			string(operationResolveAgentConfigTools), root + "/agent-configs/tools",
			"/orgs/{orgID}/projects/{projectID}/agent-configs/tools", false,
		},
	} {
		t.Run(operation.id, func(t *testing.T) {
			schema := spec.Paths.Value(operation.template).Post.RequestBody.Value.Content["application/json"].Schema.Value
			if schema.AdditionalProperties.Has == nil || *schema.AdditionalProperties.Has {
				t.Fatal("raw decoding requires a closed request schema")
			}
			if operation.inline {
				nested := schema.Properties["config_source"].Value
				if nested.AdditionalProperties.Has == nil || *nested.AdditionalProperties.Has {
					t.Fatal("raw decoding requires a closed inline config schema")
				}
			}
			wrap := func(raw string) string {
				if operation.inline {
					return `{"config_source":` + raw + `}`
				}
				return raw
			}
			for _, test := range []struct {
				name, body string
				status     int
			}{
				{"verbatim source", wrap(config), http.StatusNoContent},
				{"case variant", wrap(config[:len(config)-1] + `,"SOURCE_FORMAT":"yaml"}`), http.StatusBadRequest},
				{"source alias", wrap(config[:len(config)-1] + `,"SOURCE":{}}`), http.StatusBadRequest},
				{"duplicate", wrap(config[:len(config)-1] + `,"source_format":"yaml"}`), http.StatusBadRequest},
			} {
				t.Run(test.name, func(t *testing.T) {
					calls := 0
					strict := openapi.NewStrictHandlerWithOptions(nil, []openapi.StrictMiddlewareFunc{
						func(_ openapi.StrictHandlerFunc, opID string) openapi.StrictHandlerFunc {
							return func(_ context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
								calls++
								if opID != operation.id {
									t.Fatalf("operation=%s, want %s", opID, operation.id)
								}
								if _, retained := r.Body.(interface{ ValidatedValue() any }); retained {
									t.Fatal("config source operation retained a reserialized value")
								}
								var bound json.RawMessage
								switch request := request.(type) {
								case openapi.CreateAgentConfigRequestObject:
									bound = request.Body.Source
								case openapi.CreateAgentRequestObject:
									bound = request.Body.ConfigSource.Source
								case openapi.UpdateAgentConfigRequestObject:
									bound = request.Body.Source
								case openapi.ResolveAgentConfigToolsRequestObject:
									bound = request.Body.Source
								default:
									t.Fatalf("unexpected request type: %T", request)
								}
								if string(bound) != source {
									t.Fatalf("source=%s, want %s", bound, source)
								}
								w.WriteHeader(http.StatusNoContent)
								return nil, nil
							}
						},
					}, openapi.StrictHTTPServerOptions{
						RequestErrorHandlerFunc:  openAPIRequestErrorHandler,
						ResponseErrorHandlerFunc: writeAPIError,
					})
					generated := openapi.HandlerWithOptions(strict, openapi.StdHTTPServerOptions{BaseURL: openAPIBasePath})
					req := httptest.NewRequest(http.MethodPost, operation.path, strings.NewReader(test.body))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					validator(generated).ServeHTTP(rec, req)
					wantCalls := 0
					if test.status == http.StatusNoContent {
						wantCalls = 1
					}
					if rec.Code != test.status || calls != wantCalls {
						t.Fatalf("status=%d calls=%d body=%s", rec.Code, calls, rec.Body.String())
					}
				})
			}
		})
	}
}
