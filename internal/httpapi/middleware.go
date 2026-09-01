package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type principalContextKey struct{}
type browserCSRFHashContextKey struct{}

const maxRequestBodyBytes int64 = 1024 * 1024

// maxAttachmentRequestBodyBytes covers the per-input attachment budget as
// base64 (4/3 expansion) plus headroom for the surrounding JSON.
const maxAttachmentRequestBodyBytes int64 = 2 * modelcontext.MaxResolvedMediaBytes

const maxSkillUploadRequestBodyBytes int64 = int64(skills.MaxArchiveBytes) + 1024*1024

func requestBodyLimit(r *http.Request) int64 {
	if r.Method != http.MethodPost {
		return maxRequestBodyBytes
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/slack-setup"):
		return maxSlackSetupRequestBodyBytes
	case strings.HasSuffix(r.URL.Path, "/inputs"),
		strings.Contains(r.URL.Path, "/tool-calls/") &&
			strings.HasSuffix(r.URL.Path, "/result"):
		return maxAttachmentRequestBodyBytes
	case strings.HasSuffix(r.URL.Path, "/skills"), isSkillUpdatePath(r.URL.Path):
		return maxSkillUploadRequestBodyBytes
	case strings.HasPrefix(r.URL.Path, openAPIBasePath+"/daemon/tool-calls/") &&
		strings.HasSuffix(r.URL.Path, "/artifact"):
		return daemonprotocol.MaxArtifactUploadBytes
	}
	return maxRequestBodyBytes
}

func isSkillUpdatePath(path string) bool {
	idx := strings.LastIndexByte(path, '/')
	if idx < 0 {
		return false
	}
	return path[idx+1:] != "" && strings.HasSuffix(path[:idx], "/skills")
}

type middleware func(http.Handler) http.Handler

func chain(handler http.Handler, middlewares ...middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

func requestLog(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := logpkg.WithLogger(r.Context(), log)
			ctx, rec, event := logpkg.HTTPRequest(ctx, w, r)
			defer event.Done(ctx)
			defer recoverRequestPanic(ctx, rec)

			next.ServeHTTP(rec, r.WithContext(ctx))
		})
	}
}

func recoverRequestPanic(ctx context.Context, rec *logpkg.ResponseRecorder) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(recovered) //nolint:omnaralint // re-raise net/http's abort sentinel
	}
	logpkg.Error(ctx, fmt.Errorf("http handler panicked: %v", recovered))
	logpkg.Attach(ctx, logpkg.Fields{"error.stack": string(debug.Stack())})
	if rec.Started() {
		panic(http.ErrAbortHandler) //nolint:omnaralint // abort the partial response
	}
	apierror.Write(rec, openapi.ErrorCodeInternalError)
}

func maxBody(limit func(*http.Request) int64) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit(r))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		authAttempted := false
		if s.store != nil {
			if strings.HasPrefix(header, prefix) {
				token := strings.TrimPrefix(header, prefix)
				principal, kind, err := s.authenticateBearerToken(r.Context(), token)
				if err == nil {
					logent.Authenticated(r.Context(), principal)
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
					return
				}
				if !errors.Is(err, storeerr.ErrUnauthorized) {
					logent.AuthFailedError(
						r.Context(),
						logent.AuthSchemeBearer,
						kind,
						logent.AuthResultUnavailable,
						err,
					)
					apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
					return
				}
				logent.AuthFailed(
					r.Context(),
					logent.AuthSchemeBearer,
					kind,
					logent.AuthResultUnauthorized,
				)
				apierror.Write(w, openapi.ErrorCodeUnauthorized)
				return
			}
			sessionCookie, cookieErr := httpauth.BrowserSessionCookie(r, s.publicURL)
			if cookieErr == nil {
				authAttempted = true
				principal, csrfHash, err := s.store.Identity().AuthenticateBrowserSession(
					r.Context(),
					sessionCookie.Value,
				)
				if err == nil {
					if mutatesState(r.Method) && !s.validBrowserCSRF(r, csrfHash) {
						logent.AuthFailed(
							r.Context(),
							logent.AuthSchemeCookie,
							logent.TokenKindBrowserSession,
							logent.AuthResultCSRFFailed,
						)
						apierror.Write(w, openapi.ErrorCodeCsrfCheckFailed)
						return
					}
					logent.Authenticated(r.Context(), principal)
					ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
					ctx = context.WithValue(ctx, browserCSRFHashContextKey{}, csrfHash)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				if !errors.Is(err, storeerr.ErrUnauthorized) {
					logent.AuthFailedError(
						r.Context(),
						logent.AuthSchemeCookie,
						logent.TokenKindBrowserSession,
						logent.AuthResultUnavailable,
						err,
					)
					apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
					return
				}
				logent.AuthFailed(
					r.Context(),
					logent.AuthSchemeCookie,
					logent.TokenKindBrowserSession,
					logent.AuthResultUnauthorized,
				)
			}
		}
		if !authAttempted {
			logent.AuthFailed(
				r.Context(),
				logent.AuthSchemeNone,
				logent.TokenKindUnknown,
				logent.AuthResultUnauthorized,
			)
		}
		apierror.Write(w, openapi.ErrorCodeUnauthorized)
	})
}

func (s *Server) authenticateBearerToken(
	ctx context.Context,
	token string,
) (identitystore.PrincipalRecord, logent.TokenKind, error) {
	kind, err := bearertoken.Parse(token)
	if err != nil {
		return identitystore.PrincipalRecord{}, logent.TokenKindUnknown, storeerr.ErrUnauthorized
	}
	switch kind {
	case bearertoken.KindPersonalAccess:
		principal, err := s.store.Identity().AuthenticatePersonalAccessToken(ctx, token)
		return principal, logent.TokenKindPersonalAccess, err
	case bearertoken.KindOrganization:
		principal, err := s.store.Identity().AuthenticateOrgAPIKey(ctx, token)
		return principal, logent.TokenKindOrgAPIKey, err
	case bearertoken.KindDaemon:
		principal, err := s.store.Execution().AuthenticateMachineDaemonToken(ctx, token)
		return principal, logent.TokenKindMachineDaemon, err
	default:
		return identitystore.PrincipalRecord{}, logent.TokenKindUnknown, storeerr.ErrUnauthorized
	}
}

func requiresAuth(path string) bool {
	if strings.HasPrefix(path, openAPIBasePath+"/") {
		return true
	}
	for _, route := range serverManualRouteContracts {
		if route.Pattern == path {
			return route.Access == manualRouteAccessAuthRequired
		}
	}
	for _, route := range httpauth.RouteContracts() {
		if route.Pattern == path {
			return route.RequiresAuth()
		}
	}
	return false
}

func mutatesState(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func (s *Server) validBrowserCSRF(r *http.Request, csrfHash string) bool {
	if csrfHash == "" {
		return false
	}
	if !s.sameOrigin(r) {
		return false
	}
	presented := r.Header.Get(httpauth.CSRFHeaderName)
	if presented == "" {
		return false
	}
	hashed := identitystore.HashBearerToken(presented)
	return subtle.ConstantTimeCompare([]byte(hashed), []byte(csrfHash)) == 1
}

func (s *Server) sameOrigin(r *http.Request) bool {
	return httpauth.SameOrigin(s.publicURL, r)
}
