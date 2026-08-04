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
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
)

type responseContract struct {
	router      routers.Router
	errorSchema *openapi3.SchemaRef
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
		errorSchema, ok := spec.Components.Schemas["Error"]
		if !ok || errorSchema.Value == nil {
			responseContractErr = errors.New("openapi spec is missing the Error schema")
			return
		}
		responseContractValue = &responseContract{router: router, errorSchema: errorSchema}
	})
	return responseContractValue, responseContractErr
}

type responseContractRecorder struct {
	http.ResponseWriter
	status    int
	decided   bool
	buffering bool
	hijacked  bool
	body      bytes.Buffer
}

func (r *responseContractRecorder) WriteHeader(status int) {
	if !r.decided {
		r.decided = true
		r.status = status
		mediaType, _, err := mime.ParseMediaType(r.Header().Get("Content-Type"))
		r.buffering = err == nil && mediaType == "application/json"
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

// validateResponseContract panics on contract violations so every integration
// request doubles as a response-contract check: 2xx statuses must be declared
// and their JSON bodies must match the operation's schema, while undeclared
// error statuses must still carry the shared Error envelope.
func validateResponseContract(request *http.Request, recorder *responseContractRecorder) {
	if recorder.hijacked || !recorder.decided {
		return
	}
	contract, err := loadResponseContract()
	if err != nil {
		panic(fmt.Sprintf("load openapi response contract: %v", err))
	}
	route, pathParams, err := contract.router.FindRoute(request)
	if err != nil {
		return
	}
	if route.Operation.Responses.Status(recorder.status) == nil {
		if recorder.status >= 200 && recorder.status < 300 {
			panic(fmt.Sprintf(
				"openapi response contract violation: %s %s returned undeclared status %d\nresponse body: %s",
				request.Method, request.URL.Path, recorder.status, recorder.body.Bytes(),
			))
		}
		if recorder.buffering {
			validateResponseErrorEnvelope(contract, request, recorder)
		}
		return
	}
	if !recorder.buffering {
		return
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
}

func validateResponseErrorEnvelope(
	contract *responseContract,
	request *http.Request,
	recorder *responseContractRecorder,
) {
	var decoded any
	if err := json.Unmarshal(recorder.body.Bytes(), &decoded); err != nil {
		panic(fmt.Sprintf(
			"openapi response contract violation: %s %s returned %d with unparsable JSON: %v\nresponse body: %s",
			request.Method, request.URL.Path, recorder.status, err, recorder.body.Bytes(),
		))
	}
	if err := contract.errorSchema.Value.VisitJSON(decoded); err != nil {
		panic(fmt.Sprintf(
			"openapi response contract violation: %s %s returned %d whose body is not an Error envelope: %v\nresponse body: %s",
			request.Method, request.URL.Path, recorder.status, err, recorder.body.Bytes(),
		))
	}
}
