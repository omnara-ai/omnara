package secretstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"

	"github.com/omnara-ai/omnara/internal/resourcemeta"
)

const (
	SecretOwnerOrg     = "org"
	SecretOwnerProject = "project"
	SecretOwnerUser    = "user"

	SecretAvailabilityDirect = "direct"
	SecretAvailabilityGrant  = "grant"

	SecretKindGeneric             = secrets.KindGeneric
	SecretKindOAuthTokenSet       = secrets.KindOAuthTokenSet
	SecretKindSlackAppCredentials = secrets.KindSlackAppCredentials
	SecretKindAWSCredentials      = secrets.KindAWSCredentials

	MaxSecretMetadataBytes = 16 * 1024
)

type SecretRecord struct {
	ID                   ID              `json:"id"`
	OrgID                ID              `json:"org_id"`
	ManagementKind       management.Kind `json:"management_kind"`
	OwnerKind            string          `json:"owner_kind"`
	OwnerProjectID       ID              `json:"owner_project_id,omitempty"`
	OwnerUserID          ID              `json:"owner_user_id,omitempty"`
	Name                 string          `json:"name"`
	Kind                 secrets.Kind    `json:"kind"`
	Metadata             json.RawMessage `json:"metadata"`
	CurrentVersionID     ID              `json:"current_version_id"`
	CurrentVersionNumber int32           `json:"current_version_number"`
	PayloadKeys          []string        `json:"payload_keys"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type SecretVersionRecord struct {
	ID                ID        `json:"id"`
	OrgID             ID        `json:"org_id"`
	SecretID          ID        `json:"secret_id"`
	VersionNumber     int32     `json:"version_number"`
	PayloadKeys       []string  `json:"payload_keys"`
	EncryptionScheme  string    `json:"-"`
	KeyID             string    `json:"-"`
	DEKWrappedBy      string    `json:"-"`
	EncryptedDEK      []byte    `json:"-"`
	EncryptedDEKNonce []byte    `json:"-"`
	Nonce             []byte    `json:"-"`
	Ciphertext        []byte    `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
}

type SecretGrantRecord struct {
	ID              ID        `json:"id"`
	OrgID           ID        `json:"org_id"`
	SecretID        ID        `json:"secret_id"`
	TargetProjectID ID        `json:"target_project_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type SecretGrantListRecord struct {
	Grant         SecretGrantRecord
	TargetProject ProjectRecord
}

type SecretOwner struct {
	Kind      string
	ProjectID ID
	UserID    ID
}

type SecretAvailability struct {
	Source    string
	ProjectID ID
	GrantID   ID
}

type ProjectSecretAccessRecord struct {
	Secret       SecretRecord
	Availability SecretAvailability
}

type CreateSecretInput struct {
	OrgID          ID
	ManagementKind management.Kind
	OwnerKind      string
	OwnerProjectID ID
	OwnerUserID    ID
	Name           string
	Metadata       resourcemeta.Metadata
	Material       secrets.Material
	Actor          PrincipalRecord
	MCPOAuthFlowID ID
}

type SecretListFilters struct {
	Metadata            map[string]string
	OwnerKind           string
	OwnerProjectID      ID
	MCPOAuthFlowID      ID
	Availability        string
	AvailabilitySources []string
	Kinds               []string
}

type ListSecretsInput struct {
	OrgID   ID
	Actor   PrincipalRecord
	Filters SecretListFilters
	Limit   int
	List    listing.Options
}

type ListSecretsResult struct {
	Secrets []SecretRecord
	HasMore bool
	Next    listing.Cursor
}

type ListProjectSecretAccessesResult struct {
	Accesses []ProjectSecretAccessRecord
	HasMore  bool
	Next     listing.Cursor
}

type ListProjectAvailableSecretsInput struct {
	OrgID     ID
	ProjectID ID
	Filters   SecretListFilters
	Limit     int
	List      listing.Options
}

type ListProjectAvailableSecretsForPrincipalInput struct {
	ListProjectAvailableSecretsInput
	Actor PrincipalRecord
}

type ListSecretGrantsInput struct {
	OrgID           ID
	SecretID        ID
	Actor           PrincipalRecord
	Limit           int
	TargetProjectID ID
	List            listing.Options
}

type ListSecretGrantsResult struct {
	Grants  []SecretGrantListRecord
	HasMore bool
	Next    listing.Cursor
}

type UpdateSecretMetadataInput struct {
	OrgID    ID
	SecretID ID
	Name     string
	Metadata resourcemeta.Metadata
	Actor    PrincipalRecord
}

type CreateSecretVersionInput struct {
	OrgID          ID
	SecretID       ID
	Material       secrets.Material
	Actor          PrincipalRecord
	SecretMetadata resourcemeta.Metadata
	MCPOAuthFlowID ID
}

type DeleteSecretInput struct {
	OrgID    ID
	SecretID ID
	Actor    PrincipalRecord
}

type CreateSecretGrantInput struct {
	OrgID           ID
	SecretID        ID
	TargetProjectID ID
	Actor           PrincipalRecord
}

type DeleteSecretGrantInput struct {
	OrgID    ID
	SecretID ID
	GrantID  ID
	Actor    PrincipalRecord
}

type RewrapSecretVersionsByKeyIDResult struct {
	Scanned   int
	Rewrapped int
	Remaining int
}

type AuthorizeSecretForProjectReferenceInput struct {
	OrgID     ID
	ProjectID ID
	SecretID  ID
}

type ReadProjectAvailableSecretPayloadInput struct {
	OrgID     ID
	ProjectID ID
	SecretID  ID
	Kind      secrets.Kind
}

type ReadOrgOwnedSecretPayloadInput struct {
	OrgID          ID
	SecretID       ID
	ManagementKind management.Kind
	Kind           secrets.Kind
}

type RotateProjectAvailableOAuthSecretInput struct {
	ProjectID ID
	Lease     OAuthRefreshLeaseRecord
	Material  secrets.OAuthTokenSetMaterial
}

type SecretPayloadRecord struct {
	Payload                   secrets.Payload
	CurrentVersionID          ID
	OAuthAccessTokenExpires   bool
	OAuthAccessTokenRemaining time.Duration
}

type AcquireProjectOAuthRefreshLeaseInput struct {
	OrgID     ID
	ProjectID ID
	SecretID  ID
	TTL       time.Duration
}

type OAuthRefreshLeaseRecord struct {
	OrgID                    ID
	SecretID                 ID
	OwnerToken               ID
	ExpectedCurrentVersionID ID
}
