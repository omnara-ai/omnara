package identitystore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/authz"
)

type OrgRecord struct {
	ID             ID        `json:"id"`
	Name           string    `json:"name"`
	IdempotencyKey string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Created        bool      `json:"-"`
}

type CreateOrgForUserRecord struct {
	Org        OrgRecord
	Membership OrgMembershipRecord
	Project    ProjectRecord
	Created    bool
}

type ProvisionOrganizationInput struct {
	OrgID          ID
	UserID         ID
	Name           string
	IdempotencyKey string
}

type GetOrgCreationReplayInput struct {
	UserID         ID
	Name           string
	IdempotencyKey string
}

type CreateProjectForPrincipalInput struct {
	OrgID          ID
	Creator        PrincipalRecord
	Name           string
	IdempotencyKey string
}

type ProjectRecord struct {
	ID             ID        `json:"id"`
	OrgID          ID        `json:"org_id"`
	Name           string    `json:"name"`
	IdempotencyKey string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Created        bool      `json:"-"`
}

type VisibleProjectRecord struct {
	Project ProjectRecord
	Roles   []string
}

const (
	PrincipalTypeUser          = authz.PrincipalUser
	PrincipalTypeOrgAPIKey     = authz.PrincipalOrgAPIKey
	PrincipalTypeSystem        = "system"
	PrincipalTypeMachineDaemon = authz.PrincipalMachineDaemon

	ProjectActionRead          = authz.ProjectRead
	ProjectActionManage        = authz.ProjectManage
	ProjectActionAccessManage  = authz.ProjectAccessManage
	ProjectActionSecretsList   = authz.ProjectSecretsList
	ProjectActionSecretsManage = authz.ProjectSecretsManage
	AgentActionRead            = authz.AgentRead
	AgentActionOperate         = authz.AgentOperate

	ActorProviderOmnara   = "omnara"
	ActorProviderSlack    = "slack"
	ActorProviderExternal = "external"
)

func ProjectRolesAllow(roles []string, action string) bool {
	for _, role := range roles {
		if authz.ProjectRoleAllows(role, action) {
			return true
		}
	}
	return false
}

type CreateUserInput struct {
	DisplayName string
}

