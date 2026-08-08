package httpapi

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

type Server struct {
	log                                 *slog.Logger
	store                               *storage.Store
	integrations                        *integration.Service
	skills                              *skillstore.Store
	authLimiter                         httpauth.RateLimiter
	authOAuthStates                     httpauth.OAuthStateStore
	trustedProxyNets                    []*net.IPNet
	email                               httpauth.EmailSender
	authSignupEnabled                   bool
	authResetEnabled                    bool
	authRoutes                          *httpauth.Handler
	publicURL                           string
	publicOrigin                        configuredOrigin
	billingURL                          string
	daemonReleaseURL                    string
	agentEventWakeupSubscriber          notifications.AgentEventWakeupSubscriber
	agentStreamDeltaSubscriber          notifications.AgentStreamDeltaSubscriber
	daemonHub                           *daemonSocketHub
	recorder                            *metrics.HTTPRecorder
	daemonRecorder                      *metrics.DaemonRecorder
	requestLog                          middleware
	agentConfigOptions                  agentconfig.CompileOptions
	allowInsecureModelProviderEndpoints bool
	modelDiscoverer                     modelprovider.DiscoverFunc
	allowInsecureLocalHostBypass        bool
	defaultPools                        []executionstore.DefaultMachinePoolTemplate
	defaultModelProvider                *modelstore.DefaultModelProviderTemplate
	hostedCredentialProvisioner         modelprovider.HostedCredentialProvisioner
	daemonNotifications                 *daemonNotificationConfig
	replyPublisher                      replyChannelPublisher
	mcpOAuthHTTPClient                  *http.Client
	slackOAuth                          SlackOAuthConfig
	secretKeyWrapper                    secrets.KeyWrapper
	authHTTPClient                      *http.Client
	openAPIRequestValidator             middleware
	openAPIAuthorizer                   operationAuthorizer
	webAssets                           fs.FS

	machinePoolManager *machinepool.Manager

	daemonRuntimeLeaseDuration        time.Duration
	daemonSocketFallbackDrainInterval time.Duration
	daemonSocketFallbackDrainJitter   time.Duration
	skillDownloadSigningKey           []byte
}

type daemonNotificationConfig struct {
	subscriber notifications.DaemonWakeupSubscriber
	presence   notifications.DaemonPresenceStore
	replicaID  uuid.UUID
}

type Option func(*Server)

func WithSkillDownloadSigningKey(key []byte) Option {
	return func(s *Server) {
		s.skillDownloadSigningKey = append([]byte(nil), key...)
	}
}

func WithEmailSender(sender httpauth.EmailSender) Option {
	return func(s *Server) {
		s.email = sender
	}
}

func WithPasswordAuthEnabled(signupEnabled, resetEnabled bool) Option {
	return func(s *Server) {
		s.authSignupEnabled = signupEnabled
		s.authResetEnabled = resetEnabled
	}
}

func WithAuthRateLimiter(limiter httpauth.RateLimiter) Option {
	return func(s *Server) {
		s.authLimiter = limiter
	}
}

func WithOAuthStateStore(store httpauth.OAuthStateStore) Option {
	return func(s *Server) {
		s.authOAuthStates = store
	}
}

func WithRedisBackedAuth(client *redistore.Client) Option {
	return func(s *Server) {
		if client == nil {
			return
		}
		WithAuthRateLimiter(httpauth.NewRedisRateLimiter(client))(s)
		WithOAuthStateStore(httpauth.NewRedisOAuthStateStore(client))(s)
	}
}

func WithTrustedProxyCIDRs(cidrs []string) Option {
	return func(s *Server) {
		s.trustedProxyNets = nil
		for _, cidr := range cidrs {
			_, network, err := net.ParseCIDR(cidr)
			if err != nil {
				s.log.Warn("ignoring invalid trusted proxy CIDR", "cidr", cidr, "error", err)
				continue
			}
			s.trustedProxyNets = append(s.trustedProxyNets, network)
		}
	}
}

func WithPublicURL(publicURL string) Option {
	return func(s *Server) {
		s.publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	}
}

func WithBillingURL(billingURL string) Option {
	return func(s *Server) {
		s.billingURL = strings.TrimRight(strings.TrimSpace(billingURL), "/")
	}
}

func WithDaemonReleaseURL(releaseURL string) Option {
	return func(s *Server) {
		s.daemonReleaseURL = strings.TrimSpace(releaseURL)
	}
}

