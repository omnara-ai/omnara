package secretstore

import (
	"encoding/json"
	"time"

	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

func secretFromGet(row dbsqlc.GetSecretRow) SecretRecord {
	return secretRecord(
		row.ID,
		row.OrgID,
		management.Kind(row.ManagementKind),
		row.OwnerKind,
		row.OwnerProjectID,
		row.OwnerUserID,
		row.Name,
		secrets.Kind(row.Kind),
		row.Metadata,
		idFromSQLCPtr(row.CurrentVersionID),
		row.CurrentVersionNumber,
		row.PayloadKeys,
		row.CreatedAt,
		row.UpdatedAt,
	)
}

func secretFromGetByOwnerName(row dbsqlc.GetSecretByOwnerNameRow) SecretRecord {
	return secretFromGet(dbsqlc.GetSecretRow(row))
}

func secretFromListVisible(row dbsqlc.ListVisibleOwnedSecretsRow) SecretRecord {
	return secretRecord(
		row.ID, row.OrgID, management.Kind(row.ManagementKind), row.OwnerKind, row.OwnerProjectID, row.OwnerUserID,
		row.Name, secrets.Kind(row.Kind), row.Metadata, idFromSQLCPtr(row.CurrentVersionID),
		row.CurrentVersionNumber,
		row.PayloadKeys, row.CreatedAt, row.UpdatedAt,
	)
}

func secretAccessFromListProject(row dbsqlc.ListProjectAvailableSecretsRow, projectID ID) ProjectSecretAccessRecord {
	secret := secretRecord(
		row.ID,
		row.OrgID,
		management.Kind(row.ManagementKind),
		row.OwnerKind,
		row.OwnerProjectID,
		row.OwnerUserID,
		row.Name,
		secrets.Kind(row.Kind),
		row.Metadata,
		idFromSQLCPtr(row.CurrentVersionID),
		row.CurrentVersionNumber,
		row.PayloadKeys,
		row.CreatedAt,
		row.UpdatedAt,
	)
	return projectSecretAccess(secret, projectID, row.GrantID)
}

func secretAccessFromProjectAvailable(row dbsqlc.GetProjectAvailableSecretRow, projectID ID) ProjectSecretAccessRecord {
	secret := secretRecord(
		row.ID,
		row.OrgID,
		management.Kind(row.ManagementKind),
		row.OwnerKind,
		row.OwnerProjectID,
		row.OwnerUserID,
		row.Name,
		secrets.Kind(row.Kind),
		row.Metadata,
		idFromSQLCPtr(row.CurrentVersionID),
		row.CurrentVersionNumber,
		row.PayloadKeys,
		row.CreatedAt,
		row.UpdatedAt,
	)
	return projectSecretAccess(secret, projectID, row.GrantID)
}

func projectSecretAccess(secret SecretRecord, projectID ID, grantID *ID) ProjectSecretAccessRecord {
	availability := SecretAvailability{Source: SecretAvailabilityDirect, ProjectID: projectID}
	if grantID != nil {
		availability.Source = SecretAvailabilityGrant
		availability.GrantID = *grantID
	}
	return ProjectSecretAccessRecord{Secret: secret, Availability: availability}
}

func secretRecord(
	id, orgID ID,
	managementKind management.Kind,
	ownerKind string,
	ownerProjectID, ownerUserID *ID,
	name string,
	kind secrets.Kind,
	metadata json.RawMessage,
	currentVersionID ID,
	currentVersionNumber int32,
	payloadKeys []string,
	createdAt, updatedAt time.Time,
) SecretRecord {
	return SecretRecord{
		ID:                   id,
		OrgID:                orgID,
		ManagementKind:       managementKind,
		OwnerKind:            ownerKind,
		OwnerProjectID:       idFromSQLCPtr(ownerProjectID),
		OwnerUserID:          idFromSQLCPtr(ownerUserID),
		Name:                 name,
		Kind:                 kind,
		Metadata:             metadata,
		CurrentVersionID:     currentVersionID,
		CurrentVersionNumber: currentVersionNumber,
		PayloadKeys:          append([]string(nil), payloadKeys...),
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}
}

func secretVersionFromSQLC(row dbsqlc.InsertSecretVersionRow) SecretVersionRecord {
	return SecretVersionRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		SecretID:          row.SecretID,
		VersionNumber:     row.VersionNumber,
		PayloadKeys:       append([]string(nil), row.PayloadKeys...),
		EncryptionScheme:  row.EncryptionScheme,
		KeyID:             row.KeyID,
		DEKWrappedBy:      row.DekWrappedBy,
		EncryptedDEK:      append([]byte(nil), row.EncryptedDek...),
		EncryptedDEKNonce: append([]byte(nil), row.EncryptedDekNonce...),
		Nonce:             append([]byte(nil), row.Nonce...),
		Ciphertext:        append([]byte(nil), row.Ciphertext...),
		CreatedAt:         row.CreatedAt,
	}
}

func secretVersionFromListByKeyID(row dbsqlc.ListSecretVersionsByKeyIDRow) SecretVersionRecord {
	return SecretVersionRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		SecretID:          row.SecretID,
		VersionNumber:     row.VersionNumber,
		PayloadKeys:       append([]string(nil), row.PayloadKeys...),
		EncryptionScheme:  row.EncryptionScheme,
		KeyID:             row.KeyID,
		DEKWrappedBy:      row.DekWrappedBy,
		EncryptedDEK:      append([]byte(nil), row.EncryptedDek...),
		EncryptedDEKNonce: append([]byte(nil), row.EncryptedDekNonce...),
		Nonce:             append([]byte(nil), row.Nonce...),
		Ciphertext:        append([]byte(nil), row.Ciphertext...),
		CreatedAt:         row.CreatedAt,
	}
}

func secretVersionFromGetSQLC(row dbsqlc.GetSecretVersionRow) SecretVersionRecord {
	return SecretVersionRecord{
		ID:                row.ID,
		OrgID:             row.OrgID,
		SecretID:          row.SecretID,
		VersionNumber:     row.VersionNumber,
		PayloadKeys:       append([]string(nil), row.PayloadKeys...),
		EncryptionScheme:  row.EncryptionScheme,
		KeyID:             row.KeyID,
		DEKWrappedBy:      row.DekWrappedBy,
		EncryptedDEK:      append([]byte(nil), row.EncryptedDek...),
		EncryptedDEKNonce: append([]byte(nil), row.EncryptedDekNonce...),
		Nonce:             append([]byte(nil), row.Nonce...),
		Ciphertext:        append([]byte(nil), row.Ciphertext...),
		CreatedAt:         row.CreatedAt,
	}
}

func secretGrantFromInsert(row dbsqlc.SecretGrant) SecretGrantRecord {
	return secretGrantFromSQLC(row)
}

func secretGrantFromGet(row dbsqlc.SecretGrant) SecretGrantRecord {
	return secretGrantFromSQLC(row)
}

func secretGrantFromDelete(row dbsqlc.SecretGrant) SecretGrantRecord {
	return secretGrantFromSQLC(row)
}

func secretGrantFromSQLC(row dbsqlc.SecretGrant) SecretGrantRecord {
	return SecretGrantRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		SecretID:        row.SecretID,
		TargetProjectID: row.TargetProjectID,
		CreatedAt:       row.CreatedAt,
	}
}
