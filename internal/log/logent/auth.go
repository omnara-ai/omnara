package logent

import (
	"context"

	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

type AuthScheme string

const (
	AuthSchemeBearer AuthScheme = "bearer"
	AuthSchemeCookie AuthScheme = "cookie"
	AuthSchemeNone   AuthScheme = "none"
)

type TokenKind string

const (
	TokenKindPersonalAccess TokenKind = "personal_access_token"
	TokenKindOrgAPIKey      TokenKind = "org_api_key"
	TokenKindMachineDaemon  TokenKind = "machine_daemon_token"
	TokenKindBrowserSession TokenKind = "browser_session"
	TokenKindUnknown        TokenKind = "unknown"
)

type AuthResult string

const (
	AuthResultUnauthorized AuthResult = "unauthorized"
	AuthResultUnavailable  AuthResult = "unavailable"
	AuthResultCSRFFailed   AuthResult = "csrf_failed"
)

type ProjectAuthResult string

const (
	ProjectAuthAllowed    ProjectAuthResult = "allowed"
	ProjectAuthForbidden  ProjectAuthResult = "forbidden"
	ProjectAuthNotVisible ProjectAuthResult = "not_visible"
)

type OrgAuthResult string

const (
	OrgAuthAllowed    OrgAuthResult = "allowed"
	OrgAuthForbidden  OrgAuthResult = "forbidden"
	OrgAuthNotVisible OrgAuthResult = "not_visible"
)

type MachineAuthResult string

const (
	MachineAuthAllowed   MachineAuthResult = "allowed"
	MachineAuthForbidden MachineAuthResult = "forbidden"
)

func Authenticated(ctx context.Context, p identitystore.PrincipalRecord) {
	log.Attach(ctx, principal(p), log.Fields{"auth.result": "authenticated"})
}

func AuthFailed(ctx context.Context, scheme AuthScheme, kind TokenKind, result AuthResult) {
	log.Attach(ctx, log.Fields{
		"auth.scheme":     string(scheme),
		"auth.token_kind": string(kind),
		"auth.result":     string(result),
	})
}

func AuthFailedError(ctx context.Context, scheme AuthScheme, kind TokenKind, result AuthResult, err error) {
	AuthFailed(ctx, scheme, kind, result)
	if err != nil {
		log.Attach(ctx, log.Fields{"auth.error": err.Error()})
		log.Error(ctx, err)
	}
}

func OrgAuthorization(ctx context.Context, in identitystore.AuthorizeOrgInput, result OrgAuthResult) {
	log.Attach(ctx, principal(in.Principal), log.Fields{
		"org.id":               in.OrgID,
		"authorization.action": in.Action,
		"authorization.result": string(result),
	})
	if result != OrgAuthAllowed {
		log.Level(ctx, log.WarnLevel)
	}
}

func ProjectAuthorization(ctx context.Context, in identitystore.AuthorizeProjectInput, result ProjectAuthResult) {
	log.Attach(ctx, principal(in.Principal), log.Fields{
		"org.id":               in.OrgID,
		"project.id":           in.ProjectID,
		"authorization.action": in.Action,
		"authorization.result": string(result),
	})
	if result != ProjectAuthAllowed {
		log.Level(ctx, log.WarnLevel)
	}
}

func MachineAuthorization(ctx context.Context, in executionstore.AuthorizeMachineInput, result MachineAuthResult) {
	log.Attach(ctx, principal(in.Principal), log.Fields{
		"org.id":               in.OrgID,
		"machine.id":           in.MachineID,
		"authorization.action": in.Action,
		"authorization.result": string(result),
	})
	if result != MachineAuthAllowed {
		log.Level(ctx, log.WarnLevel)
	}
}

func AuthorizationCheckFailed(ctx context.Context, err error) {
	fields := log.Fields{"authorization.result": "unavailable"}
	if err != nil {
		fields["authorization.error"] = err.Error()
		log.Error(ctx, err)
	}
	log.Attach(ctx, fields)
}

func principal(p identitystore.PrincipalRecord) log.Fields {
	f := log.Fields{
		"principal.type":           p.Type,
		"principal.id":             p.ID,
		"org.id":                   p.OrgID,
		"personal_access_token.id": p.PersonalAccessTokenID,
		"org_api_key.id":           p.OrgAPIKeyID,
		"browser_session.id":       p.BrowserSessionID,
		"machine_daemon_token.id":  p.MachineDaemonTokenID,
	}
	if p.Type == identitystore.PrincipalTypeMachineDaemon {
		f["machine.id"] = p.ID
	}
	return f
}