// WithWebAssets configures embedded SPA assets.
func WithWebAssets(assets fs.FS) Option {
	return func(s *Server) {
		s.webAssets = assets
	}
}

func WithSecretKeyWrapper(keyWrapper secrets.KeyWrapper) Option {
	return func(s *Server) {
		s.secretKeyWrapper = keyWrapper
	}
}

func WithDefaultMachinePools(defaultPoolTemplates []executionstore.DefaultMachinePoolTemplate) Option {
	return func(s *Server) {
		s.defaultPools = defaultPoolTemplates
	}
}

func WithDefaultModelProvider(defaultProviderTemplate *modelstore.DefaultModelProviderTemplate) Option {
	return func(s *Server) {
		s.defaultModelProvider = defaultProviderTemplate
	}
}

func WithHostedCredentialProvisioner(provisioner modelprovider.HostedCredentialProvisioner) Option {
	return func(s *Server) {
		s.hostedCredentialProvisioner = provisioner
	}
}

func WithSlackOAuth(config SlackOAuthConfig) Option {
	return func(s *Server) {
		s.slackOAuth = config
	}
}

func WithAuthHTTPClient(client *http.Client) Option {
	return func(s *Server) {
		s.authHTTPClient = client
	}
}

func WithHTTPRecorder(recorder *metrics.HTTPRecorder) Option {
	return func(s *Server) {
		s.recorder = recorder
	}
}

func WithDaemonRecorder(recorder *metrics.DaemonRecorder) Option {
	return func(s *Server) {
		s.daemonRecorder = recorder
	}
}

func WithDaemonNotifications(
	subscriber notifications.DaemonWakeupSubscriber,
	presence notifications.DaemonPresenceStore,
	replicaID uuid.UUID,
) Option {
	return func(s *Server) {
		s.daemonNotifications = &daemonNotificationConfig{
			subscriber: subscriber,
			presence:   presence,
			replicaID:  replicaID,
		}
	}
}

func WithAgentEventWakeupSubscriber(subscriber notifications.AgentEventWakeupSubscriber) Option {
	return func(s *Server) {
		s.agentEventWakeupSubscriber = subscriber
	}
}

func WithAgentStreamDeltaSubscriber(subscriber notifications.AgentStreamDeltaSubscriber) Option {
	return func(s *Server) {
		s.agentStreamDeltaSubscriber = subscriber
	}
}

func WithDaemonReplyPublisher(publisher replyChannelPublisher) Option {
	return func(s *Server) {
		s.replyPublisher = publisher
	}
}

func WithMachinePoolManager(manager *machinepool.Manager) Option {
	return func(s *Server) {
		s.machinePoolManager = manager
	}
}

func WithDaemonSocketFallbackDrainTiming(interval time.Duration, jitter time.Duration) Option {
	return func(s *Server) {
		if interval > 0 {
			s.daemonSocketFallbackDrainInterval = interval
		}
		if jitter >= 0 && (interval <= 0 || jitter <= interval) {
			s.daemonSocketFallbackDrainJitter = jitter
		}
	}
}

func (s *Server) daemonRuntimePresenceTTL() time.Duration {
	ttl := 3 * s.daemonRuntimeHeartbeatAfter()
	if s.daemonRuntimeLeaseDuration < ttl {
		return s.daemonRuntimeLeaseDuration
	}
	return ttl
}

func (s *Server) daemonRuntimeHeartbeatAfter() time.Duration {
	leaseHeartbeat := s.daemonRuntimeLeaseDuration / 3
	if leaseHeartbeat > 0 && leaseHeartbeat < daemonRuntimeHeartbeatInterval {
		return leaseHeartbeat
	}
	return daemonRuntimeHeartbeatInterval
}

func WithAgentConfigOptions(opts agentconfig.CompileOptions) Option {
	return func(s *Server) {
		s.agentConfigOptions = opts
	}
}

func WithAllowInsecureModelProviderEndpoints() Option {
	return func(s *Server) {
		s.allowInsecureModelProviderEndpoints = true
	}
}

// WithModelDiscoverer replaces the default provider-native model discovery,
// e.g. to add catalog enrichment in production or stub discovery in tests.
func WithModelDiscoverer(discoverer modelprovider.DiscoverFunc) Option {
	return func(s *Server) {
		s.modelDiscoverer = discoverer
	}
}

