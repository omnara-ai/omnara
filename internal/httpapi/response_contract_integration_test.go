//go:build integration

package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	sjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

type responseContract struct {
	router routers.Router
	body   *documentBodyValidator
}

var (
	responseContractOnce  sync.Once
	responseContractValue *responseContract
	responseContractErr   error
)

func loadResponseContract() (*responseContract, error) {
	responseContractOnce.Do(func() {
		spec, err := openapi.GetSpec()
		if err != nil {
			responseContractErr = err
			return
		}
		spec.Servers = nil
		router, err := gorillamux.NewRouter(spec)
		if err != nil {
			responseContractErr = err
			return
		}
		body, err := newDocumentBodyValidator()
		if err != nil {
			responseContractErr = err
			return
		}
		responseContractValue = &responseContract{router: router, body: body}
	})
	return responseContractValue, responseContractErr
}

const openapiDocumentResource = "urn:omnara:openapi-document"

const errorSchemaPointer = "/components/schemas/Error"

type documentBodyValidator struct {
	mu       sync.Mutex
	root     any
	compiler *sjsonschema.Compiler
	schemas  map[string]*sjsonschema.Schema
}

func newDocumentBodyValidator() (*documentBodyValidator, error) {
	var decoded any
	if err := yaml.Unmarshal(openapispec.YAML, &decoded); err != nil {
		return nil, fmt.Errorf("decode openapi document: %w", err)
	}
	raw, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode openapi document as JSON: %w", err)
	}
	document, err := sjsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode openapi document JSON: %w", err)
	}
	compiler := sjsonschema.NewCompiler()
	compiler.DefaultDraft(sjsonschema.Draft2020)
	compiler.UseLoader(sjsonschema.SchemeURLLoader{})
	if err := compiler.AddResource(openapiDocumentResource, document); err != nil {
		return nil, fmt.Errorf("register openapi document: %w", err)
	}
	return &documentBodyValidator{
		root:     document,
		compiler: compiler,
		schemas:  map[string]*sjsonschema.Schema{},
	}, nil
}

func (d *documentBodyValidator) schemaAt(pointer string) (*sjsonschema.Schema, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if schema, ok := d.schemas[pointer]; ok {
		return schema, nil
	}
	schema, err := d.compiler.Compile(openapiDocumentResource + "#" + pointer)
	if err != nil {
		return nil, fmt.Errorf("compile schema at %s: %w", pointer, err)
	}
	d.schemas[pointer] = schema
	return schema, nil
}

func escapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func resolveJSONPointer(root any, pointer string) (any, bool) {
	node := root
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		node, ok = object[token]
		if !ok {
			return nil, false
		}
	}
	return node, true
}

func responseStatusKey(responses map[string]any, status int) (string, bool) {
	for _, key := range []string{
		strconv.Itoa(status),
		strconv.Itoa(status/100) + "XX",
		"default",
	} {
		if _, ok := responses[key]; ok {
			return key, true
		}
	}
	return "", false
}

func (d *documentBodyValidator) bodySchemaPointer(specPath, method string, status int) (string, bool) {
	base := "/paths/" + escapeJSONPointerToken(specPath) + "/" + strings.ToLower(method) + "/responses"
	node, ok := resolveJSONPointer(d.root, base)
	if !ok {
		return "", false
	}
	responses, ok := node.(map[string]any)
	if !ok {
		return "", false
	}
	statusKey, ok := responseStatusKey(responses, status)
	if !ok {
		return "", false
	}
	base += "/" + statusKey
	node = responses[statusKey]
	if response, ok := node.(map[string]any); ok {
		if ref, ok := response["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			base = strings.TrimPrefix(ref, "#")
			if node, ok = resolveJSONPointer(d.root, base); !ok {
				return "", false
			}
		}
	}
	response, ok := node.(map[string]any)
	if !ok {
		return "", false
	}
	content, ok := response["content"].(map[string]any)
	if !ok {
		return "", false
	}
	keys := make([]string, 0, len(content))
	for key := range content {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		mediaType, _, err := mime.ParseMediaType(key)
		if err != nil || mediaType != "application/json" {
			continue
		}
		if media, ok := content[key].(map[string]any); ok {
			if _, ok := media["schema"]; ok {
				return base + "/content/" + escapeJSONPointerToken(key) + "/schema", true
			}
		}
	}
	return "", false
}

func (d *documentBodyValidator) validateErrorEnvelope(body []byte) error {
	schema, err := d.schemaAt(errorSchemaPointer)
	if err != nil {
		return err
	}
	value, err := sjsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	return schema.Validate(value)
}

