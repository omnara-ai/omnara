package auth

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

type Store interface {
	RevokeBrowserSession(context.Context, string) error
	StartPasswordSignup(
		context.Context,
		identitystore.PasswordSignupStartInput,
	) (identitystore.PasswordSignupStartRecord, error)
	ActiveAuthTokenNormalizedEmail(context.Context, string, string) (string, error)
	CompletePasswordSignup(
		context.Context,
		identitystore.CompletePasswordSignupInput,
	) (identitystore.CompletePasswordSignupRecord, error)
	AuthenticatePasswordAndCreateSession(
		context.Context,
		identitystore.PasswordLoginSessionInput,
	) (identitystore.UserRecord, error)
	StartPasswordReset(
		context.Context,
		identitystore.PasswordResetStartInput,
	) (identitystore.PasswordResetStartRecord, error)
	CompletePasswordReset(context.Context, identitystore.CompletePasswordResetInput) (identitystore.UserRecord, error)
	PrimaryVerifiedEmailForUser(context.Context, identitystore.ID) (identitystore.UserEmailRecord, bool, error)
	ChangePassword(context.Context, identitystore.ChangePasswordInput) (identitystore.UserRecord, error)
	ListEnabledAuthConnectorSummaries(context.Context) ([]identitystore.AuthConnectorSummaryRecord, error)
	GetEnabledAuthConnectorBySlug(context.Context, string) (identitystore.AuthConnectorRecord, error)
	ResolveAuthIdentityUserAndCreateSession(
		context.Context,
		identitystore.ResolveAuthIdentitySessionInput,
	) (identitystore.UserRecord, error)
	StartDeviceAuthFlow(
		context.Context,
		identitystore.StartDeviceAuthFlowInput,
	) (identitystore.DeviceAuthFlowStartRecord, error)
	PollDeviceAuthFlow(
		context.Context,
		identitystore.DeviceAuthFlowPollInput,
	) (identitystore.DeviceAuthFlowPollRecord, error)
	ApproveDeviceAuthFlow(context.Context, identitystore.ApproveDeviceAuthFlowInput) error
	DenyDeviceAuthFlow(context.Context, identitystore.DenyDeviceAuthFlowInput) error
	PendingDeviceAuthFlow(
		context.Context,
		identitystore.DeviceAuthFlowPendingInput,
	) (identitystore.DeviceAuthFlowPendingRecord, error)
}

type CompromiseRevoker interface {
	RevokeUserTokensForCompromiseWithPasswordIfPresent(context.Context, identitystore.ID, string) error
}

type EmailSender interface {
	SendInvite(context.Context, string, string) error
	SendEmailVerification(context.Context, string, string) error
	SendPasswordReset(context.Context, string, string) error
	SendAccountExists(context.Context, string, string) error
	SendPasswordChangedNotice(context.Context, string) error
}

type PrincipalFunc func(context.Context) (identitystore.PrincipalRecord, bool)

type Handler struct {
	log                  *slog.Logger
	store                Store
	compromiseRevoker    CompromiseRevoker
	limiter              RateLimiter
	oauthStates          OAuthStateStore
	email                EmailSender
	signupEnabled        bool
	resetEnabled         bool
	publicURL            string
	trustedProxyNets     []*net.IPNet
	principalFromContext PrincipalFunc
	httpClient           *http.Client
}

type Config struct {
	Log                  *slog.Logger
	Store                Store
	CompromiseRevoker    CompromiseRevoker
	Limiter              RateLimiter
	OAuthStates          OAuthStateStore
	Email                EmailSender
	SignupEnabled        bool
	ResetEnabled         bool
	PublicURL            string
	TrustedProxyNets     []*net.IPNet
	PrincipalFromContext PrincipalFunc
	HTTPClient           *http.Client
}

type RouteAccess string

const (
	RouteAccessPublicSameOrigin RouteAccess = "public-same-origin"
	RouteAccessPublicAuth       RouteAccess = "public-auth"
	RouteAccessBrowserSession   RouteAccess = "browser-session"
	RouteAccessOAuthState       RouteAccess = "oauth-state"
)

type RouteContract struct {
	Method  string
	Pattern string
	Access  RouteAccess
}

const defaultOutboundHTTPTimeout = 30 * time.Second

func (r RouteContract) RequiresAuth() bool {
	return r.Access == RouteAccessBrowserSession
}

