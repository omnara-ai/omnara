package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

const openAPIBasePath = "/api/v1"

type strictOpenAPIServer struct {
	server *Server
}

var _ openapi.StrictServerInterface = strictOpenAPIServer{}

func (s *Server) strictOpenAPIHandler() openapi.ServerInterface {
	return openapi.NewStrictHandlerWithOptions(
		strictOpenAPIServer{server: s},
		[]openapi.StrictMiddlewareFunc{s.operationMiddleware},
		openapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  openAPIRequestErrorHandler,
			ResponseErrorHandlerFunc: openAPIResponseErrorHandler,
		},
	)
}

func (s *Server) operationMiddleware(next openapi.StrictHandlerFunc, opID string) openapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		policy, ok := s.openAPIAuthorizer.policy(operationID(opID))
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "missing operation policy")
		}
		scopedCtx, err := s.authorizeOperation(ctx, r, policy)
		if err != nil {
			return nil, err
		}
		scopedRequest := r.WithContext(scopedCtx)
		scopedCtx = context.WithValue(scopedCtx, openAPIHTTPRequestContextKey{}, scopedRequest)
		return next(scopedCtx, w, scopedRequest, request)
	}
}

func openAPIRequestErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	writeOpenAPIRequestError(w, r, err)
}

func writeOpenAPIRequestError(w http.ResponseWriter, r *http.Request, err error) {
	logpkg.Error(r.Context(), err)
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		apierror.Write(w, openapi.ErrorCodeRequestTooLarge)
		return
	}
	apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
}

func openAPIResponseErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	logpkg.Error(r.Context(), err)
	apierror.WriteError(w, err)
}

func (s *Server) rejectEncodedSlashPathValues(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range pathWildcardNames(r.Pattern) {
			if strings.Contains(r.PathValue(name), "/") {
				s.notFound(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func pathWildcardNames(pattern string) []string {
	var names []string
	rest := pattern
	for {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			break
		}
		rest = rest[open+1:]
		closeIdx := strings.IndexByte(rest, '}')
		if closeIdx < 0 {
			break
		}
		name := rest[:closeIdx]
		rest = rest[closeIdx+1:]
		if name == "$" || strings.HasSuffix(name, "...") {
			continue
		}
		names = append(names, name)
	}
	return names
}

type openAPIHTTPRequestContextKey struct{}

func openAPIHTTPRequest(ctx context.Context) (*http.Request, bool) {
	r, ok := ctx.Value(openAPIHTTPRequestContextKey{}).(*http.Request)
	return r, ok
}
