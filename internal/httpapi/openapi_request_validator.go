package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"golang.org/x/text/unicode/norm"
)

const openAPIUnicodeNormalizationExtension = "x-omnara-unicode-normalization"

func newOpenAPIRequestValidator() (middleware, error) {
	openapi3.SchemaErrorDetailsDisabled = true

	spec, err := openapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load generated openapi spec: %w", err)
	}
	spec.Servers = nil
	queryRouter, err := gorillamux.NewRouter(spec)
	if err != nil {
		return nil, fmt.Errorf("build openapi query validator: %w", err)
	}
	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
		ErrorHandlerWithOpts: openAPIValidationErrorHandler,
	})

	return func(next http.Handler) http.Handler {
		validated := validator(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				next.ServeHTTP(w, r)
				return
			}
			if route, _, err := queryRouter.FindRoute(r); err == nil {
				if err := rejectUndeclaredOpenAPIQueryParams(
					r,
					route.PathItem.Parameters,
					route.Operation.Parameters,
				); err != nil {
					writeOpenAPIRequestError(w, r, err)
					return
				}
				if route.Operation.RequestBody == nil {
					if err := rejectUnexpectedOpenAPIRequestBody(r); err != nil {
						writeOpenAPIRequestError(w, r, err)
						return
					}
				} else {
					if err := rejectTrailingJSONRequestBody(r); err != nil {
						writeOpenAPIRequestError(w, r, err)
						return
					}
					if err := normalizeOpenAPIRequestBody(r, route.Operation.RequestBody.Value); err != nil {
						writeOpenAPIRequestError(w, r, err)
						return
					}
				}
			}
			validated.ServeHTTP(w, r)
		})
	}, nil
}

func normalizeOpenAPIRequestBody(r *http.Request, requestBody *openapi3.RequestBody) error {
	if r.Body == nil || requestBody == nil || !isJSONContentType(r.Header.Get("Content-Type")) {
		return nil
	}
	mediaType := requestBody.Content.Get(r.Header.Get("Content-Type"))
	if mediaType == nil || mediaType.Schema == nil {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	value, changed, err := normalizeOpenAPIValue(value, mediaType.Schema, 0)
	if err != nil || !changed {
		return err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode normalized request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(normalized))
	r.ContentLength = int64(len(normalized))
	return nil
}

func normalizeOpenAPIValue(value any, schemaRef *openapi3.SchemaRef, depth int) (any, bool, error) {
	const maximumSchemaDepth = 100
	if schemaRef == nil || schemaRef.Value == nil {
		return value, false, nil
	}
	if depth > maximumSchemaDepth {
		return nil, false, errors.New("OpenAPI schema exceeds normalization depth")
	}
	schema := schemaRef.Value
	if text, ok := value.(string); ok && schema.Extensions[openAPIUnicodeNormalizationExtension] == "NFC" {
		normalized := norm.NFC.String(text)
		return normalized, normalized != text, nil
	}
	changed := false
	for _, nested := range schema.AllOf {
		var nestedChanged bool
		var err error
		value, nestedChanged, err = normalizeOpenAPIValue(value, nested, depth+1)
		if err != nil {
			return nil, false, err
		}
		changed = nestedChanged || changed
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			property := schema.Properties[key]
			if property == nil {
				continue
			}
			normalized, nestedChanged, err := normalizeOpenAPIValue(item, property, depth+1)
			if err != nil {
				return nil, false, err
			}
			typed[key] = normalized
			changed = nestedChanged || changed
		}
	case []any:
		for index, item := range typed {
			normalized, nestedChanged, err := normalizeOpenAPIValue(item, schema.Items, depth+1)
			if err != nil {
				return nil, false, err
			}
			typed[index] = normalized
			changed = nestedChanged || changed
		}
	}
	return value, changed, nil
}

func rejectUnexpectedOpenAPIRequestBody(r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		return errors.New("request body is not allowed")
	}
	return nil
}

func rejectTrailingJSONRequestBody(r *http.Request) error {
	if r.Body == nil || !isJSONContentType(r.Header.Get("Content-Type")) {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(new(json.RawMessage)) == nil && !errors.Is(decoder.Decode(new(json.RawMessage)), io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}
	return nil
}

func isJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func openAPIValidationErrorHandler(
	ctx context.Context,
	err error,
	w http.ResponseWriter,
	r *http.Request,
	opts nethttpmiddleware.ErrorHandlerOpts,
) {
	logpkg.Error(ctx, err)
	switch opts.StatusCode {
	case 0, http.StatusBadRequest:
		writeOpenAPIRequestError(w, r, err)
	case http.StatusNotFound:
		apierror.Write(w, openapi.ErrorCodeNotFound)
	case http.StatusUnauthorized:
		apierror.Write(w, openapi.ErrorCodeUnauthorized, err.Error())
	case http.StatusForbidden:
		apierror.Write(w, openapi.ErrorCodeForbidden, err.Error())
	default:
		apierror.Write(w, openapi.ErrorCodeInternalError)
	}
}

func rejectUndeclaredOpenAPIQueryParams(
	r *http.Request,
	pathParams openapi3.Parameters,
	operationParams openapi3.Parameters,
) error {
	values := r.URL.Query()
	if len(values) == 0 {
		return nil
	}
	allowed := map[string]bool{}
	deepObject := map[string]bool{}
	for _, params := range []openapi3.Parameters{pathParams, operationParams} {
		for _, item := range params {
			if item == nil || item.Value == nil || item.Value.In != openapi3.ParameterInQuery {
				continue
			}
			if item.Value.Style == "deepObject" {
				deepObject[item.Value.Name] = true
				continue
			}
			allowed[item.Value.Name] = true
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if allowed[key] {
			continue
		}
		allowedDeepObjectKey := false
		for name := range deepObject {
			if strings.HasPrefix(key, name+"[") && strings.HasSuffix(key, "]") {
				allowedDeepObjectKey = true
				break
			}
		}
		if allowedDeepObjectKey {
			continue
		}
		return fmt.Errorf("unsupported query parameter: %s", key)
	}
	return nil
}