var authRouteContracts = []RouteContract{
	{Method: http.MethodGet, Pattern: OAuthAuthorizationServerMetadataPath, Access: RouteAccessPublicAuth},
	{Method: http.MethodPost, Pattern: "/api/auth/signup", Access: RouteAccessPublicSameOrigin},
	{Method: http.MethodPost, Pattern: "/api/auth/login", Access: RouteAccessPublicSameOrigin},
	{Method: http.MethodPost, Pattern: "/api/auth/logout", Access: RouteAccessBrowserSession},
	{Method: http.MethodPost, Pattern: "/api/auth/email/verify/request", Access: RouteAccessPublicSameOrigin},
	{Method: http.MethodPost, Pattern: "/api/auth/email/verify", Access: RouteAccessPublicSameOrigin},
	{Method: http.MethodPost, Pattern: "/api/auth/password/reset/request", Access: RouteAccessPublicSameOrigin},
	{Method: http.MethodPost, Pattern: "/api/auth/password/reset", Access: RouteAccessPublicSameOrigin},
	{Method: http.MethodPost, Pattern: "/api/auth/password/change", Access: RouteAccessBrowserSession},
	{Method: http.MethodPost, Pattern: "/api/auth/security/revoke-all", Access: RouteAccessBrowserSession},
	{Method: http.MethodGet, Pattern: authConnectorsPath, Access: RouteAccessPublicAuth},
	{Method: http.MethodGet, Pattern: authConnectorLoginPathPattern, Access: RouteAccessOAuthState},
	{Method: http.MethodGet, Pattern: authConnectorCallbackPattern, Access: RouteAccessOAuthState},
	{Method: http.MethodPost, Pattern: OAuthDeviceAuthorizationPath, Access: RouteAccessPublicAuth},
	{Method: http.MethodPost, Pattern: OAuthTokenPath, Access: RouteAccessPublicAuth},
	{Method: http.MethodGet, Pattern: "/api/auth/device/pending", Access: RouteAccessBrowserSession},
	{Method: http.MethodPost, Pattern: "/api/auth/device/approve", Access: RouteAccessBrowserSession},
	{Method: http.MethodPost, Pattern: "/api/auth/device/deny", Access: RouteAccessBrowserSession},
}

func RouteContracts() []RouteContract {
	return append([]RouteContract(nil), authRouteContracts...)
}

func New(config Config) *Handler {
	return &Handler{
		log:                  config.Log,
		store:                config.Store,
		compromiseRevoker:    config.CompromiseRevoker,
		limiter:              config.Limiter,
		oauthStates:          config.OAuthStates,
		email:                config.Email,
		signupEnabled:        config.SignupEnabled,
		resetEnabled:         config.ResetEnabled,
		publicURL:            config.PublicURL,
		trustedProxyNets:     config.TrustedProxyNets,
		principalFromContext: config.PrincipalFromContext,
		httpClient:           httpClientWithoutRedirects(config.HTTPClient),
	}
}

func httpClientWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: defaultOutboundHTTPTimeout}
	}
	return outboundhttp.CloneWithoutRedirects(client)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", h.authorizationServerMetadataRoute)
	mux.HandleFunc("POST /api/auth/signup", h.passwordSignupRoute)
	mux.HandleFunc("POST /api/auth/login", h.passwordLoginRoute)
	mux.HandleFunc("POST /api/auth/logout", h.logoutRoute)
	mux.HandleFunc("POST /api/auth/email/verify/request", h.resendEmailVerificationRoute)
	mux.HandleFunc("POST /api/auth/email/verify", h.completeEmailVerificationRoute)
	mux.HandleFunc("POST /api/auth/password/reset/request", h.requestPasswordResetRoute)
	mux.HandleFunc("POST /api/auth/password/reset", h.completePasswordResetRoute)
	mux.HandleFunc("POST /api/auth/password/change", h.changePasswordRoute)
	mux.HandleFunc("POST /api/auth/security/revoke-all", h.revokeAllAuthTokensRoute)
	mux.HandleFunc("GET /api/auth/connectors", h.listConnectorsRoute)
	mux.HandleFunc("GET /api/auth/connectors/{connector}/login", h.connectorLoginRoute)
	mux.HandleFunc("GET /api/auth/connectors/{connector}/callback", h.connectorCallbackRoute)
	mux.HandleFunc("POST /api/auth/device/code", h.startDeviceAuthRoute)
	mux.HandleFunc("POST /api/auth/device/token", h.pollDeviceAuthRoute)
	mux.HandleFunc("GET /api/auth/device/pending", h.pendingDeviceAuthRoute)
	mux.HandleFunc("POST /api/auth/device/approve", h.approveDeviceAuthRoute)
	mux.HandleFunc("POST /api/auth/device/deny", h.denyDeviceAuthRoute)
}
