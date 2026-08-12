package httpapi

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"gopkg.in/yaml.v3"
)

func TestNoManualV1RouteRegistration(t *testing.T) {
	var manualRoutes []string
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == "openapi" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "HandleFunc" && selector.Sel.Name != "Handle") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				manualRoutes = append(manualRoutes, path+": dynamic route pattern")
				return true
			}
			pattern := strings.Trim(literal.Value, `"`)
			routePath := pattern
			if _, path, ok := strings.Cut(pattern, " "); ok {
				routePath = path
			}
			if strings.HasPrefix(routePath, "/api/v1/") {
				manualRoutes = append(manualRoutes, path+": "+pattern)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan route registrations: %v", err)
	}
	if len(manualRoutes) > 0 {
		t.Fatalf(
			"/api/v1 routes must be registered by generated OpenAPI code, found manual registrations:\n%s",
			strings.Join(manualRoutes, "\n"),
		)
	}
}

func TestGeneratedOpenAPISpecMatchesServedSpec(t *testing.T) {
	generated, err := openapi.GetSpec()
	if err != nil {
		t.Fatalf("load generated openapi spec: %v", err)
	}
	if len(generated.Servers) != 0 {
		t.Fatal("OpenAPI paths include the public /api/v1 prefix, so servers must not define a second base URL")
	}

	served := httptest.NewRecorder()
	(&Server{}).openapiYAMLRoute(served, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
	if served.Code != http.StatusOK {
		t.Fatalf("GET /api/openapi.yaml status = %d, want %d", served.Code, http.StatusOK)
	}
	if !bytes.Equal(served.Body.Bytes(), openapispec.YAML) {
		t.Fatal("GET /api/openapi.yaml does not serve api/openapi/openapi.yaml")
	}

	var checkedIn struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(openapispec.YAML, &checkedIn); err != nil {
		t.Fatalf("parse checked-in openapi spec: %v", err)
	}
	var generatedPaths []string
	for path := range generated.Paths.Map() {
		if !strings.HasPrefix(path, "/api/v1/") {
			t.Fatalf("generated OpenAPI path %q must include the public /api/v1 prefix", path)
		}
		generatedPaths = append(generatedPaths, path)
	}
	var checkedInPaths []string
	for path := range checkedIn.Paths {
		if !strings.HasPrefix(path, "/api/v1/") {
			t.Fatalf("checked-in OpenAPI path %q must include the public /api/v1 prefix", path)
		}
		checkedInPaths = append(checkedInPaths, path)
	}
	slices.Sort(generatedPaths)
	slices.Sort(checkedInPaths)
	if !slices.Equal(generatedPaths, checkedInPaths) {
		t.Fatalf(
			"generated OpenAPI paths differ from checked-in spec\n generated: %s\nchecked-in: %s",
			strings.Join(generatedPaths, ", "),
			strings.Join(checkedInPaths, ", "),
		)
	}
}

func TestOpenAPISpecialRouteContracts(t *testing.T) {
	var doc struct {
		Security   []any          `yaml:"security"`
		Paths      map[string]any `yaml:"paths"`
		Components struct {
			SecuritySchemes map[string]any `yaml:"securitySchemes"`
			Schemas         map[string]struct {
				Pattern              string         `yaml:"pattern"`
				Required             []string       `yaml:"required"`
				Properties           map[string]any `yaml:"properties"`
				AdditionalProperties any            `yaml:"additionalProperties"`
				OneOf                []any          `yaml:"oneOf"`
				Discriminator        *struct {
					PropertyName string `yaml:"propertyName"`
				} `yaml:"discriminator"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapispec.YAML, &doc); err != nil {
		t.Fatalf("parse checked-in openapi spec: %v", err)
	}

	if got := doc.Components.Schemas["ProjectID"].Pattern; got != `^proj_[a-z2-7]{26}$` {
		t.Fatalf("ProjectID pattern = %q, want proj_ public ID prefix", got)
	}
	if _, ok := doc.Components.SecuritySchemes["machineDaemonAuth"]; !ok {
		t.Fatal("machineDaemonAuth security scheme is required for daemon routes")
	}
	if !openAPISecurityHasRequirement(doc.Security, "bearerAuth") ||
		!openAPISecurityHasRequirement(doc.Security, "browserSessionCookie") {
		t.Fatalf("global OpenAPI security must document bearer and browser session auth, got %+v", doc.Security)
	}
	for _, schemaName := range []string{"RegisterDaemonRuntimeRequest", "DaemonRuntime"} {
		property := openAPIPropertySchema(t, doc.Components.Schemas[schemaName].Properties, "daemon_instance_id")
		if property["format"] != "uuid" {
			t.Fatalf("%s daemon_instance_id format = %v, want uuid", schemaName, property["format"])
		}
	}

	stream := openAPIResponseContent(
		t,
		doc.Paths,
		"/api/v1/orgs/{orgID}/projects/{projectID}/agents/{agentID}/events/stream",
		"get",
		"200",
	)
	streamContent, ok := stream["text/event-stream"].(map[string]any)
	if !ok {
		t.Fatal("event stream response must document text/event-stream")
	}
	if _, ok := stream["application/json"]; ok {
		t.Fatal("event stream response must not be documented as application/json")
	}
	streamSchema, ok := streamContent["schema"].(map[string]any)
	if !ok || streamSchema["$ref"] != "#/components/schemas/AgentEventStreamData" {
		t.Fatalf("event stream must use the typed frame schema, got %+v", streamContent)
	}
	assertJSONResponseSchemaRef(
		t,
		doc.Paths,
		"/api/v1/orgs/{orgID}/projects/{projectID}/agents/{agentID}/events",
		"get",
		"ListAgentEventsResponse",
	)
	assertJSONResponseSchemaRef(
		t,
		doc.Paths,
		"/api/v1/orgs/{orgID}/projects/{projectID}/agents/{agentID}/turns/{turnID}/events",
		"get",
		"ListTurnEventsResponse",
	)
	assertJSONResponseSchemaRef(
		t,
		doc.Paths,
		"/api/v1/orgs/{orgID}/projects/{projectID}/agents/{agentID}/cancel",
		"post",
		"CancelAgentResponse",
	)
	agentEvent := doc.Components.Schemas["AgentEvent"]
	if len(agentEvent.OneOf) != 4 || agentEvent.Discriminator == nil ||
		agentEvent.Discriminator.PropertyName != "event_kind" {
		t.Fatalf("AgentEvent must be a four-variant event_kind union: %+v", agentEvent)
	}
	for _, name := range []string{"AgentInputEvent", "ModelOutputEvent", "ToolResultEvent"} {
		variant := doc.Components.Schemas[name]
		if got, ok := variant.AdditionalProperties.(bool); !ok || got {
			t.Fatalf("%s schema must reject fields from other event variants", name)
		}
	}
	if _, ok := doc.Components.Schemas["ModelOutputEvent"].Properties["model_call_context_id"]; !ok {
		t.Fatal("ModelOutputEvent must document model_call_context_id")
	}
	if _, ok := doc.Components.Schemas["AgentInputEvent"].Properties["model_call_context_id"]; ok {
		t.Fatal("AgentInputEvent must not expose model_call_context_id")
	}
	cancelResponse := doc.Components.Schemas["CancelAgentResponse"]
	if !slices.Equal(cancelResponse.Required, []string{"event", "runtime_cancel_requested", "affected"}) {
		t.Fatalf(
			"CancelAgentResponse required = %v, want event/runtime_cancel_requested/affected",
			cancelResponse.Required,
		)
	}

	socketResponses := openAPIResponses(
		t,
		doc.Paths,
		"/api/v1/daemon/runtimes/{runtimeID}/socket",
		"get",
	)
	if _, ok := socketResponses["101"]; !ok {
		t.Fatal("daemon socket response must document websocket upgrade status 101")
	}
	if _, ok := socketResponses["200"]; ok {
		t.Fatal("daemon socket response must not be documented as normal 200 JSON")
	}

	for _, route := range []struct {
		path   string
		method string
	}{
		{"/api/v1/daemon/bootstrap", "post"},
		{"/api/v1/daemon/failures", "post"},
		{"/api/v1/daemon/runtimes", "post"},
		{"/api/v1/daemon/runtimes/{runtimeID}/socket", "get"},
		{"/api/v1/daemon/runtimes/{runtimeID}/end", "post"},
		{"/api/v1/daemon/runtimes/{runtimeID}/sleep", "post"},
		{"/api/v1/daemon/skills/{skillID}/archive", "get"},
	} {
		operation := openAPIOperation(t, doc.Paths, route.path, route.method)
		hidden, ok := operation["x-hidden"].(bool)
		if !ok || !hidden {
			t.Fatalf("%s %s must be hidden from public API navigation", route.method, route.path)
		}
		tags, ok := operation["tags"].([]any)
		if !ok || len(tags) != 1 || tags[0] != "Machine Daemon" {
			t.Fatalf("%s %s tags = %v, want [Machine Daemon]", route.method, route.path, tags)
		}
		security, ok := operation["security"].([]any)
		if !ok || len(security) != 1 {
			t.Fatalf("%s %s must declare machineDaemonAuth security", route.method, route.path)
		}
		requirement, ok := security[0].(map[string]any)
		if !ok {
			t.Fatalf("%s %s security requirement has unexpected shape", route.method, route.path)
		}
		if _, ok := requirement["machineDaemonAuth"]; !ok {
			t.Fatalf("%s %s must use machineDaemonAuth", route.method, route.path)
		}
	}

	browserOnlyMutations := map[string]bool{
		"post /api/v1/personal-access-tokens":                               true,
		"post /api/v1/orgs/{orgID}/api-keys":                                true,
		"patch /api/v1/orgs/{orgID}/api-keys/{keyID}":                       true,
		"post /api/v1/orgs/{orgID}/api-keys/{keyID}/revoke":                 true,
		"put /api/v1/orgs/{orgID}/api-keys/{keyID}/projects/{projectID}":    true,
		"delete /api/v1/orgs/{orgID}/api-keys/{keyID}/projects/{projectID}": true,
	}
	machineOnlyMutations := map[string]bool{
		"post /api/v1/daemon/bootstrap":                  true,
		"post /api/v1/daemon/failures":                   true,
		"post /api/v1/daemon/runtimes":                   true,
		"post /api/v1/daemon/runtimes/{runtimeID}/end":   true,
		"post /api/v1/daemon/runtimes/{runtimeID}/sleep": true,
	}
	mutatingMethods := map[string]bool{"post": true, "put": true, "patch": true, "delete": true}
	for path, pathItemAny := range doc.Paths {
		pathItem, ok := pathItemAny.(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI path %s has unexpected shape", path)
		}
		for method, operationAny := range pathItem {
			if !mutatingMethods[method] {
				continue
			}
			operation, ok := operationAny.(map[string]any)
			if !ok {
				t.Fatalf("OpenAPI operation %s %s has unexpected shape", method, path)
			}
			security, ok := operation["security"].([]any)
			if !ok {
				t.Fatalf(
					"%s %s must declare operation security because browser sessions require CSRF on mutations",
					method,
					path,
				)
			}
			key := method + " " + path
			switch {
			case machineOnlyMutations[key]:
				if !openAPISecurityHasRequirement(security, "machineDaemonAuth") {
					t.Fatalf("%s must document machine daemon auth, got %+v", key, security)
				}
			case browserOnlyMutations[key]:
				if openAPISecurityHasRequirement(security, "bearerAuth") ||
					!openAPISecurityHasRequirement(security, "browserSessionCookie", "csrfHeader") {
					t.Fatalf("%s must document browser session plus CSRF only, got %+v", key, security)
				}
			default:
				if !openAPISecurityHasRequirement(security, "bearerAuth") ||
					!openAPISecurityHasRequirement(security, "browserSessionCookie", "csrfHeader") {
					t.Fatalf("%s must document bearer auth or browser session plus CSRF, got %+v", key, security)
				}
			}
		}
	}
}

func TestOpenAPIModelProviderOpenRouterOptionsContract(t *testing.T) {
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Type                 string         `yaml:"type"`
				AdditionalProperties any            `yaml:"additionalProperties"`
				Properties           map[string]any `yaml:"properties"`
				Enum                 []any          `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(openapispec.YAML, &doc); err != nil {
		t.Fatalf("parse checked-in openapi spec: %v", err)
	}
	if got := openAPIStringSlice(doc.Components.Schemas["ModelProviderAPIVariant"].Enum); !slices.Equal(
		got,
		[]string{"default", "openrouter", "bedrock"},
	) {
		t.Fatalf("ModelProviderAPIVariant enum = %v, want default/openrouter/bedrock", got)
	}
	for _, schemaName := range []string{
		"AgentConfigModel",
		"CreateModelProviderConfigRequest",
		"ModelProviderConfig",
	} {
		apiVariant := openAPIPropertySchema(t, doc.Components.Schemas[schemaName].Properties, "api_variant")
		if apiVariant["$ref"] != "#/components/schemas/ModelProviderAPIVariant" {
			t.Fatalf("%s.api_variant must reference ModelProviderAPIVariant, got %+v", schemaName, apiVariant)
		}
	}
	for _, schemaName := range []string{
		"CreateModelProviderConfigRequest",
		"UpdateModelProviderConfigRequest",
		"ModelProviderConfig",
	} {
		if _, ok := doc.Components.Schemas[schemaName].Properties["api_variant_options"]; ok {
			t.Fatalf("%s.api_variant_options must not be exposed on provider configs", schemaName)
		}
	}
	for _, schemaName := range []string{
		"CreateConfiguredModelRequest",
		"UpdateConfiguredModelRequest",
		"ConfiguredModel",
	} {
		options := openAPIPropertySchema(t, doc.Components.Schemas[schemaName].Properties, "api_variant_options")
		if options["$ref"] != "#/components/schemas/ModelAPIVariantOptions" {
			t.Fatalf("%s.api_variant_options must reference ModelAPIVariantOptions, got %+v", schemaName, options)
		}
	}
	apiVariantOptions := doc.Components.Schemas["ModelAPIVariantOptions"]
	if apiVariantOptions.Type != "object" {
		t.Fatalf("ModelAPIVariantOptions type = %v, want object", apiVariantOptions.Type)
	}
	if apiVariantOptions.AdditionalProperties != true {
		t.Fatal("ModelAPIVariantOptions must allow provider-owned fields")
	}
	if len(apiVariantOptions.Properties) != 0 {
		t.Fatalf("ModelAPIVariantOptions must be a flat passthrough object, got properties %+v", apiVariantOptions.Properties)
	}
	if _, ok := doc.Components.Schemas["ModelAPIVariantProviderOptions"]; ok {
		t.Fatal("ModelAPIVariantProviderOptions must not exist; api_variant_options is the only public options object")
	}
}

func openAPIPropertySchema(t *testing.T, properties map[string]any, name string) map[string]any {
	t.Helper()
	raw, ok := properties[name]
	if !ok {
		t.Fatalf("OpenAPI property %s is missing", name)
	}
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI property %s has unexpected shape", name)
	}
	return value
}

func openAPIStringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func openAPIOperation(t *testing.T, paths map[string]any, path string, method string) map[string]any {
	t.Helper()
	pathItem, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("missing OpenAPI path %s", path)
	}
	operation, ok := pathItem[method].(map[string]any)
	if !ok {
		t.Fatalf("missing OpenAPI operation %s %s", method, path)
	}
	return operation
}

func openAPISecurityHasRequirement(security []any, names ...string) bool {
	for _, requirementAny := range security {
		requirement, ok := requirementAny.(map[string]any)
		if !ok || len(requirement) != len(names) {
			continue
		}
		matched := true
		for _, name := range names {
			if _, ok := requirement[name]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func openAPIResponses(t *testing.T, paths map[string]any, path string, method string) map[string]any {
	t.Helper()
	operation := openAPIOperation(t, paths, path, method)
	responses, ok := operation["responses"].(map[string]any)
	if !ok {
		t.Fatalf("missing OpenAPI responses for %s %s", method, path)
	}
	return responses
}

func openAPIResponseContent(
	t *testing.T,
	paths map[string]any,
	path string,
	method string,
	status string,
) map[string]any {
	t.Helper()
	response, ok := openAPIResponses(t, paths, path, method)[status].(map[string]any)
	if !ok {
		t.Fatalf("missing OpenAPI response %s for %s %s", status, method, path)
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		t.Fatalf("missing OpenAPI response content for %s %s %s", method, path, status)
	}
	return content
}

func assertJSONResponseSchemaRef(t *testing.T, paths map[string]any, path string, method string, schemaName string) {
	t.Helper()
	content := openAPIResponseContent(t, paths, path, method, "200")
	jsonContent, ok := content["application/json"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s must document application/json response", method, path)
	}
	schema, ok := jsonContent["schema"].(map[string]any)
	if !ok {
		t.Fatalf("%s %s application/json response must document schema", method, path)
	}
	want := "#/components/schemas/" + schemaName
	if got, _ := schema["$ref"].(string); got != want {
		t.Fatalf("%s %s schema ref = %q, want %q", method, path, got, want)
	}
}

func TestOpenAPIRequestValidatorRejectsUnknownV1Routes(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
		error  string
	}{
		{name: "path", method: http.MethodGet, path: "/api/v1/not-a-route", status: http.StatusNotFound, error: "not found"},
		{name: "method", method: http.MethodPut, path: "/api/v1/orgs", status: http.StatusNotFound, error: "not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.status, rec.Body.String())
			}
			if contentType := rec.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode validation error body: %v", err)
			}
			if body["error"] != tc.error {
				t.Fatalf("validation error = %q, want %q", body["error"], tc.error)
			}
		})
	}
}

func TestOpenAPIRequestValidatorRejectsUndeclaredQueryParams(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	orgPath := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	tests := []struct {
		name      string
		path      string
		want      int
		wantError string
	}{
		{
			name: "ordinary list allows declared params",
			path: "/api/v1/personal-access-tokens?limit=10&cursor=abc",
			want: http.StatusNoContent,
		},
		{
			name:      "ordinary list rejects undeclared param",
			path:      "/api/v1/personal-access-tokens?foo=bar",
			want:      http.StatusBadRequest,
			wantError: "unsupported query parameter: foo",
		},
		{
			name:      "route without params rejects query",
			path:      "/api/v1/me?foo=bar",
			want:      http.StatusBadRequest,
			wantError: "unsupported query parameter: foo",
		},
		{
			name: "deep object metadata filter allows bracketed key",
			path: "/api/v1/orgs/" + orgPath + "/secrets?metadata[label]=owner",
			want: http.StatusNoContent,
		},
		{
			name:      "deep object metadata filter rejects bare key",
			path:      "/api/v1/orgs/" + orgPath + "/secrets?metadata=owner",
			want:      http.StatusBadRequest,
			wantError: "unsupported query parameter: metadata",
		},
		{
			name:      "deep object route still rejects unrelated query",
			path:      "/api/v1/orgs/" + orgPath + "/secrets?metadata[label]=owner&foo=bar",
			want:      http.StatusBadRequest,
			wantError: "unsupported query parameter: foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
			if tt.wantError != "" && !strings.Contains(rec.Body.String(), tt.wantError) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), tt.wantError)
			}
		})
	}
}

