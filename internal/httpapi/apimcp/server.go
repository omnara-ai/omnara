package apimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

const (
	Path = "/mcp"

	serverName           = "omnara"
	bodyArgument         = "body"
	idempotencyHeader    = "Idempotency-Key"
	componentSchemaRef   = "#/components/schemas/"
	definitionsRef       = "#/$defs/"
	definitionsKey       = "$defs"
	defaultKey           = "default"
	unsupportedParameter = "parameter %q of operation %s is in %q, only path and query are supported"
)

var componentRefPattern = regexp.MustCompile(`"#/components/schemas/([^"#/]+)"`)

type Grants interface {
	Allows(ctx context.Context, operationID string) (bool, error)
}

type GrantResolver func(ctx context.Context) Grants

type Options struct {
	Dispatch    http.Handler
	APIBasePath string
	Grants      GrantResolver
}

type ToolCall struct {
	Tool        string
	OperationID string
}

type toolCallContextKey struct{}

func ContextWithToolCall(ctx context.Context, call ToolCall) context.Context {
	return context.WithValue(ctx, toolCallContextKey{}, call)
}

func ToolCallFromContext(ctx context.Context) (ToolCall, bool) {
	call, ok := ctx.Value(toolCallContextKey{}).(ToolCall)
	return call, ok
}

type queryParameter struct {
	name       string
	array      bool
	deepObject bool
}

type operation struct {
	name          string
	operationID   string
	method        string
	path          string
	basePath      string
	pathParams    []string
	queryParams   []queryParameter
	hasBody       bool
	bodyFlattened bool
	bodyKeys      []string
	idempotency   bool
	dispatch      http.Handler
}

type specOperation struct {
	method     string
	path       string
	parameters []*openapi3.Parameter
	operation  *openapi3.Operation
}

func NewServer(spec *openapi3.T, tools []Tool, options Options) (*mcp.Server, error) {
	if options.Dispatch == nil {
		return nil, errors.New("dispatch handler is required")
	}
	operations := indexOperations(spec)
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: specVersion(spec)}, nil)
	operationByTool := make(map[string]string, len(tools))
	for _, tool := range tools {
		if _, duplicate := operationByTool[tool.Name]; duplicate {
			return nil, fmt.Errorf("duplicate mcp tool name %q", tool.Name)
		}
		source, ok := operations[operationKey(tool.OperationID)]
		if !ok {
			return nil, fmt.Errorf("mcp tool %q references unknown operation %q", tool.Name, tool.OperationID)
		}
		compiled, inputSchema, err := compileOperation(spec, tool, source, options)
		if err != nil {
			return nil, fmt.Errorf("mcp tool %q: %w", tool.Name, err)
		}
		operationByTool[tool.Name] = compiled.operationID
		description := tool.Description
		if description == "" {
			description = operationDescription(source.operation)
		}
		mcp.AddTool(server, &mcp.Tool{
			Name:        tool.Name,
			Description: description,
			InputSchema: inputSchema,
			Annotations: annotationsFor(compiled.method, tool.Destructive),
		}, compiled.handle)
	}
	if options.Grants != nil {
		server.AddReceivingMiddleware(grantMiddleware(options.Grants, operationByTool))
	}
	return server, nil
}

func NewHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:                  true,
			JSONResponse:               true,
			DisableLocalhostProtection: true,
		},
	)
}

func annotationsFor(method string, destructive bool) *mcp.ToolAnnotations {
	readOnly := method == http.MethodGet
	idempotent := readOnly || method == http.MethodPut || method == http.MethodDelete
	annotations := &mcp.ToolAnnotations{
		ReadOnlyHint:   readOnly,
		IdempotentHint: idempotent,
	}
	switch {
	case readOnly:
		annotations.DestructiveHint = new(false)
	case destructive || method == http.MethodDelete:
		annotations.DestructiveHint = new(true)
	}
	return annotations
}

func specVersion(spec *openapi3.T) string {
	if spec.Info == nil {
		return ""
	}
	return spec.Info.Version
}

func operationDescription(op *openapi3.Operation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func operationKey(operationID string) string {
	return strings.ToLower(operationID)
}

func indexOperations(spec *openapi3.T) map[string]specOperation {
	operations := make(map[string]specOperation)
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				continue
			}
			parameters := make([]*openapi3.Parameter, 0, len(item.Parameters)+len(op.Parameters))
			for _, ref := range item.Parameters {
				parameters = append(parameters, ref.Value)
			}
			for _, ref := range op.Parameters {
				parameters = append(parameters, ref.Value)
			}
			operations[operationKey(op.OperationID)] = specOperation{
				method:     strings.ToUpper(method),
				path:       path,
				parameters: parameters,
				operation:  op,
			}
		}
	}
	return operations
}

