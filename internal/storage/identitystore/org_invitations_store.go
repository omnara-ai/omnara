package identitystore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateOrgInvitation(
	ctx context.Context,
	input CreateOrgInvitationInput,
) (OrgInvitationRecord, error) {
	if isNilID(input.OrgID) {
		return OrgInvitationRecord{}, errors.New("org id is required")
	}
	if input.Email == "" {
		return OrgInvitationRecord{}, storeerr.InvalidRequest(errors.New("email is required"))
	}
	if input.Role == "" {
		return OrgInvitationRecord{}, storeerr.InvalidRequest(errors.New("role is required"))
	}
	if input.Role != authz.OrgRoleAdmin && input.Role != authz.OrgRoleMember {
		return OrgInvitationRecord{}, storeerr.InvalidRequest(errors.New("role must be admin or member"))
	}
	normalizedEmail := NormalizeEmail(input.Email)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("begin create org invitation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	emailUser, err := qtx.GetVerifiedUserEmailByNormalizedEmail(
		ctx,
		dbsqlc.GetVerifiedUserEmailByNormalizedEmailParams{NormalizedEmail: normalizedEmail},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OrgInvitationRecord{}, fmt.Errorf("check invite target email: %w", err)
	}
	if err == nil {
		if _, err := qtx.GetOrgAuthorizationRole(
			ctx,
			dbsqlc.GetOrgAuthorizationRoleParams{OrgID: input.OrgID, UserID: emailUser.UserID},
		); err == nil {
			return OrgInvitationRecord{}, storeerr.ErrIdempotencyConflict
		} else if !errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return OrgInvitationRecord{}, fmt.Errorf("check existing org member: %w", err)
		}
	}
	existing, err := qtx.GetPendingOrgInvitationByEmail(
		ctx,
		dbsqlc.GetPendingOrgInvitationByEmailParams{
			OrgID:           input.OrgID,
			NormalizedEmail: normalizedEmail,
		},
	)
	if err == nil {
		if existing.OrgRole != input.Role {
			return OrgInvitationRecord{}, storeerr.ErrIdempotencyConflict
		}
		return orgInvitationRecordFromSQLC(existing), nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OrgInvitationRecord{}, fmt.Errorf("load pending org invitation: %w", err)
	}
	row, err := qtx.CreateOrgInvitation(
		ctx,
		dbsqlc.CreateOrgInvitationParams{
			OrgID:           input.OrgID,
			Email:           input.Email,
			NormalizedEmail: normalizedEmail,
			OrgRole:         input.Role,
		},
	)
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return OrgInvitationRecord{}, storeerr.ErrIdempotencyConflict
		}
		return OrgInvitationRecord{}, fmt.Errorf("create org invitation: %w", err)
	}
	if err := resourceguard.Lock(ctx, qtx, resourceOrgInvitations, input.OrgID.String()); err != nil {
		return OrgInvitationRecord{}, err
	}
	invitationCount, err := qtx.CountPendingOrgInvitationsForOrg(
		ctx,
		dbsqlc.CountPendingOrgInvitationsForOrgParams{OrgID: input.OrgID},
	)
	if err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("count pending organization invitations: %w", err)
	}
	if invitationCount > MaxPendingOrgInvitationsPerOrg {
		return OrgInvitationRecord{}, resourceLimitExceeded(
			"pending organization invitations",
			MaxPendingOrgInvitationsPerOrg,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("commit create org invitation: %w", err)
	}
	return orgInvitationRecordFromSQLC(row), nil
}

type ListOrgInvitationsInput struct {
	OrgID ID
	Limit int
	After listing.KeysetCursor
}

type ListOrgInvitationsResult struct {
	Invitations []OrgInvitationRecord
	HasMore     bool
}

func (s *Store) ListOrgInvitations(
	ctx context.Context,
	input ListOrgInvitationsInput,
) (ListOrgInvitationsResult, error) {
	if isNilID(input.OrgID) {
		return ListOrgInvitationsResult{}, errors.New("org id is required")
	}
	if input.Limit <= 0 {
		return ListOrgInvitationsResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListOrgInvitationsParams{
		OrgID:    input.OrgID,
		RowLimit: int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListOrgInvitations(ctx, params)
	if err != nil {
		return ListOrgInvitationsResult{}, fmt.Errorf("list org invitations: %w", err)
	}
	records := make([]OrgInvitationRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orgInvitationRecordFromSQLC(row))
	}
	result := ListOrgInvitationsResult{}
	if len(records) > input.Limit {
		result.HasMore = true
		records = records[:input.Limit]
	}
	result.Invitations = records
	return result, nil
}

type ListPendingOrgInvitationsForUserInput struct {
	UserID ID
	Limit  int
	After  listing.KeysetCursor
}

type ListPendingOrgInvitationsForUserResult struct {
	Invitations []OrgInvitationRecord
	HasMore     bool
}

func (s *Store) ListPendingOrgInvitationsForUser(
	ctx context.Context,
	input ListPendingOrgInvitationsForUserInput,
) (ListPendingOrgInvitationsForUserResult, error) {
	if isNilID(input.UserID) {
		return ListPendingOrgInvitationsForUserResult{}, errors.New("user id is required")
	}
	if input.Limit <= 0 {
		return ListPendingOrgInvitationsForUserResult{}, errors.New("limit must be positive")
	}
	emailRows, err := s.q.ListVerifiedUserEmailsByUser(
		ctx,
		dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: input.UserID},
	)
	if err != nil {
		return ListPendingOrgInvitationsForUserResult{}, fmt.Errorf("list verified emails: %w", err)
	}
	emails := make([]string, 0, len(emailRows))
	for _, email := range emailRows {
		emails = append(emails, email.NormalizedEmail)
	}
	if len(emails) == 0 {
		return ListPendingOrgInvitationsForUserResult{Invitations: []OrgInvitationRecord{}}, nil
	}
	params := dbsqlc.ListPendingOrgInvitationsForEmailsParams{
		NormalizedEmails: emails,
		RowLimit:         int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListPendingOrgInvitationsForEmails(ctx, params)
	if err != nil {
		return ListPendingOrgInvitationsForUserResult{}, fmt.Errorf("list pending invitations: %w", err)
	}
	records := make([]OrgInvitationRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orgInvitationRecordFromSQLC(row))
	}
	result := ListPendingOrgInvitationsForUserResult{}
	if len(records) > input.Limit {
		result.HasMore = true
		records = records[:input.Limit]
	}
	result.Invitations = records
	return result, nil
}

