package identitystore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func orgRecordFromSQLC(row dbsqlc.CreateOrgRow) OrgRecord {
	return OrgRecord{
		ID:             row.ID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func orgRecordFromGetByIdempotencySQLC(row dbsqlc.GetOrgByIdempotencyKeyRow) OrgRecord {
	return OrgRecord{
		ID:             row.ID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func orgRecordFromGetSQLC(row dbsqlc.GetOrgRow) OrgRecord {
	return OrgRecord{
		ID:             row.ID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func projectRecordFromSQLC(row dbsqlc.CreateProjectRow) ProjectRecord {
	return ProjectRecord{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func projectRecordFromGetByIdempotencySQLC(row dbsqlc.GetProjectByIdempotencyKeyRow) ProjectRecord {
	return ProjectRecord{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func projectRecordFromGetSQLC(row dbsqlc.GetProjectRow) ProjectRecord {
	return ProjectRecord{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func projectRecordFromVisibleSQLC(row dbsqlc.ListVisibleProjectRolesForPrincipalRow) ProjectRecord {
	return ProjectRecord{
		ID:             row.ID,
		OrgID:          row.OrgID,
		Name:           row.Name,
		IdempotencyKey: row.IdempotencyKey,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func userRecordFromSQLC(row dbsqlc.User) UserRecord {
	return UserRecord{
		ID:          row.ID,
		DisplayName: row.DisplayName,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func userEmailRecordFromSQLC(row dbsqlc.UserEmail) UserEmailRecord {
	return UserEmailRecord{
		ID:              row.ID,
		UserID:          row.UserID,
		Email:           row.Email,
		NormalizedEmail: row.NormalizedEmail,
		VerifiedAt:      row.VerifiedAt,
		IsPrimary:       row.IsPrimary,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func userAuthIdentityRecordFromSQLC(row dbsqlc.UserAuthIdentity) UserAuthIdentityRecord {
	return UserAuthIdentityRecord{
		ID:              row.ID,
		UserID:          row.UserID,
		AuthConnectorID: row.AuthConnectorID,
		Issuer:          row.Issuer,
		Subject:         row.Subject,
		EmailAtLink:     row.EmailAtLink,
		EmailVerified:   row.EmailVerified,
		CreatedAt:       row.CreatedAt,
	}
}

func authConnectorRecordFromSQLC(row dbsqlc.AuthConnector, clientSecret string) AuthConnectorRecord {
	return AuthConnectorRecord{
		ID:               row.ID,
		Slug:             row.Slug,
		Kind:             row.Kind,
		DisplayName:      row.DisplayName,
		Issuer:           row.Issuer,
		AuthorizationURL: row.AuthorizationUrl,
		TokenURL:         row.TokenUrl,
		UserinfoURL:      row.UserinfoUrl,
		ClientID:         row.ClientID,
		ClientSecret:     clientSecret,
		Scopes:           append([]string(nil), row.Scopes...),
		EmailTrustPolicy: row.EmailTrustPolicy,
		Enabled:          row.Enabled,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

func orgInvitationRecordFromSQLC(row dbsqlc.OrgInvitation) OrgInvitationRecord {
	return OrgInvitationRecord{
		ID:              row.ID,
		OrgID:           row.OrgID,
		Email:           row.Email,
		NormalizedEmail: row.NormalizedEmail,
		OrgRole:         row.OrgRole,
		CreatedAt:       row.CreatedAt,
	}
}

func pendingOrgInvitationRecordFromSQLC(
	row dbsqlc.ListPendingOrgInvitationsForEmailsRow,
) OrgInvitationWithOrgNameRecord {
	return OrgInvitationWithOrgNameRecord{
		OrgInvitationRecord: OrgInvitationRecord{
			ID:              row.ID,
			OrgID:           row.OrgID,
			Email:           row.Email,
			NormalizedEmail: row.NormalizedEmail,
			OrgRole:         row.OrgRole,
			CreatedAt:       row.CreatedAt,
		},
		OrgName: row.OrgName,
	}
}

func consumedOrgInvitationRecordFromSQLC(
	row dbsqlc.ConsumeOrgInvitationForEmailsRow,
) OrgInvitationWithOrgNameRecord {
	return OrgInvitationWithOrgNameRecord{
		OrgInvitationRecord: OrgInvitationRecord{
			ID:              row.ID,
			OrgID:           row.OrgID,
			Email:           row.Email,
			NormalizedEmail: row.NormalizedEmail,
			OrgRole:         row.OrgRole,
			CreatedAt:       row.CreatedAt,
		},
		OrgName: row.OrgName,
	}
}

func userOrgMembershipRecord(id, orgID, userID ID, role string, createdAt time.Time) OrgMembershipRecord {
	return OrgMembershipRecord{
		ID:        id,
		OrgID:     orgID,
		UserID:    userID,
		Role:      role,
		CreatedAt: createdAt,
	}
}

func userOrgMembershipRecordFromSQLC(row dbsqlc.ListOrgMembershipsForUserRow) UserOrgMembershipRecord {
	return UserOrgMembershipRecord{OrgID: row.ID, OrgName: row.Name, Role: row.Role, CreatedAt: row.CreatedAt}
}

func orgMemberRecordFromSQLC(row dbsqlc.ListOrgMembersRow) OrgMemberRecord {
	return OrgMemberRecord{UserID: row.UserID, DisplayName: row.DisplayName, Role: row.Role, CreatedAt: row.CreatedAt}
}

func projectMembershipRecordFromSQLC(row dbsqlc.ProjectMembership) ProjectMembershipRecord {
	return ProjectMembershipRecord{
		OrgID:           row.OrgID,
		ProjectID:       row.ProjectID,
		OrgMembershipID: row.OrgMembershipID,
		Role:            row.Role,
		CreatedAt:       row.CreatedAt,
	}
}

func personalAccessTokenRecordFromSQLC(row dbsqlc.PersonalAccessToken) PersonalAccessTokenRecord {
	return PersonalAccessTokenRecord{
		ID:         row.ID,
		UserID:     row.UserID,
		Name:       row.Name,
		TokenID:    row.TokenID,
		TokenHash:  row.TokenHash,
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
		RevokedAt:  row.RevokedAt,
	}
}

func browserSessionRecordFromSQLC(row dbsqlc.BrowserSession) BrowserSessionRecord {
	return BrowserSessionRecord{
		ID:            row.ID,
		UserID:        row.UserID,
		TokenHash:     row.TokenHash,
		CSRFTokenHash: row.CsrfTokenHash,
		CreatedAt:     row.CreatedAt,
		LastSeenAt:    row.LastSeenAt,
		ExpiresAt:     row.ExpiresAt,
		RevokedAt:     row.RevokedAt,
	}
}