func (d *documentBodyValidator) validateBody(specPath, method string, status int, body []byte) error {
	pointer, ok := d.bodySchemaPointer(specPath, method, status)
	if !ok {
		return nil
	}
	schema, err := d.schemaAt(pointer)
	if err != nil {
		return err
	}
	value, err := sjsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	return schema.Validate(value)
}

type responseContractRecorder struct {
	http.ResponseWriter
	request   *http.Request
	status    int
	decided   bool
	buffering bool
	hijacked  bool
	body      bytes.Buffer
}

func shouldBufferResponse(request *http.Request, status int) bool {
	contract, err := loadResponseContract()
	if err != nil {
		return true
	}
	route, _, err := contract.router.FindRoute(request)
	if err != nil {
		return true
	}
	response := route.Operation.Responses.Status(status)
	if response == nil {
		return true
	}
	if response.Value == nil {
		return false
	}
	for key := range response.Value.Content {
		mediaType, _, err := mime.ParseMediaType(key)
		if err == nil && mediaType == "application/json" {
			return true
		}
	}
	return false
}

func (r *responseContractRecorder) WriteHeader(status int) {
	if !r.decided {
		r.decided = true
		r.status = status
		r.buffering = shouldBufferResponse(r.request, status)
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseContractRecorder) Write(p []byte) (int, error) {
	if !r.decided {
		r.WriteHeader(http.StatusOK)
	}
	if r.buffering {
		r.body.Write(p)
	}
	return r.ResponseWriter.Write(p)
}

func (r *responseContractRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseContractRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	r.hijacked = true
	return hijacker.Hijack()
}

func validateResponseContentType(route *routers.Route, status int, contentType string) error {
	response := route.Operation.Responses.Status(status)
	if response == nil || response.Value == nil || len(response.Value.Content) == 0 {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fmt.Errorf("parse response Content-Type %q: %w", contentType, err)
	}
	if response.Value.Content.Get(mediaType) == nil {
		return fmt.Errorf("response Content-Type %q is not declared for status %d", contentType, status)
	}
	return nil
}

func validateResponseContract(request *http.Request, recorder *responseContractRecorder) {
	if recorder.hijacked {
		return
	}
	if !recorder.decided {
		recorder.decided = true
		recorder.status = http.StatusOK
		recorder.buffering = shouldBufferResponse(request, http.StatusOK)
	}
	contract, err := loadResponseContract()
	if err != nil {
		panic(fmt.Sprintf("load openapi response contract: %v", err))
	}
	route, pathParams, err := contract.router.FindRoute(request)
	if err != nil {
		if recorder.status >= 200 && recorder.status < 300 {
			panic(fmt.Sprintf(
				"openapi response contract violation: %s %s matches no OpenAPI operation but returned %d\nresponse body: %s",
				request.Method, request.URL.Path, recorder.status, recorder.body.Bytes(),
			))
		}
		if err := contract.body.validateErrorEnvelope(recorder.body.Bytes()); err != nil {
			panic(fmt.Sprintf(
				"openapi response contract violation: %s %s matches no OpenAPI operation and returned %d without an Error envelope: %v\nresponse body: %s",
				request.Method, request.URL.Path, recorder.status, err, recorder.body.Bytes(),
			))
		}
		return
	}
	if route.Operation.Responses.Status(recorder.status) == nil {
		panic(fmt.Sprintf(
			"openapi response contract violation: %s %s returned undeclared status %d\nresponse body: %s",
			request.Method, request.URL.Path, recorder.status, recorder.body.Bytes(),
		))
	}
	if !recorder.buffering {
		if err := validateResponseContentType(route, recorder.status, recorder.Header().Get("Content-Type")); err != nil {
			panic(fmt.Sprintf(
				"openapi response contract violation: %s %s returned %d: %v",
				request.Method, request.URL.Path, recorder.status, err,
			))
		}
		return
	}
	if recorder.body.Len() == 0 {
		panic(fmt.Sprintf(
			"openapi response contract violation: %s %s returned %d with an empty body where the contract declares a JSON response",
			request.Method, request.URL.Path, recorder.status,
		))
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    request,
			PathParams: pathParams,
			Route:      route,
		},
		Status:  recorder.status,
		Header:  recorder.Header(),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	input.SetBodyBytes(recorder.body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		panic(fmt.Sprintf(
			"openapi response contract violation: %s %s returned %d: %v\nresponse body: %s",
			request.Method, request.URL.Path, recorder.status, err, recorder.body.Bytes(),
		))
	}
	if err := contract.body.validateBody(route.Path, request.Method, recorder.status, recorder.body.Bytes()); err != nil {
		panic(fmt.Sprintf(
			"openapi response contract violation: %s %s returned %d: %v\nresponse body: %s",
			request.Method, request.URL.Path, recorder.status, err, recorder.body.Bytes(),
		))
	}
}
