// Package github implements read-only GitHub App connection setup, apart from
// exchanging an OAuth authorization code. Runtime channel operations live in the gateway.
package github

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	defaultAPIURL       = "https://api.github.com"
	defaultAuthorizeURL = "https://github.com/login/oauth/authorize"
	defaultTokenURL     = "https://github.com/login/oauth/access_token"
	setupTimeout        = 30 * time.Second
	responseMaxBytes    = 2 * 1024 * 1024
	pageSize            = 100
)

var (
	ErrInvalidConfig           = errors.New("invalid GitHub setup configuration")
	ErrInvalidInput            = errors.New("invalid GitHub setup input")
	ErrUnsupportedID           = errors.New("GitHub identifier exceeds the supported runtime range")
	ErrInvalidResponse         = errors.New("invalid GitHub setup response")
	ErrResponseTooLarge        = errors.New("GitHub setup response exceeds the 2 MiB limit")
	ErrOAuthExchange           = errors.New("GitHub OAuth code exchange rejected")
	ErrOAuthExpired            = errors.New("GitHub setup user token expired")
	ErrExpiringTokenRequired   = errors.New("GitHub App must enable expiring user tokens")
	ErrAppMismatch             = errors.New("GitHub App identity does not match the registered app")
	ErrInstallationUnavailable = errors.New("GitHub installation is suspended or unavailable")
	ErrMissingPermissions      = errors.New("GitHub installation lacks required communication permissions")
	ErrRepositoryAdminRequired = errors.New("GitHub repository administrator permission required")
	ErrSelectionNotAccessible  = errors.New("GitHub repository is not accessible in the selected installation")
)

// Config contains caller-resolved App credentials. Endpoint/client overrides are
// trusted operator/test configuration, never request, tool, or provider input.
// WebhookSecret is deliberately absent: setup does not receive webhooks.
type Config struct {
	AppID        string
	ClientID     string
	ClientSecret string
	PrivateKey   string
	HTTPClient   *http.Client
	APIURL       string
	AuthorizeURL string
	TokenURL     string
}

type Client struct {
	appID        string
	clientID     string
	clientSecret string
	privateKey   *rsa.PrivateKey
	http         *http.Client
	apiURL       string
	authorizeURL string
	tokenURL     string
}

func NewClient(config Config) (*Client, error) {
	if !validID(config.AppID) || !boundedText(config.ClientID, 256) || !boundedText(config.ClientSecret, 4096) {
		return nil, ErrInvalidConfig
	}
	if !supportedRuntimeID(config.AppID) {
		return nil, ErrUnsupportedID
	}
	key, err := parsePrivateKey(config.PrivateKey)
	if err != nil {
		return nil, err
	}
	apiURL, err := configuredURL(config.APIURL, defaultAPIURL)
	if err != nil {
		return nil, err
	}
	authorizeURL, err := configuredURL(config.AuthorizeURL, defaultAuthorizeURL)
	if err != nil {
		return nil, err
	}
	tokenURL, err := configuredURL(config.TokenURL, defaultTokenURL)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: setupTimeout})
	} else {
		client = outboundhttp.CloneWithoutRedirects(client)
		if client.Timeout <= 0 || client.Timeout > setupTimeout {
			client.Timeout = setupTimeout
		}
	}
	return &Client{
		appID: config.AppID, clientID: config.ClientID, clientSecret: config.ClientSecret,
		privateKey: key, http: client, apiURL: strings.TrimRight(apiURL, "/"),
		authorizeURL: authorizeURL, tokenURL: tokenURL,
	}, nil
}

func configuredURL(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", ErrInvalidConfig
	}
	return parsed.String(), nil
}

func parsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	if len(raw) > 16*1024 {
		return nil, ErrInvalidConfig
	}
	block, rest := pem.Decode([]byte(raw))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, ErrInvalidConfig
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, ErrInvalidConfig
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, ErrInvalidConfig
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, ErrInvalidConfig
		}
	default:
		return nil, ErrInvalidConfig
	}
	if key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, ErrInvalidConfig
	}
	return key, nil
}

func validID(value string) bool {
	if len(value) == 0 || len(value) > 20 || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func boundedText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

// Match github/configuration.ts: Octokit's installation-token path takes numeric
// App/install/repository IDs. Parse exactly first, then reject unsupported IDs;
// never round a real identity into an apparently supported one.
func supportedRuntimeID(value string) bool {
	const largest = "9007199254740991"
	return validID(value) && (len(value) < len(largest) || len(value) == len(largest) && value <= largest)
}