type UserRecord struct {
	ID          ID        `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateUserEmailInput struct {
	UserID    ID
	Email     string
	Verified  bool
	IsPrimary bool
}

type UserEmailRecord struct {
	ID              ID         `json:"id"`
	UserID          ID         `json:"user_id"`
	Email           string     `json:"email"`
	NormalizedEmail string     `json:"normalized_email"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
	IsPrimary       bool       `json:"is_primary"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type PasswordSignupStartInput struct {
	Email string
}

type PasswordSignupStartRecord struct {
	User                 UserRecord
	Email                UserEmailRecord
	Token                string
	EmailAlreadyVerified bool
}

type CompletePasswordSignupInput struct {
	Token            string
	PasswordHash     string
	DisplayName      string
	SessionToken     string
	SessionCSRFToken string
	SessionTTL       time.Duration
}

type CompletePasswordSignupRecord struct {
	User     UserRecord
	Verified bool
}

type PasswordResetStartInput struct {
	Email string
}

type PasswordResetStartRecord struct {
	Email string
	Token string
	Found bool
}

type CompletePasswordResetInput struct {
	Token            string
	PasswordHash     string
	SessionToken     string
	SessionCSRFToken string
	SessionTTL       time.Duration
}

type ChangePasswordInput struct {
	UserID           ID
	CurrentPassword  string
	PasswordHash     string
	SessionToken     string
	SessionCSRFToken string
	SessionTTL       time.Duration
}

type PasswordLoginSessionInput struct {
	Email            string
	Password         string
	SessionToken     string
	SessionCSRFToken string
	SessionTTL       time.Duration
}

type CreateUserAuthIdentityInput struct {
	UserID          ID
	AuthConnectorID ID
	Issuer          string
	Subject         string
	EmailAtLink     string
	EmailVerified   bool
}

type UserAuthIdentityRecord struct {
	ID              ID        `json:"id"`
	UserID          ID        `json:"user_id"`
	AuthConnectorID ID        `json:"auth_connector_id"`
	Issuer          string    `json:"issuer"`
	Subject         string    `json:"subject"`
	EmailAtLink     string    `json:"email_at_link"`
	EmailVerified   bool      `json:"email_verified"`
	CreatedAt       time.Time `json:"created_at"`
}

type ResolveAuthIdentityInput struct {
	AuthConnectorID ID
	Issuer          string
	Subject         string
	Email           string
	EmailVerified   bool
	DisplayName     string
}

type CreateAuthConnectorInput struct {
	Slug             string
	Kind             string
	DisplayName      string
	Issuer           string
	AuthorizationURL string
	TokenURL         string
	UserinfoURL      string
	ClientID         string
	ClientSecret     string
	Scopes           []string
	EmailTrustPolicy string
	Enabled          bool
}

type AuthConnectorRecord struct {
	ID               ID
	Slug             string
	Kind             string
	DisplayName      string
	Issuer           string
	AuthorizationURL string
	TokenURL         string
	UserinfoURL      string
	ClientID         string
	ClientSecret     string
	Scopes           []string
	EmailTrustPolicy string
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type AuthConnectorSummaryRecord struct {
	ID          ID
	Slug        string
	Kind        string
	DisplayName string
}

type ResolveAuthIdentitySessionInput struct {
	ResolveAuthIdentityInput
	SessionToken     string
	SessionCSRFToken string
	SessionTTL       time.Duration
}

type StartDeviceAuthFlowInput struct {
	ClientName string
	TokenName  string
}

type DeviceAuthFlowStartRecord struct {
	DeviceCode string
	UserCode   string
	ExpiresIn  time.Duration
	Interval   time.Duration
}

type DeviceAuthFlowStatus string

const (
	DeviceAuthFlowStatusPending  DeviceAuthFlowStatus = "authorization_pending"
	DeviceAuthFlowStatusSlowDown DeviceAuthFlowStatus = "slow_down"
	DeviceAuthFlowStatusDenied   DeviceAuthFlowStatus = "access_denied"
	DeviceAuthFlowStatusExpired  DeviceAuthFlowStatus = "expired_token"
	DeviceAuthFlowStatusApproved DeviceAuthFlowStatus = "approved"
)

type DeviceAuthFlowPollInput struct {
	DeviceCode string
}

type DeviceAuthFlowPollRecord struct {
	Status   DeviceAuthFlowStatus
	Token    string
	Interval time.Duration
}

type DeviceAuthFlowPendingInput struct {
	UserCode string
}

type DeviceAuthFlowPendingRecord struct {
	ClientName string
	TokenName  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type ApproveDeviceAuthFlowInput struct {
	UserCode                 string
	UserID                   ID
	ApprovedBrowserSessionID ID
}

type DenyDeviceAuthFlowInput struct {
	UserCode string
}

type CreateOrgInvitationInput struct {
	OrgID ID
	Email string
	Role  string
}

type OrgInvitationRecord struct {
	ID              ID        `json:"id"`
	OrgID           ID        `json:"org_id"`
	Email           string    `json:"email"`
	NormalizedEmail string    `json:"normalized_email"`
	OrgRole         string    `json:"org_role"`
	CreatedAt       time.Time `json:"created_at"`
}

type OrgInvitationWithOrgNameRecord struct {
	OrgInvitationRecord
	OrgName string `json:"org_name"`
}

type AcceptOrgInvitationInput struct {
	ID     ID
	UserID ID
}

type DeclineOrgInvitationInput struct {
	ID     ID
	UserID ID
}

type AddProjectMembershipInput struct {
	OrgID     ID
	ProjectID ID
	UserID    ID
	Role      string
}

type AddOrgMembershipInput struct {
	OrgID  ID
	UserID ID
	Role   string
}

type OrgMembershipRecord struct {
	ID          ID        `json:"id"`
	OrgID       ID        `json:"org_id"`
	UserID      ID        `json:"user_id,omitempty"`
	OrgAPIKeyID ID        `json:"org_api_key_id,omitempty"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserOrgMembershipRecord is a user's organization membership read projection.
type UserOrgMembershipRecord struct {
	OrgID     ID        `json:"org_id"`
	OrgName   string    `json:"org_name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type OrgMemberRecord struct {
	UserID      ID        `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type ProjectMembershipRecord struct {
	OrgID           ID        `json:"org_id"`
	ProjectID       ID        `json:"project_id"`
	OrgMembershipID ID        `json:"org_membership_id"`
	Role            string    `json:"role"`
	CreatedAt       time.Time `json:"created_at"`
}

type UpdateOrgMemberRoleInput struct {
	OrgID  ID
	UserID ID
	Role   string
}

type RemoveOrgMemberInput struct {
	OrgID  ID
	UserID ID
}

type RemoveProjectMembershipInput struct {
	OrgID     ID
	ProjectID ID
	UserID    ID
}

type ProjectMembershipGrantRecord struct {
	ProjectID   ID        `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

type CreatePersonalAccessTokenInput struct {
	UserID         ID
	ActorPrincipal PrincipalRecord
	Name           string
}

type PersonalAccessTokenRecord struct {
	ID         ID         `json:"id"`
	UserID     ID         `json:"user_id"`
	Name       string     `json:"name"`
	TokenID    string     `json:"token_id"`
	TokenHash  string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type CreateOrgAPIKeyInput struct {
	OrgID           ID
	ActorPrincipal  PrincipalRecord
	CreatedByUserID ID
	Name            string
	OrgRole         string
}

type OrgAPIKeyRecord struct {
	ID              ID         `json:"id"`
	OrgID           ID         `json:"org_id"`
	Name            string     `json:"name"`
	TokenID         string     `json:"token_id"`
	TokenHash       string     `json:"-"`
	OrgRole         string     `json:"org_role"`
	CreatedByUserID ID         `json:"created_by_user_id"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

// PrincipalRecord identifies an authenticated subject or internal actor. ID is
// the subject; credential-specific IDs record how it authenticated.
type PrincipalRecord struct {
	Type                  string
	ID                    ID
	OrgID                 ID
	PersonalAccessTokenID ID
	OrgAPIKeyID           ID
	BrowserSessionID      ID
	MachineDaemonTokenID  ID
}

func NewUserPrincipal(userID ID) PrincipalRecord {
	return PrincipalRecord{Type: PrincipalTypeUser, ID: userID}
}

func NewPersonalAccessTokenPrincipal(userID, tokenID ID) PrincipalRecord {
	return PrincipalRecord{
		Type:                  PrincipalTypeUser,
		ID:                    userID,
		PersonalAccessTokenID: tokenID,
	}
}

func NewBrowserSessionPrincipal(userID, sessionID ID) PrincipalRecord {
	return PrincipalRecord{
		Type:             PrincipalTypeUser,
		ID:               userID,
		BrowserSessionID: sessionID,
	}
}

func NewOrgAPIKeyPrincipal(orgID, keyID ID) PrincipalRecord {
	return PrincipalRecord{
		Type:        PrincipalTypeOrgAPIKey,
		ID:          keyID,
		OrgID:       orgID,
		OrgAPIKeyID: keyID,
	}
}

func NewMachineDaemonPrincipal(orgID, machineID, tokenID ID) PrincipalRecord {
	return PrincipalRecord{
		Type:                 PrincipalTypeMachineDaemon,
		ID:                   machineID,
		OrgID:                orgID,
		MachineDaemonTokenID: tokenID,
	}
}

func AccountPrincipalIDs(principal PrincipalRecord) (userID, orgAPIKeyID *ID) {
	if isNilID(principal.ID) {
		return nil, nil
	}
	id := principal.ID
	switch principal.Type {
	case authz.PrincipalUser:
		return &id, nil
	case authz.PrincipalOrgAPIKey:
		return nil, &id
	default:
		return nil, nil
	}
}

func IsAccountPrincipal(principal PrincipalRecord) bool {
	userID, orgAPIKeyID := AccountPrincipalIDs(principal)
	return userID != nil || orgAPIKeyID != nil
}

type AuthorizeProjectInput struct {
	Principal PrincipalRecord
	OrgID     ID
	ProjectID ID
	Action    string
}

const (
	OrgActionRead          = authz.OrgRead
	OrgActionManage        = authz.OrgManage
	OrgActionOwn           = authz.OrgOwn
	OrgActionSecretsList   = authz.OrgSecretsList
	OrgActionSecretsManage = authz.OrgSecretsManage
)

type AuthorizeOrgInput struct {
	Principal PrincipalRecord
	OrgID     ID
	Action    string
}