func compileOperation(
	spec *openapi3.T,
	tool Tool,
	source specOperation,
	options Options,
) (*operation, map[string]any, error) {
	compiled := &operation{
		name:        tool.Name,
		operationID: source.operation.OperationID,
		method:      source.method,
		path:        source.path,
		basePath:    options.APIBasePath,
		dispatch:    options.Dispatch,
	}
	properties := make(map[string]json.RawMessage)
	required := make([]string, 0)
	addProperty := func(name string, schema json.RawMessage, isRequired bool) error {
		if _, exists := properties[name]; exists {
			return fmt.Errorf("argument %q is declared more than once", name)
		}
		properties[name] = schema
		if isRequired {
			required = append(required, name)
		}
		return nil
	}
	for _, parameter := range source.parameters {
		switch parameter.In {
		case openapi3.ParameterInPath:
			compiled.pathParams = append(compiled.pathParams, parameter.Name)
		case openapi3.ParameterInQuery:
			compiled.queryParams = append(compiled.queryParams, queryParameter{
				name:       parameter.Name,
				array:      isArrayParameter(parameter),
				deepObject: parameter.Style == openapi3.SerializationDeepObject,
			})
		case openapi3.ParameterInHeader:
			if parameter.Name == idempotencyHeader {
				compiled.idempotency = true
				continue
			}
			return nil, nil, fmt.Errorf(unsupportedParameter, parameter.Name, tool.OperationID, parameter.In)
		default:
			return nil, nil, fmt.Errorf(unsupportedParameter, parameter.Name, tool.OperationID, parameter.In)
		}
		schema, err := parameterSchemaJSON(parameter)
		if err != nil {
			return nil, nil, err
		}
		if err := addProperty(parameter.Name, schema, parameter.Required); err != nil {
			return nil, nil, err
		}
	}
	if body := jsonRequestBody(source.operation); body != nil {
		compiled.hasBody = true
		bodySchema := body.Value
		if isFlatObjectSchema(bodySchema) {
			compiled.bodyFlattened = true
			for name, property := range bodySchema.Properties {
				schema, err := property.MarshalJSON()
				if err != nil {
					return nil, nil, fmt.Errorf("marshal body property %q: %w", name, err)
				}
				if err := addProperty(name, schema, slices.Contains(bodySchema.Required, name)); err != nil {
					return nil, nil, err
				}
				compiled.bodyKeys = append(compiled.bodyKeys, name)
			}
		} else {
			schema, err := body.MarshalJSON()
			if err != nil {
				return nil, nil, fmt.Errorf("marshal request body schema: %w", err)
			}
			bodyRequired := source.operation.RequestBody.Value.Required
			if err := addProperty(bodyArgument, schema, bodyRequired); err != nil {
				return nil, nil, err
			}
		}
	}
	slices.Sort(required)
	inputSchema, err := assembleInputSchema(spec, properties, required)
	if err != nil {
		return nil, nil, err
	}
	return compiled, inputSchema, nil
}

func isArrayParameter(parameter *openapi3.Parameter) bool {
	return parameter.Schema != nil && parameter.Schema.Value != nil && parameter.Schema.Value.Type.Is(openapi3.TypeArray)
}

func parameterSchemaJSON(parameter *openapi3.Parameter) (json.RawMessage, error) {
	if parameter.Schema == nil {
		return json.RawMessage(`{"type":"string"}`), nil
	}
	schema, err := parameter.Schema.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal parameter %q schema: %w", parameter.Name, err)
	}
	if parameter.Description == "" {
		return schema, nil
	}
	return withDescription(schema, parameter.Description)
}

func withDescription(schema json.RawMessage, description string) (json.RawMessage, error) {
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return nil, err
	}
	if _, has := decoded["description"]; has {
		return schema, nil
	}
	if _, isRef := decoded["$ref"]; isRef {
		return json.Marshal(map[string]any{"allOf": []any{decoded}, "description": description})
	}
	decoded["description"] = description
	return json.Marshal(decoded)
}

func jsonRequestBody(op *openapi3.Operation) *openapi3.SchemaRef {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return nil
	}
	media := op.RequestBody.Value.Content.Get("application/json")
	if media == nil {
		return nil
	}
	return media.Schema
}

func isFlatObjectSchema(schema *openapi3.Schema) bool {
	if schema == nil || len(schema.Properties) == 0 {
		return false
	}
	return len(schema.AllOf) == 0 && len(schema.OneOf) == 0 && len(schema.AnyOf) == 0
}

func assembleInputSchema(
	spec *openapi3.T,
	properties map[string]json.RawMessage,
	required []string,
) (map[string]any, error) {
	encodedProperties, err := json.Marshal(properties)
	if err != nil {
		return nil, err
	}
	definitions, err := collectDefinitions(spec, encodedProperties)
	if err != nil {
		return nil, err
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           json.RawMessage(rewriteComponentRefs(encodedProperties)),
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	if len(definitions) > 0 {
		schema[definitionsKey] = definitions
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	stripDefaults(decoded)
	return decoded, nil
}

func stripDefaults(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, defaultKey)
		for _, child := range typed {
			stripDefaults(child)
		}
	case []any:
		for _, child := range typed {
			stripDefaults(child)
		}
	}
}