func TestOpenAPIRequestValidatorAllowsFlatSkillUpload(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	ownerHeader := make(textproto.MIMEHeader)
	ownerHeader.Set("Content-Disposition", `form-data; name="owner"`)
	ownerHeader.Set("Content-Type", "application/json")
	owner, err := writer.CreatePart(ownerHeader)
	if err != nil {
		t.Fatalf("create owner field: %v", err)
	}
	projectID := testPublicID(t, publicid.KindProject, httpTestProjectID)
	if _, err := owner.Write([]byte(`{"kind":"project","project_id":"` + projectID + `"}`)); err != nil {
		t.Fatalf("write owner field: %v", err)
	}
	part, err := writer.CreateFormFile("archive", "skill.tar.gz")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("placeholder")); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	orgID := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/skills",
		&body,
	)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOpenAPIRequestValidatorRejectsSchemaViolations(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	orgPath := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	body := `{"name":"pool","provider":"test","default_machine_cpu":1,` +
		`"default_machine_memory_mb":1024,"default_machine_provider_options":{},"default_machine_extra":true,"max_total_machines":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+orgPath+"/machine-pools", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode validation error body: %v; body=%s", err, rec.Body.String())
	}
	if response["error"] == "" {
		t.Fatalf("expected validation error body, got %+v", response)
	}
}

func TestOpenAPIRequestValidatorEnforcesMachinePoolProviderShape(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	orgID := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	secretID := testPublicID(t, publicid.KindSecret, testHTTPID(29))
	common := `"name":"pool","default_machine_provider_options":{},` +
		`"provider_auth_secret_id":"` + secretID + `","max_total_machines":1`
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "unikraft",
			body: `{"provider":"unikraft",` + common +
				`,"default_machine_cpu":1,"default_machine_memory_mb":1024,` +
				`"max_total_cpu":4,"max_total_memory_mb":8192,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
			want: http.StatusNoContent,
		},
		{
			name: "unikraft missing cpu",
			body: `{"provider":"unikraft",` + common + `}`,
			want: http.StatusBadRequest,
		},
		{
			name: "daytona without resource defaults",
			body: `{"provider":"daytona",` + common +
				`,"max_total_cpu":4,"max_total_memory_mb":8192,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
			want: http.StatusNoContent,
		},
		{
			name: "daytona with optional resource defaults",
			body: `{"provider":"daytona",` + common +
				`,"default_machine_cpu":1,"default_machine_memory_mb":1024,` +
				`"max_total_cpu":4,"max_total_memory_mb":8192,` +
				`"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
			want: http.StatusNoContent,
		},
		{
			name: "daytona missing cpu limit",
			body: `{"provider":"daytona",` + common +
				`,"max_total_memory_mb":8192,"max_machine_cpu":2,"max_machine_memory_mb":4096}`,
			want: http.StatusBadRequest,
		},
		{
			name: "blaxel missing memory",
			body: `{"provider":"blaxel",` + common + `}`,
			want: http.StatusBadRequest,
		},
		{
			name: "blaxel",
			body: `{"provider":"blaxel",` + common +
				`,"default_machine_memory_mb":1024,"max_total_memory_mb":8192,"max_machine_memory_mb":4096}`,
			want: http.StatusNoContent,
		},
		{
			name: "blaxel rejects cpu",
			body: `{"provider":"blaxel",` + common +
				`,"default_machine_cpu":1,"default_machine_memory_mb":1024,` +
				`"max_total_memory_mb":8192,"max_machine_memory_mb":4096}`,
			want: http.StatusBadRequest,
		},
		{
			name: "blaxel rejects cpu limit",
			body: `{"provider":"blaxel",` + common +
				`,"default_machine_memory_mb":1024,"max_total_cpu":4,` +
				`"max_total_memory_mb":8192,"max_machine_memory_mb":4096}`,
			want: http.StatusBadRequest,
		},
		{
			name: "blaxel rejects cpu minimum",
			body: `{"provider":"blaxel",` + common +
				`,"default_machine_memory_mb":1024,"min_machine_cpu":1,` +
				`"max_total_memory_mb":8192,"max_machine_memory_mb":4096}`,
			want: http.StatusBadRequest,
		},
		{
			name: "unknown provider",
			body: `{"provider":"unknown",` + common + `}`,
			want: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/orgs/"+orgID+"/machine-pools",
				strings.NewReader(tt.body),
			)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tt.want)
			}
		})
	}
}

