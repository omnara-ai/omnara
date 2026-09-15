package secretstore

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
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

	SecretKindGeneric                = secrets.KindGeneric
	SecretKindOAuthTokenSet          = secrets.KindOAuthTokenSet
	SecretKindSlackAppCredentials    = secrets.KindSlackAppCredentials
	SecretKindAWSCredentials         = secrets.KindAWSCredentials
	SecretKindIntegrationCredentials = secrets.KindIntegrationCredentials

	MaxSecretMetadataBytes = 16 * 1024
)

type SecretRecord struct {
	ID                   uuid.UUID       `json:"id"`
	OrgID                uuid.UUID       `json:"org_id"`
	ManagementKind       management.Kind `json:"management_kind"`
	OwnerKind            string          `json:"owner_kind"`
	OwnerProjectID       uuid.UUID       `json:"owner_project_id,omitempty"`
	OwnerUserID          uuid.UUID       `json:"owner_user_id,omitempty"`
	Name                 string          `json:"name"`
	Kind                 secrets.Kind    `json:"kind"`
	Metadata             json.RawMessage `json:"metadata"`
	CurrentVersionID     uuid.UUID       `json:"current_version_id"`
	CurrentVersionNumber int32           `json:"current_version_number"`
	PayloadKeys          []string        `json:"payload_keys"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type SecretVersionRecord struct {
	ID                uuid.UUID `json:"id"`
	OrgID             uuid.UUID `json:"org_id"`
	SecretID          uuid.UUID `json:"secret_id"`
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

type AssociatedSecretPayload struct {
	Kind    secrets.Kind
	Payload secrets.Payload
}

type SecretGrantRecord struct {
	ID              uuid.UUID `json:"id"`
	OrgID           uuid.UUID `json:"org_id"`
	SecretID        uuid.UUID `json:"secret_id"`
	TargetProjectID uuid.UUID `json:"target_project_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type SecretGrantListRecord struct {
	Grant         SecretGrantRecord
	TargetProject ProjectRecord
}

type SecretOwner struct {
	Kind      string
	ProjectID uuid.UUID
	UserID    uuid.UUID
}

type SecretAvailability struct {
	Source    string
	ProjectID uuid.UUID
	GrantID   uuid.UUID
}

type ProjectSecretAccessRecord struct {
	Secret       SecretRecord
	Availability SecretAvailability
}

type CreateSecretInput struct {
	OrgID          uuid.UUID
	ManagementKind management.Kind
	OwnerKind      string
	OwnerProjectID uuid.UUID
	OwnerUserID    uuid.UUID
	Name           string
	Metadata       resourcemeta.Metadata
	Material       secrets.Material
	Actor          identitystore.PrincipalRecord
	MCPOAuthFlowID uuid.UUID
}

type SecretListFilters struct {
	Metadata            map[string]string
	OwnerKind           string
	OwnerProjectID      uuid.UUID
	MCPOAuthFlowID      uuid.UUID
	Availability        string
	AvailabilitySources []string
	Kinds               []string
}

type ListSecretsInput struct {
	OrgID   uuid.UUID
	Actor   identitystore.PrincipalRecord
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
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Filters   SecretListFilters
	Limit     int
	List      listing.Options
}

type ListProjectAvailableSecretsForPrincipalInput struct {
	ListProjectAvailableSecretsInput
	Actor identitystore.PrincipalRecord
}

type ListSecretGrantsInput struct {
	OrgID           uuid.UUID
	SecretID        uuid.UUID
	Actor           identitystore.PrincipalRecord
	Limit           int
	TargetProjectID uuid.UUID
	List            listing.Options
}

type ListSecretGrantsResult struct {
	Grants  []SecretGrantListRecord
	HasMore bool
	Next    listing.Cursor
}

type UpdateSecretMetadataInput struct {
	OrgID    uuid.UUID
	SecretID uuid.UUID
	Name     string
	Metadata resourcemeta.Metadata
	Actor    identitystore.PrincipalRecord
}

type CreateSecretVersionInput struct {
	OrgID          uuid.UUID
	SecretID       uuid.UUID
	Material       secrets.Material
	Actor          identitystore.PrincipalRecord
	SecretMetadata resourcemeta.Metadata
	MCPOAuthFlowID uuid.UUID
}

type DeleteSecretInput struct {
	OrgID    uuid.UUID
	SecretID uuid.UUID
	Actor    identitystore.PrincipalRecord
}

type CreateSecretGrantInput struct {
	OrgID           uuid.UUID
	SecretID        uuid.UUID
	TargetProjectID uuid.UUID
	Actor           identitystore.PrincipalRecord
}

type DeleteSecretGrantInput struct {
	OrgID    uuid.UUID
	SecretID uuid.UUID
	GrantID  uuid.UUID
	Actor    identitystore.PrincipalRecord
}

type RewrapSecretVersionsByKeyIDResult struct {
	Scanned   int
	Rewrapped int
	Remaining int
}

type AuthorizeSecretForProjectReferenceInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	SecretID  uuid.UUID
}

type ReadProjectAvailableSecretPayloadInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	SecretID  uuid.UUID
	Kind      secrets.Kind
}

type ReadOrgOwnedSecretPayloadInput struct {
	OrgID          uuid.UUID
	SecretID       uuid.UUID
	ManagementKind management.Kind
	Kind           secrets.Kind
}

type RotateProjectAvailableOAuthSecretInput struct {
	ProjectID uuid.UUID
	Lease     OAuthRefreshLeaseRecord
	Material  secrets.OAuthTokenSetMaterial
}

type SecretPayloadRecord struct {
	Payload                   secrets.Payload
	CurrentVersionID          uuid.UUID
	OAuthAccessTokenExpires   bool
	OAuthAccessTokenRemaining time.Duration
}

type AcquireProjectOAuthRefreshLeaseInput struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	SecretID  uuid.UUID
	TTL       time.Duration
}

type OAuthRefreshLeaseRecord struct {
	OrgID                    uuid.UUID
	SecretID                 uuid.UUID
	OwnerToken               uuid.UUID
	ExpectedCurrentVersionID uuid.UUID
}
