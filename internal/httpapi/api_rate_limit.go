package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/apimcp"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

type apiRateLimit struct {
	limiter       httpauth.RateLimiter
	readsPerHour  int
	writesPerHour int
	burst         int
}

func WithAPIRateLimit(client *redistore.Client, readsPerHour, writesPerHour, burst int) Option {
	return func(s *Server) {
		s.apiRateLimit = &apiRateLimit{
			limiter:       httpauth.NewRedisRateLimiter(client),
			readsPerHour:  readsPerHour,
			writesPerHour: writesPerHour,
			burst:         burst,
		}
	}
}

func (s *Server) limitAPIRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apimcp.Path && !strings.HasPrefix(r.URL.Path, "/api/auth/") {
			if allowed, retryAfter := s.apiRateLimit.allow(r); !allowed {
				w.Header().Set("Retry-After", retryAfter)
				apierror.Write(w, openapi.ErrorCodeRateLimited, "retry after "+retryAfter+"s")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (l *apiRateLimit) allow(r *http.Request) (bool, string) {
	if l == nil {
		return true, ""
	}
	principal, ok := principalFromContext(r.Context())
	if !ok {
		return true, ""
	}
	account, ok := rateLimitAccount(principal)
	if !ok {
		return true, ""
	}
	bucket, perHour := "write", l.writesPerHour
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		bucket, perHour = "read", l.readsPerHour
	}
	if perHour == 0 {
		return true, ""
	}
	key := "api:" + strconv.Itoa(perHour) + ":" + strconv.Itoa(l.burst) + ":" + account + ":" + bucket
	window := time.Duration(l.burst) * time.Hour / time.Duration(perHour)
	err := l.limiter.Allow(context.WithoutCancel(r.Context()), key, l.burst, window)
	switch {
	case errors.Is(err, httpauth.ErrRateLimited):
		logpkg.Attach(r.Context(), logpkg.Fields{"rate_limit.result": "limited", "rate_limit.account": account})
		return false, strconv.Itoa(int(math.Ceil(time.Hour.Seconds() / float64(perHour))))
	case err != nil:
		logpkg.Attach(r.Context(), logpkg.Fields{
			"rate_limit.result":  "error",
			"rate_limit.account": account,
			"rate_limit.error":   err.Error(),
		})
	}
	return true, ""
}

func rateLimitAccount(principal identitystore.PrincipalRecord) (string, bool) {
	switch principal.Type {
	case identitystore.PrincipalTypeUser:
		return "user:" + principal.ID.String(), true
	case identitystore.PrincipalTypeOrgAPIKey:
		return "org:" + principal.OrgID.String(), true
	case identitystore.PrincipalTypeMachineDaemon:
		return "machine:" + principal.ID.String(), true
	default:
		return "", false
	}
}