func (s *Store) AcceptOrgInvitation(
	ctx context.Context,
	input AcceptOrgInvitationInput,
) (OrgInvitationRecord, error) {
	return s.answerOrgInvitation(ctx, input.ID, input.UserID, true)
}

func (s *Store) DeclineOrgInvitation(
	ctx context.Context,
	input DeclineOrgInvitationInput,
) (OrgInvitationRecord, error) {
	return s.answerOrgInvitation(ctx, input.ID, input.UserID, false)
}

func (s *Store) DeleteOrgInvitation(
	ctx context.Context,
	orgID, id ID,
) (OrgInvitationRecord, error) {
	if isNilID(orgID) {
		return OrgInvitationRecord{}, errors.New("org id is required")
	}
	if isNilID(id) {
		return OrgInvitationRecord{}, errors.New("invitation id is required")
	}
	row, err := s.q.DeleteOrgInvitation(
		ctx,
		dbsqlc.DeleteOrgInvitationParams{OrgID: orgID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgInvitationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("delete org invitation: %w", err)
	}
	return orgInvitationRecordFromSQLC(row), nil
}

func (s *Store) answerOrgInvitation(
	ctx context.Context,
	id, userID ID,
	accept bool,
) (OrgInvitationRecord, error) {
	if isNilID(id) {
		return OrgInvitationRecord{}, errors.New("invitation id is required")
	}
	if isNilID(userID) {
		return OrgInvitationRecord{}, errors.New("user id is required")
	}
	emailRows, err := s.q.ListVerifiedUserEmailsByUser(
		ctx,
		dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: userID},
	)
	if err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("list verified emails: %w", err)
	}
	if len(emailRows) == 0 {
		return OrgInvitationRecord{}, storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("begin answer org invitation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OrgInvitationRecord{}, storeerr.ErrNotFound
		}
		return OrgInvitationRecord{}, fmt.Errorf("lock invited user: %w", err)
	}
	var answered dbsqlc.OrgInvitation
	var matched bool
	var answerErr error
	for _, email := range emailRows {
		answered, answerErr = qtx.ConsumeOrgInvitationForEmail(
			ctx,
			dbsqlc.ConsumeOrgInvitationForEmailParams{
				ID:              id,
				NormalizedEmail: email.NormalizedEmail,
			},
		)
		if answerErr == nil {
			matched = true
			break
		}
		if !errors.Is(answerErr, pgx.ErrNoRows) {
			return OrgInvitationRecord{}, fmt.Errorf("answer org invitation: %w", answerErr)
		}
	}
	if !matched {
		return OrgInvitationRecord{}, storeerr.ErrNotFound
	}
	if accept {
		orgActive, err := qtx.OrgExistsActive(ctx, dbsqlc.OrgExistsActiveParams{ID: answered.OrgID})
		if err != nil {
			return OrgInvitationRecord{}, fmt.Errorf("check org for invitation accept: %w", err)
		}
		if !orgActive {
			// A pending invitation must never mint a membership in a deleted
			// organization; deletion also revokes invitations, but the accept
			// path guards against races.
			return OrgInvitationRecord{}, storeerr.ErrNotFound
		}
		_, membershipErr := qtx.LockUserOrgMembership(
			ctx,
			dbsqlc.LockUserOrgMembershipParams{OrgID: answered.OrgID, UserID: userID},
		)
		if membershipErr != nil && !errors.Is(membershipErr, pgx.ErrNoRows) {
			return OrgInvitationRecord{}, fmt.Errorf(
				"lock existing org membership: %w",
				membershipErr,
			)
		}
		if errors.Is(membershipErr, pgx.ErrNoRows) {
			membershipCount, err := qtx.CountOrgMembershipsForUser(
				ctx,
				dbsqlc.CountOrgMembershipsForUserParams{UserID: userID},
			)
			if err != nil {
				return OrgInvitationRecord{}, fmt.Errorf("count user org memberships: %w", err)
			}
			if membershipCount >= MaxOrgMembershipsPerUser {
				return OrgInvitationRecord{}, storeerr.ErrUnauthorized
			}
		}
		if _, err := qtx.AddUserOrgMembershipIfMissing(
			ctx,
			dbsqlc.AddUserOrgMembershipIfMissingParams{
				OrgID:  answered.OrgID,
				UserID: userID,
				Role:   answered.OrgRole,
			},
		); err != nil &&
			!errors.Is(err, pgx.ErrNoRows) {
			return OrgInvitationRecord{}, fmt.Errorf("create invited org membership: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return OrgInvitationRecord{}, fmt.Errorf("commit answer org invitation: %w", err)
	}
	return orgInvitationRecordFromSQLC(answered), nil
}