// WithAllowInsecureLocalHostBypass exempts loopback and Docker's
// host.docker.internal alias from the public-host guard, so local
// daemons/containers can reach the API without a matching Host header. Only
// wire this when OMNARA_ALLOW_INSECURE_DEV_DEFAULTS is set.
func WithAllowInsecureLocalHostBypass() Option {
	return func(s *Server) {
		s.allowInsecureLocalHostBypass = true
	}
}

func New(log *slog.Logger, store *storage.Store, opts ...Option) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	server := &Server{
		log:                               log,
		store:                             store,
		requestLog:                        requestLog(log),
		authSignupEnabled:                 true,
		authResetEnabled:                  true,
		daemonRuntimeLeaseDuration:        executionstore.DaemonRuntimeLeaseDuration,
		daemonSocketFallbackDrainInterval: defaultDaemonSocketFallbackDrainInterval,
		daemonSocketFallbackDrainJitter:   defaultDaemonSocketFallbackDrainJitter,
		modelDiscoverer:                   modelprovider.DiscoverModels,
	}
	var authStore httpauth.Store
	var compromiseRevoker httpauth.CompromiseRevoker
	if store != nil {
		server.skills = store.Skills()
		server.integrations = integration.New(store.Execution(), store.Integrations())
		authStore = store.Identity()
		compromiseRevoker = store.AccountSecurity()
	}
	openAPIRequestValidator, err := newOpenAPIRequestValidator()
	if err != nil {
		return nil, fmt.Errorf("create openapi request validator: %w", err)
	}
	server.openAPIRequestValidator = openAPIRequestValidator
	openAPIAuthorizer, err := newOpenAPIAuthorizer()
	if err != nil {
		return nil, fmt.Errorf("create openapi operation authorizer: %w", err)
	}
	server.openAPIAuthorizer = openAPIAuthorizer
	for _, opt := range opts {
		opt(server)
	}
	publicOrigin, err := parseConfiguredOrigin(server.publicURL)
	if err != nil {
		return nil, err
	}
	server.publicOrigin = publicOrigin
	server.authRoutes = httpauth.New(httpauth.Config{
		Log:                  log,
		Store:                authStore,
		CompromiseRevoker:    compromiseRevoker,
		Limiter:              server.authLimiter,
		OAuthStates:          server.authOAuthStates,
		Email:                server.email,
		SignupEnabled:        server.authSignupEnabled,
		ResetEnabled:         server.authResetEnabled,
		PublicURL:            server.publicURL,
		TrustedProxyNets:     server.trustedProxyNets,
		PrincipalFromContext: principalFromContext,
		HTTPClient:           server.authHTTPClient,
	})
	if server.agentEventWakeupSubscriber == nil {
		return nil, fmt.Errorf("agent event wakeup subscriber is required; wire via WithAgentEventWakeupSubscriber")
	}
	if server.agentStreamDeltaSubscriber == nil {
		return nil, fmt.Errorf("agent stream delta subscriber is required; wire via WithAgentStreamDeltaSubscriber")
	}
	if config := server.daemonNotifications; config != nil {
		if store == nil {
			return nil, fmt.Errorf("store is required for daemon notifications")
		}
		var err error
		server.daemonHub, err = newDaemonSocketHub(
			config.subscriber,
			server.replyPublisher,
			config.presence,
			config.replicaID,
			log,
			server.daemonRecorder,
			server.daemonSocketFallbackDrainInterval,
			server.daemonSocketFallbackDrainJitter,
		)
		if err != nil {
			return nil, fmt.Errorf("create daemon socket hub: %w", err)
		}
	}
	server.mcpOAuthHTTPClient = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{
		AllowLoopback: server.agentConfigOptions.AllowInsecureLocalMCPHTTP,
	})
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	middlewares := make([]middleware, 0, 7)
	if s.recorder != nil {
		middlewares = append(middlewares, s.recorder.Middleware(mux))
	}
	middlewares = append(middlewares,
		s.requestLog,
		maxBody(requestBodyLimit),
		s.publicHostGuard,
		s.auth,
		s.openAPIRequestValidator,
	)
	return chain(mux, middlewares...)
}

func (s *Server) CloseDaemonSockets() {
	if s == nil || s.daemonHub == nil {
		return
	}
	s.daemonHub.Close()
}