func TestOpenAPIParameterErrorsUseJSONEnvelope(t *testing.T) {
	server := mustNewUnitServer(t)
	mux := http.NewServeMux()
	server.registerRoutes(mux)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/orgs/org_agpoe6flpb4zrhtsjjr6h73dty/projects/proj_agpoe6flpv7ohmgp4v5tkcbjse/agents/agt_agpoe6flt57sbavd6mmu63ihyq/events?after_sequence=bad",
		nil,
	)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode generated parameter error body: %v body=%s", err, rec.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("expected generated parameter error body, got %+v", body)
	}
}

func TestOpenAPIRequestValidatorRejectsNoBodyOperationBodies(t *testing.T) {
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatalf("load generated openapi spec: %v", err)
	}
	handler := newOpenAPIValidatorTestHandler(t)

	checked := 0
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.RequestBody != nil {
				continue
			}
			if operationRequiresNonPathParam(item, op) {
				continue
			}
			concrete := concreteOpenAPIPath(path)
			req := httptest.NewRequest(method, concrete, strings.NewReader(`{"unexpected":true}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf(
					"%s %s with body: status = %d, want %d; body=%s",
					method,
					concrete,
					rec.Code,
					http.StatusBadRequest,
					rec.Body.String(),
				)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s %s: decode error body: %v; body=%s", method, concrete, err, rec.Body.String())
			}
			if body["error"] == "" {
				t.Fatalf("%s %s: expected validation error body, got %+v", method, concrete, body)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("expected to exercise at least one no-body operation")
	}
}

// operationRequiresNonPathParam reports whether the operation (including
// path-level parameters) requires a query/header/cookie parameter. Those would
// fail generated parameter binding with a 400 before the no-body check runs.
func operationRequiresNonPathParam(item *openapi3.PathItem, op *openapi3.Operation) bool {
	for _, params := range []openapi3.Parameters{item.Parameters, op.Parameters} {
		for _, ref := range params {
			if ref.Value != nil && ref.Value.In != openapi3.ParameterInPath && ref.Value.Required {
				return true
			}
		}
	}
	return false
}

// concreteOpenAPIPath replaces every {param} placeholder with a routable, non-empty
// segment. The generated parameter binder accepts any string for path parameters
// (public-ID shape is validated later inside the strict method), so the no-body
// check is reached regardless of the placeholder value.
func concreteOpenAPIPath(pathTemplate string) string {
	var b strings.Builder
	for {
		open := strings.IndexByte(pathTemplate, '{')
		if open < 0 {
			b.WriteString(pathTemplate)
			return b.String()
		}
		closeIdx := strings.IndexByte(pathTemplate[open:], '}')
		if closeIdx < 0 {
			b.WriteString(pathTemplate)
			return b.String()
		}
		b.WriteString(pathTemplate[:open])
		b.WriteString("placeholder")
		pathTemplate = pathTemplate[open+closeIdx+1:]
	}
}

// TestUnauthenticatedRequestRejectedBeforeBodyValidation locks in the stack
// ordering the strict cutover depends on: top-level authentication (s.auth) runs
// before the generated strict body decoder, so an unauthenticated request with a
// malformed body returns 401, never 400. Leaking a 400 here would tell an
// anonymous caller their malformed body was parsed, undermining the
// auth-visibility semantics the strict handler preserves.
func TestUnauthenticatedRequestRejectedBeforeBodyValidation(t *testing.T) {
	server := mustNewUnitServer(t)
	handler := server.Handler()

	// No Authorization header, and a body that would fail strict JSON decoding.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs", strings.NewReader(`{ this is not json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf(
			"status = %d, want %d (auth must precede body validation); body=%s",
			rec.Code,
			http.StatusUnauthorized,
			rec.Body.String(),
		)
	}
}

func TestOpenAPIRequestValidatorRejectsUnsupportedHEAD(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/api/v1/invitations", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("HEAD status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestOpenAPIRequestValidatorRejectsTrailingJSON(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	orgID := "org_" + strings.Repeat("a", 26)
	for name, body := range map[string]string{
		"second object":  `{"owner":{"kind":"org"},"name":"n","material":{"kind":"generic","value":"v"}} {}`,
		"trailing array": `{"owner":{"kind":"org"},"name":"n","material":{"kind":"generic","value":"v"}} []`,
		"trailing junk":  `{"owner":{"kind":"org"},"name":"n","material":{"kind":"generic","value":"v"}} true`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+orgID+"/secrets", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest ||
				!strings.Contains(rec.Body.String(), "single JSON value") {
				t.Fatalf("status = %d body = %s, want 400 single-JSON-value error", rec.Code, rec.Body.String())
			}
		})
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/secrets",
		strings.NewReader(`{"owner":{"kind":"org"},"name":"n","material":{"kind":"generic","value":"v"}}`+"\n\t "),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("trailing whitespace status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
	}
}

func TestOpenAPIRequestValidatorRejectsBodyOnNoBodyOperation(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	invitationID := "oinv_" + strings.Repeat("a", 26)
	for name, body := range map[string]string{
		"empty object": `{}`,
		"json value":   `{"unexpected":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/invitations/"+invitationID+"/accept",
				strings.NewReader(body),
			)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/"+invitationID+"/accept", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("empty body status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusNoContent)
	}
}

func TestOpenAPIValidationErrorsOmitRequestValues(t *testing.T) {
	handler := newOpenAPIValidatorTestHandler(t)
	orgID := "org_" + strings.Repeat("a", 26)
	const sentinel = "sentinel-value-must-not-leak"
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orgs/"+orgID+"/secrets",
		strings.NewReader(`{"owner":{"kind":"org"},"name":"n","material":{"kind":{"nested":"`+sentinel+`"},"value":"v"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
	}
	if strings.Contains(rec.Body.String(), sentinel) {
		t.Fatalf("validation error echoed the request value: %s", rec.Body.String())
	}
}

func newOpenAPIValidatorTestHandler(t *testing.T) http.Handler {
	t.Helper()
	validator, err := newOpenAPIRequestValidator()
	if err != nil {
		t.Fatalf("create openapi request validator: %v", err)
	}
	return validator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
}