func collectDefinitions(spec *openapi3.T, seed []byte) (map[string]json.RawMessage, error) {
	definitions := make(map[string]json.RawMessage)
	pending := referencedComponentNames(seed)
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if _, done := definitions[name]; done {
			continue
		}
		component, ok := spec.Components.Schemas[name]
		if !ok {
			return nil, fmt.Errorf("unknown component schema %q", name)
		}
		encoded, err := component.Value.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal component schema %q: %w", name, err)
		}
		definitions[name] = json.RawMessage(rewriteComponentRefs(encoded))
		pending = append(pending, referencedComponentNames(encoded)...)
	}
	return definitions, nil
}

func referencedComponentNames(encoded []byte) []string {
	matches := componentRefPattern.FindAllSubmatch(encoded, -1)
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, string(match[1]))
	}
	return names
}

func rewriteComponentRefs(encoded []byte) []byte {
	return bytes.ReplaceAll(encoded, []byte(componentSchemaRef), []byte(definitionsRef))
}

func (o *operation) handle(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	arguments map[string]json.RawMessage,
) (result *mcp.CallToolResult, _ any, err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		logpkg.Error(ctx, fmt.Errorf("mcp tool %s panicked: %v", o.name, recovered))
		logpkg.Attach(ctx, logpkg.Fields{"error.stack": string(debug.Stack())})
		result, err = nil, internalError()
	}()
	ctx = ContextWithToolCall(ctx, ToolCall{Tool: o.name, OperationID: o.operationID})
	httpRequest, err := o.buildRequest(ctx, arguments)
	if err != nil {
		return nil, nil, err
	}
	recorder := httptest.NewRecorder()
	o.dispatch.ServeHTTP(recorder, httpRequest)
	return resultFromResponse(recorder), nil, nil
}

func internalError() error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal server error"}
}

func unknownToolError(name string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
}

func (o *operation) buildRequest(ctx context.Context, arguments map[string]json.RawMessage) (*http.Request, error) {
	path := o.path
	for _, name := range o.pathParams {
		raw, ok := arguments[name]
		if !ok {
			return nil, fmt.Errorf("missing required argument %q", name)
		}
		value, err := scalarString(raw)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %w", name, err)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
	}
	query := url.Values{}
	for _, parameter := range o.queryParams {
		raw, ok := arguments[parameter.name]
		if !ok {
			continue
		}
		if err := appendQueryValues(query, parameter, raw); err != nil {
			return nil, err
		}
	}
	var body []byte
	if o.hasBody {
		encoded, err := o.encodeBody(arguments)
		if err != nil {
			return nil, err
		}
		body = encoded
	}
	target := o.basePath + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	httpRequest, err := http.NewRequestWithContext(ctx, o.method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Accept", "application/json")
	if body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if o.idempotency {
		httpRequest.Header.Set(idempotencyHeader, uuid.NewString())
	}
	return httpRequest, nil
}

func (o *operation) encodeBody(arguments map[string]json.RawMessage) ([]byte, error) {
	if !o.bodyFlattened {
		raw, ok := arguments[bodyArgument]
		if !ok {
			return []byte("{}"), nil
		}
		return raw, nil
	}
	body := make(map[string]json.RawMessage, len(o.bodyKeys))
	for _, name := range o.bodyKeys {
		raw, ok := arguments[name]
		if !ok {
			continue
		}
		body[name] = raw
	}
	return json.Marshal(body)
}

func appendQueryValues(query url.Values, parameter queryParameter, raw json.RawMessage) error {
	if parameter.deepObject {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("argument %q must be an object: %w", parameter.name, err)
		}
		for key, field := range fields {
			value, err := scalarString(field)
			if err != nil {
				return fmt.Errorf("argument %q[%s]: %w", parameter.name, key, err)
			}
			query.Add(parameter.name+"["+key+"]", value)
		}
		return nil
	}
	if parameter.array {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("argument %q must be an array: %w", parameter.name, err)
		}
		for _, item := range items {
			value, err := scalarString(item)
			if err != nil {
				return fmt.Errorf("argument %q: %w", parameter.name, err)
			}
			query.Add(parameter.name, value)
		}
		return nil
	}
	value, err := scalarString(raw)
	if err != nil {
		return fmt.Errorf("argument %q: %w", parameter.name, err)
	}
	query.Add(parameter.name, value)
	return nil
}

func scalarString(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	switch typed := value.(type) {
	case string:
		return typed, nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case nil:
		return "", errors.New("must not be null")
	default:
		return "", errors.New("must be a string, number, or boolean")
	}
}

func resultFromResponse(recorder *httptest.ResponseRecorder) *mcp.CallToolResult {
	body := recorder.Body.Bytes()
	text := string(body)
	if len(bytes.TrimSpace(body)) == 0 {
		text = http.StatusText(recorder.Code)
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	if recorder.Code >= http.StatusBadRequest {
		result.IsError = true
		return result
	}
	if isObjectJSON(body) {
		result.StructuredContent = json.RawMessage(body)
	}
	return result
}

func isObjectJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
