package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestWriteOpenAPIRequestErrorClassifiesOversizedBody(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    openapi.ErrorCode
		wantMessage string
	}{
		{
			name:        "oversized body",
			err:         &http.MaxBytesError{Limit: 1048576},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    openapi.ErrorCodeRequestTooLarge,
			wantMessage: "request too large",
		},
		{
			name:        "wrapped oversized body",
			err:         fmt.Errorf("decode request body: %w", &http.MaxBytesError{Limit: 1048576}),
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    openapi.ErrorCodeRequestTooLarge,
			wantMessage: "request too large",
		},
		{
			name:        "malformed body",
			err:         fmt.Errorf("field is required"),
			wantStatus:  http.StatusBadRequest,
			wantCode:    openapi.ErrorCodeValidationFailed,
			wantMessage: "validation failed: field is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
			writeOpenAPIRequestError(recorder, req, tt.err)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			var response openapi.Error
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != tt.wantCode || response.Error != tt.wantMessage {
				t.Fatalf("response = %+v, want code=%q message=%q", response, tt.wantCode, tt.wantMessage)
			}
		})
	}
}

func TestOpenAPIRequestValidatorKeepsValidatedJSON(t *testing.T) {
	validator, err := newOpenAPIRequestValidator()
	if err != nil {
		t.Fatal(err)
	}
	const raw = `{"name":"docs"}`
	handler := validator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := r.Body.(interface{ ValidatedValue() any })
		if !ok {
			t.Fatal("validator discarded the validated JSON value")
		}
		value, ok := body.ValidatedValue().(map[string]any)
		if !ok || len(value) != 1 || value["name"] != "docs" {
			t.Fatalf("validated value changed or acquired omission defaults: %#v", body.ValidatedValue())
		}
		readable, err := io.ReadAll(r.Body)
		if err != nil || string(readable) != raw {
			t.Fatalf("request body changed: %q, %v", readable, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, memoryStoreBindingPath(t), strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStrictJSONBodyBindsOnlyValidatedMembers(t *testing.T) {
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = openapi3.Servers{{URL: openAPIBasePath}}
	allowExtra := true
	spec.Components.Schemas["CreateMemoryStore"].Value.AdditionalProperties.Has = &allowExtra

	for _, test := range []struct {
		name   string
		raw    string
		access string
		status int
	}{
		{name: "ordinary", raw: `{"name":"docs","agent_access":"read"}`, access: "read", status: http.StatusNoContent},
		{
			name:   "case variants",
			raw:    `{"name":"docs","NAME":"../invalid","agent_access":"read","AGENT_ACCESS":"write"}`,
			access: "read",
			status: http.StatusNoContent,
		},
		{
			name:   "case variant before member",
			raw:    `{"NAME":"../invalid","AGENT_ACCESS":"write","name":"docs","agent_access":"read"}`,
			access: "read",
			status: http.StatusNoContent,
		},
		{
			name:   "extra properties",
			raw:    `{"name":"docs","agent_access":"read","extra":{"note":"allowed"}}`,
			access: "read",
			status: http.StatusNoContent,
		},
		{name: "omission", raw: `{"name":"docs"}`, status: http.StatusNoContent},
		{
			name:   "case variant cannot supply omitted member",
			raw:    `{"name":"docs","AGENT_ACCESS":"write"}`,
			status: http.StatusNoContent,
		},
		{name: "invalid canonical name", raw: `{"name":"../invalid"}`, status: http.StatusBadRequest},
		{name: "invalid canonical access", raw: `{"name":"docs","agent_access":"write"}`, status: http.StatusBadRequest},
		{name: "explicit null", raw: `{"name":"docs","agent_access":null}`, status: http.StatusBadRequest},
		{name: "duplicate members", raw: `{"name":"docs","name":"other"}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			strict := openapi.NewStrictHandlerWithOptions(nil, []openapi.StrictMiddlewareFunc{
				func(_ openapi.StrictHandlerFunc, operationID string) openapi.StrictHandlerFunc {
					return func(_ context.Context, w http.ResponseWriter, _ *http.Request, request any) (any, error) {
						calls++
						bound, ok := request.(openapi.CreateMemoryStoreRequestObject)
						if operationID != "CreateMemoryStore" || !ok || bound.Body == nil {
							t.Fatalf("unexpected operation or request: %s, %#v", operationID, request)
						}
						if bound.Body.Name != "docs" {
							t.Fatalf("unvalidated name reached the handler: %q", bound.Body.Name)
						}
						if test.access == "" {
							if bound.Body.AgentAccess != nil {
								t.Fatalf("omitted access was supplied by an unvalidated member or default: %q", *bound.Body.AgentAccess)
							}
						} else if bound.Body.AgentAccess == nil || string(*bound.Body.AgentAccess) != test.access {
							t.Fatalf("unvalidated access reached the handler: %v", bound.Body.AgentAccess)
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
			validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
				Options: openapi3filter.Options{
					AuthenticationFunc:  openapi3filter.NoopAuthenticationFunc,
					SkipSettingDefaults: true,
				},
				SilenceServersWarning: true,
			})
			req := httptest.NewRequest(http.MethodPost, memoryStoreBindingPath(t), strings.NewReader(test.raw))
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
}

func memoryStoreBindingPath(t *testing.T) string {
	t.Helper()
	orgID := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	projectID := testPublicID(t, publicid.KindProject, httpTestProjectID)
	return openAPIBasePath + "/orgs/" + orgID + "/projects/" + projectID + "/memory-stores"
}

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
