package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SetupConfig uses App credentials without choosing an installation. Omit
// Credentials only for manifest conversion. APIURL is a trusted deployment/test
// setting with Config's restrictions; the guided HTTP adapter must enforce
// github.com. BeforeRequest rechecks caller authority before every HTTP attempt.
type SetupConfig struct {
	Credentials   Credentials
	HTTPClient    *http.Client
	APIURL        string
	BeforeRequest func(context.Context) error
}

// SetupClient performs bounded registration and App-authenticated discovery.
// It never chooses an installation or mints installation access tokens.
type SetupClient struct {
	*appClient
}

func NewSetupClient(config SetupConfig) (*SetupClient, error) {
	client, err := newAppClient(config)
	if err != nil {
		return nil, err
	}
	return &SetupClient{appClient: client}, nil
}

// AppMetadata contains provider-returned identity, not credentials. Browser
// links should be constructed from the verified slug/account at the HTTP layer.
// Conversion preserves an enterprise owner's ID with empty login/type; App
// inspection rejects that unsupported owner before returning browser metadata.
type AppMetadata struct {
	ID    int64  `json:"id"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Owner User   `json:"owner"`
}

// ManifestConversion is secret-bearing. Persist Credentials through secretstore;
// never return this value to the browser or log it.
type ManifestConversion struct {
	App         AppMetadata
	Credentials Credentials
}

type InstallationsPage struct {
	Installations []Installation `json:"installations"`
	NextPage      int            `json:"next_page,omitempty"`
}

// GitHub's enterprise account payload has a slug instead of a user's login and
// type. Decode only the extra discriminator here; common User remains unchanged.
type setupAccount struct {
	User
	Slug string `json:"slug"`
}

func (a setupAccount) enterprise() bool {
	return a.ID > 0 && a.Login == "" && a.Type == "" && validRepositorySegment(a.Slug)
}

type setupApp struct {
	AppMetadata
	Owner setupAccount `json:"owner"`
}

// ConvertManifest redeems a one-time code without authentication. A successful
// response is validated in full before any credential is returned. The POST is
// never retried; a failed response may have consumed the code.
// A valid enterprise owner does not discard one-time credentials: save them, then
// let App inspection report UnsupportedAccount. This does not enable connection.
func (c *SetupClient) ConvertManifest(ctx context.Context, code string) (ManifestConversion, error) {
	if len(code) == 0 || len(code) > 512 {
		return ManifestConversion{}, errors.New("invalid github manifest code")
	}
	for _, ch := range code {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			return ManifestConversion{}, errors.New("invalid github manifest code")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	var result struct {
		setupApp
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", nil, &result, true)
	if err != nil {
		return ManifestConversion{}, err
	}
	if !validAppMetadata(result.setupApp) || result.WebhookSecret == "" {
		return ManifestConversion{}, &APIError{Code: InvalidResponse}
	}
	if _, err := parsePrivateKey(result.PEM); err != nil {
		return ManifestConversion{}, &APIError{Code: InvalidResponse}
	}
	result.AppMetadata.Owner = result.Owner.User
	return ManifestConversion{
		App:         result.AppMetadata,
		Credentials: Credentials{AppID: result.ID, PrivateKeyPEM: result.PEM, WebhookSecret: result.WebhookSecret},
	}, nil
}

func (c *SetupClient) App(ctx context.Context) (AppMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	jwt, err := c.appJWT()
	if err != nil {
		return AppMetadata{}, err
	}
	var app setupApp
	if _, err := c.doJSON(ctx, http.MethodGet, "/app", jwt, nil, &app, false); err != nil {
		return AppMetadata{}, err
	}
	if app.ID != c.appID {
		return AppMetadata{}, &APIError{Code: ScopeMismatch}
	}
	if !validAppMetadata(app) {
		return AppMetadata{}, &APIError{Code: InvalidResponse}
	}
	if app.Owner.enterprise() {
		return AppMetadata{}, &APIError{Code: UnsupportedAccount}
	}
	app.AppMetadata.Owner = app.Owner.User
	return app.AppMetadata, nil
}

// ListInstallations returns one requested page. Every account belongs to the
// authenticated App, but this does not prove any browser user's GitHub access.
// An enterprise installation fails the whole page with UnsupportedAccount;
// candidates are never silently filtered or partially returned.
func (c *SetupClient) ListInstallations(ctx context.Context, options PageOptions) (InstallationsPage, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	options, err := canonicalPageOptions(options)
	if err != nil {
		return InstallationsPage{}, err
	}
	jwt, err := c.appJWT()
	if err != nil {
		return InstallationsPage{}, err
	}
	const path = "/app/installations"
	query := url.Values{"page": {strconv.Itoa(options.Page)}, "per_page": {strconv.Itoa(options.PerPage)}}
	var installations []struct {
		Installation
		Account setupAccount `json:"account"`
	}
	header, err := c.doJSON(ctx, http.MethodGet, path+"?"+query.Encode(), jwt, nil, &installations, false)
	if err != nil {
		return InstallationsPage{}, err
	}
	if installations == nil || len(installations) > options.PerPage {
		return InstallationsPage{}, &APIError{Code: InvalidResponse}
	}
	seen := make(map[int64]bool, len(installations))
	output := make([]Installation, 0, len(installations))
	for _, installation := range installations {
		if installation.AppID != c.appID {
			return InstallationsPage{}, &APIError{Code: ScopeMismatch}
		}
		if installation.ID <= 0 || seen[installation.ID] {
			return InstallationsPage{}, &APIError{Code: InvalidResponse}
		}
		if installation.Account.enterprise() {
			return InstallationsPage{}, &APIError{Code: UnsupportedAccount}
		}
		if !validSetupAccount(installation.Account.User) {
			return InstallationsPage{}, &APIError{Code: InvalidResponse}
		}
		seen[installation.ID] = true
		installation.Installation.Account = installation.Account.User
		output = append(output, installation.Installation)
	}
	next, err := c.nextPage(header.Values("Link"), path, options)
	if err != nil {
		return InstallationsPage{}, err
	}
	return InstallationsPage{Installations: output, NextPage: next}, nil
}

func validAppMetadata(app setupApp) bool {
	return app.ID > 0 && validAppSlug(app.Slug) && len(app.Name) <= 512 &&
		utf8.ValidString(app.Name) && strings.TrimSpace(app.Name) != "" &&
		(validSetupAccount(app.Owner.User) || app.Owner.enterprise())
}

func validSetupAccount(account User) bool {
	return account.ID > 0 && validRepositorySegment(account.Login) &&
		(account.Type == "User" || account.Type == "Organization")
}
