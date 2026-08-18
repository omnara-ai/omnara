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
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

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
				} else if err := rejectTrailingJSONRequestBody(r); err != nil {
					writeOpenAPIRequestError(w, r, err)
					return
				}
			}
			validated.ServeHTTP(w, r)
		})
	}, nil
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
	if err := validateJSONUnicode(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(new(json.RawMessage)) == nil && !errors.Is(decoder.Decode(new(json.RawMessage)), io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}
	return nil
}

func validateJSONUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("request body must be valid UTF-8")
	}
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(raw) {
				continue
			}
			if raw[index+1] != 'u' {
				index++
				continue
			}
			codeUnit, ok := jsonHexCodeUnit(raw[index+2:])
			if !ok {
				continue
			}
			switch {
			case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
				if index+12 > len(raw) || raw[index+6] != '\\' || raw[index+7] != 'u' {
					return errors.New("request body must use valid Unicode scalar values")
				}
				low, ok := jsonHexCodeUnit(raw[index+8:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return errors.New("request body must use valid Unicode scalar values")
				}
				index += 11
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return errors.New("request body must use valid Unicode scalar values")
			default:
				index += 5
			}
		}
	}
	return nil
}

func jsonHexCodeUnit(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, char := range raw[:4] {
		value = value * 16
		switch {
		case char >= '0' && char <= '9':
			value += uint16(char - '0')
		case char >= 'a' && char <= 'f':
			value += uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value += uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
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
