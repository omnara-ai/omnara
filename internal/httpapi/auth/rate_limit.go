package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

var errRateLimited = errors.New("auth rate limited")

type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) error
}

type RedisRateLimiter struct {
	client *redistore.Client
}

func NewRedisRateLimiter(client *redistore.Client) *RedisRateLimiter {
	return &RedisRateLimiter{client: client}
}

const authRateLimitScript = `
local limit = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local interval = math.floor(window / limit)
if interval < 1 then
  interval = 1
end
local burst = window - interval
if burst < 0 then
  burst = 0
end
local redis_time = redis.call("TIME")
local now = (redis_time[1] * 1000) + math.floor(redis_time[2] / 1000)
local tat = tonumber(redis.call("GET", KEYS[1]) or "0")
local allow_at = tat - burst
if now < allow_at then
  return 0
end
local new_tat = math.max(tat, now) + interval
local ttl = new_tat - now + burst
redis.call("SET", KEYS[1], new_tat, "PX", ttl)
return 1
`

func (l *RedisRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) error {
	if l == nil || l.client == nil {
		return errors.New("auth limiter unavailable")
	}
	if limit <= 0 || window <= 0 {
		return errors.New("invalid auth rate limit")
	}
	allowed, err := l.client.EvalInt(ctx, authRateLimitScript, []string{key}, limit, int(window/time.Millisecond))
	if err != nil {
		return fmt.Errorf("auth rate limit: %w", err)
	}
	if allowed == 0 {
		return errRateLimited
	}
	return nil
}

type authLimit struct {
	scope   string
	subject string
	limit   int
}

func (h *Handler) requireAuthRateLimits(
	w http.ResponseWriter,
	r *http.Request,
	action, subject string,
	pairLimit, subjectLimit, clientLimit int,
	window time.Duration,
) bool {
	if h.limiter == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return false
	}
	client := h.clientBucket(r)
	return h.requireAuthLimitBuckets(w, r, action, []authLimit{
		{scope: "pair", subject: subject + ":" + client, limit: pairLimit},
		{scope: "subject", subject: subject, limit: subjectLimit},
		{scope: "client", subject: client, limit: clientLimit},
	}, window)
}

func (h *Handler) requireOAuthLoginRateLimits(w http.ResponseWriter, r *http.Request, connectorSlug string) bool {
	if h.limiter == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return false
	}
	client := h.clientBucket(r)
	return h.requireAuthLimitBuckets(w, r, "oauth_login", []authLimit{
		{scope: "pair", subject: connectorSlug + ":" + client, limit: oauthLoginRateLimit},
		{scope: "client", subject: client, limit: oauthLoginClientRateLimit},
	}, authShortWindow)
}

func (h *Handler) requireAuthLimitBuckets(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	limits []authLimit,
	window time.Duration,
) bool {
	for _, authLimit := range limits {
		if authLimit.limit <= 0 {
			apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
			return false
		}
		key := "auth:" + action + ":" + authLimit.scope + ":" + identitystore.HashBearerToken(authLimit.subject)
		if err := h.limiter.Allow(r.Context(), key, authLimit.limit, window); err != nil {
			if errors.Is(err, errRateLimited) {
				logent.AuthRateLimited(r.Context(), action, authLimit.scope)
				apierror.Write(w, openapi.ErrorCodeRateLimited)
				return false
			}
			apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
			return false
		}
	}
	return true
}

func (h *Handler) clientBucket(r *http.Request) string {
	host := remoteHost(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && h.trustsProxy(ip) {
		if forwarded := h.forwardedClientIP(r.Header.Get("X-Forwarded-For")); forwarded != "" {
			return forwarded
		}
		if realIP := headerIP(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}
	if host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return strings.ToLower(r.Host)
}

func (h *Handler) trustsProxy(ip net.IP) bool {
	for _, network := range h.trustedProxyNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}

func (h *Handler) forwardedClientIP(header string) string {
	var fallback string
	parts := strings.Split(header, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := parseHeaderIP(parts[i])
		if ip == nil {
			continue
		}
		raw := ip.String()
		if fallback == "" {
			fallback = raw
		}
		if !h.trustsProxy(ip) {
			return raw
		}
	}
	return fallback
}

func headerIP(header string) string {
	ip := parseHeaderIP(header)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func parseHeaderIP(header string) net.IP {
	return net.ParseIP(strings.TrimSpace(header))
}
