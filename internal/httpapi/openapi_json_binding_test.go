package httpapi

import (
	"context"
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
	// An open object schema exposes the decoder mismatch; closed schemas would
	// reject case variants before exercising the real generated body binder.
	allowExtra := true
	spec.Components.Schemas["CreateMemoryStore"].Value.AdditionalProperties.Has = &allowExtra

	for _, test := range []struct {
		name   string
		raw    string
		access string
		status int
	}{
		{name: "ordinary", raw: `{"name":"docs","agent_access":"read"}`, access: "read", status: http.StatusNoContent},
		{name: "case variants", raw: `{"name":"docs","NAME":"../invalid","agent_access":"read","AGENT_ACCESS":"write"}`, access: "read", status: http.StatusNoContent},
		{name: "case variant before member", raw: `{"NAME":"../invalid","AGENT_ACCESS":"write","name":"docs","agent_access":"read"}`, access: "read", status: http.StatusNoContent},
		{name: "extra properties", raw: `{"name":"docs","agent_access":"read","extra":{"note":"allowed"}}`, access: "read", status: http.StatusNoContent},
		{name: "omission", raw: `{"name":"docs"}`, status: http.StatusNoContent},
		{name: "case variant cannot supply omitted member", raw: `{"name":"docs","AGENT_ACCESS":"write"}`, status: http.StatusNoContent},
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
