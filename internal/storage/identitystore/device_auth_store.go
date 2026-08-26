package identitystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	DeviceAuthFlowTTL      = 15 * time.Minute
	DeviceAuthPollInterval = 5 * time.Second
	deviceAuthCodeAttempts = 5
	deviceAuthClientIDMax  = 256
)

func (s *Store) StartDeviceAuthFlow(
	ctx context.Context,
	input StartDeviceAuthFlowInput,
) (DeviceAuthFlowStartRecord, error) {
	clientID := input.ClientID
	if clientID == "" || len(clientID) > deviceAuthClientIDMax || strings.IndexFunc(clientID, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	}) >= 0 {
		return DeviceAuthFlowStartRecord{}, storeerr.ErrInvalidDeviceAuthFlow
	}
	clientName := input.ClientName
	if clientName == "" {
		clientName = "Device"
	}
	tokenName := input.TokenName
	if tokenName == "" {
		tokenName = "Device login"
	}
	var err error
	clientName, err = resourcename.CanonicalizeRequired("client_name", clientName)
	if err != nil {
		return DeviceAuthFlowStartRecord{}, storeerr.Tag(storeerr.ErrInvalidDeviceAuthFlow, err)
	}
	tokenName, err = resourcename.CanonicalizeRequired("token_name", tokenName)
	if err != nil {
		return DeviceAuthFlowStartRecord{}, storeerr.Tag(storeerr.ErrInvalidDeviceAuthFlow, err)
	}
	var deviceCode string
	var userCode string
	for range deviceAuthCodeAttempts {
		var err error
		deviceCode, err = randomTokenPart(32)
		if err != nil {
			return DeviceAuthFlowStartRecord{}, fmt.Errorf("generate device code: %w", err)
		}
		userCode, err = randomUserCode()
		if err != nil {
			return DeviceAuthFlowStartRecord{}, err
		}
		flow, err := s.q.CreateAuthDeviceFlow(ctx, dbsqlc.CreateAuthDeviceFlowParams{
			DeviceCodeHash: HashBearerToken(deviceCode),
			UserCodeHash:   HashBearerToken(NormalizeDeviceUserCode(userCode)),
			ClientID:       clientID,
			ClientName:     clientName,
			TokenName:      tokenName,
			TtlSeconds:     int64(DeviceAuthFlowTTL / time.Second),
		})
		if err != nil {
			if storeutil.IsUniqueViolation(err) {
				continue
			}
			return DeviceAuthFlowStartRecord{}, fmt.Errorf("create device auth flow: %w", err)
		}
		return DeviceAuthFlowStartRecord{
			DeviceCode: deviceCode,
			UserCode:   userCode,
			ExpiresIn:  time.Duration(flow.ExpiresInSeconds) * time.Second,
			Interval:   DeviceAuthPollInterval,
		}, nil
	}
	return DeviceAuthFlowStartRecord{}, storeerr.ErrIdempotencyConflict
}

func (s *Store) PendingDeviceAuthFlow(
	ctx context.Context,
	input DeviceAuthFlowPendingInput,
) (DeviceAuthFlowPendingRecord, error) {
	if input.UserCode == "" {
		return DeviceAuthFlowPendingRecord{}, storeerr.ErrUnauthorized
	}
	flow, err := s.q.GetAuthDeviceFlowByUserCode(
		ctx,
		dbsqlc.GetAuthDeviceFlowByUserCodeParams{UserCodeHash: HashBearerToken(NormalizeDeviceUserCode(input.UserCode))},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceAuthFlowPendingRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return DeviceAuthFlowPendingRecord{}, fmt.Errorf("load device auth flow: %w", err)
	}
	return DeviceAuthFlowPendingRecord{
		ClientName: flow.ClientName,
		TokenName:  flow.TokenName,
		CreatedAt:  flow.CreatedAt,
		ExpiresAt:  flow.ExpiresAt,
	}, nil
}

func (s *Store) ApproveDeviceAuthFlow(ctx context.Context, input ApproveDeviceAuthFlowInput) error {
	if input.UserCode == "" || isNilID(input.UserID) || isNilID(input.ApprovedBrowserSessionID) {
		return storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin device auth approval: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	flow, err := qtx.GetAuthDeviceFlowByUserCodeForUpdate(
		ctx,
		dbsqlc.GetAuthDeviceFlowByUserCodeForUpdateParams{
			UserCodeHash: HashBearerToken(NormalizeDeviceUserCode(input.UserCode)),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("load device auth flow: %w", err)
	}
	if _, err := qtx.GetActiveBrowserSessionForUserByID(
		ctx,
		dbsqlc.GetActiveBrowserSessionForUserByIDParams{
			ID:                 input.ApprovedBrowserSessionID,
			UserID:             input.UserID,
			IdleTimeoutSeconds: int64(browserSessionIdleDuration / time.Second),
		},
	); errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return storeerr.ErrUnauthorized
	} else if err != nil {
		return fmt.Errorf("revalidate device approval browser session: %w", err)
	}
	if _, err := qtx.ApproveAuthDeviceFlow(
		ctx,
		dbsqlc.ApproveAuthDeviceFlowParams{
			ID:                       flow.ID,
			ApprovedByUserID:         &input.UserID,
			ApprovedBrowserSessionID: &input.ApprovedBrowserSessionID,
		},
	); errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return storeerr.ErrUnauthorized
	} else if err != nil {
		return fmt.Errorf("approve device auth flow: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit device auth approval: %w", err)
	}
	return nil
}

func (s *Store) DenyDeviceAuthFlow(ctx context.Context, input DenyDeviceAuthFlowInput) error {
	if input.UserCode == "" {
		return storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin device auth denial: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	flow, err := qtx.GetAuthDeviceFlowByUserCodeForUpdate(
		ctx,
		dbsqlc.GetAuthDeviceFlowByUserCodeForUpdateParams{
			UserCodeHash: HashBearerToken(NormalizeDeviceUserCode(input.UserCode)),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrUnauthorized
	}
	if err != nil {
		return fmt.Errorf("load device auth flow: %w", err)
	}
	if _, err := qtx.DenyAuthDeviceFlow(
		ctx,
		dbsqlc.DenyAuthDeviceFlowParams{ID: flow.ID},
	); errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return storeerr.ErrUnauthorized
	} else if err != nil {
		return fmt.Errorf("deny device auth flow: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit device auth denial: %w", err)
	}
	return nil
}

func (s *Store) PollDeviceAuthFlow(
	ctx context.Context,
	input DeviceAuthFlowPollInput,
) (DeviceAuthFlowPollRecord, error) {
	if input.DeviceCode == "" || input.ClientID == "" {
		return DeviceAuthFlowPollRecord{}, storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DeviceAuthFlowPollRecord{}, fmt.Errorf("begin device auth poll: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	flow, err := qtx.GetAuthDeviceFlowByDeviceCodeForUpdate(
		ctx,
		dbsqlc.GetAuthDeviceFlowByDeviceCodeForUpdateParams{DeviceCodeHash: HashBearerToken(input.DeviceCode)},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusExpired, Interval: DeviceAuthPollInterval}, nil
	}
	if err != nil {
		return DeviceAuthFlowPollRecord{}, fmt.Errorf("load device auth flow: %w", err)
	}
	if flow.ClientID != input.ClientID {
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusInvalid, Interval: DeviceAuthPollInterval}, nil
	}
	pollState, err := qtx.GetAuthDeviceFlowPollState(
		ctx,
		dbsqlc.GetAuthDeviceFlowPollStateParams{
			ID:                  flow.ID,
			PollIntervalSeconds: int64(DeviceAuthPollInterval / time.Second),
		},
	)
	if err != nil {
		return DeviceAuthFlowPollRecord{}, fmt.Errorf("load device auth flow poll state: %w", err)
	}
	if pollState.Expired {
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusExpired, Interval: DeviceAuthPollInterval}, nil
	}
	if flow.DeniedAt != nil {
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusDenied, Interval: DeviceAuthPollInterval}, nil
	}
	if flow.ApprovedAt == nil {
		if pollState.PolledTooSoon {
			return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusSlowDown, Interval: DeviceAuthPollInterval}, nil
		}
		updated, err := qtx.MarkAuthDeviceFlowPolled(
			ctx,
			dbsqlc.MarkAuthDeviceFlowPolledParams{
				ID:                  flow.ID,
				PollIntervalSeconds: int64(DeviceAuthPollInterval / time.Second),
			},
		)
		if err != nil {
			return DeviceAuthFlowPollRecord{}, fmt.Errorf("mark device auth poll: %w", err)
		}
		if updated != 1 {
			return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusExpired, Interval: DeviceAuthPollInterval}, nil
		}
		if err := tx.Commit(ctx); err != nil {
			return DeviceAuthFlowPollRecord{}, fmt.Errorf("commit pending device auth poll: %w", err)
		}
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusPending, Interval: DeviceAuthPollInterval}, nil
	}
	preparedToken, err := preparePersonalAccessTokenInput(CreatePersonalAccessTokenInput{
		UserID:         *flow.ApprovedByUserID,
		ActorPrincipal: NewBrowserSessionPrincipal(*flow.ApprovedByUserID, *flow.ApprovedBrowserSessionID),
		Name:           flow.TokenName,
	})
	if err != nil {
		return DeviceAuthFlowPollRecord{}, err
	}
	if _, err := createPersonalAccessTokenTx(ctx, qtx, preparedToken); errors.Is(err, storeerr.ErrUnauthorized) {
		updated, denyErr := qtx.DenyInvalidatedApprovedAuthDeviceFlow(
			ctx,
			dbsqlc.DenyInvalidatedApprovedAuthDeviceFlowParams{ID: flow.ID},
		)
		if denyErr != nil {
			return DeviceAuthFlowPollRecord{}, fmt.Errorf("deny invalidated device auth flow: %w", denyErr)
		}
		if updated != 1 {
			return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusExpired, Interval: DeviceAuthPollInterval}, nil
		}
		if err := tx.Commit(ctx); err != nil {
			return DeviceAuthFlowPollRecord{}, fmt.Errorf("commit invalidated device auth poll: %w", err)
		}
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusDenied, Interval: DeviceAuthPollInterval}, nil
	} else if err != nil {
		return DeviceAuthFlowPollRecord{}, err
	}
	consumed, err := qtx.ConsumeAuthDeviceFlow(
		ctx,
		dbsqlc.ConsumeAuthDeviceFlowParams{ID: flow.ID},
	)
	if err != nil {
		return DeviceAuthFlowPollRecord{}, fmt.Errorf("consume device auth flow: %w", err)
	}
	if consumed != 1 {
		return DeviceAuthFlowPollRecord{Status: DeviceAuthFlowStatusExpired, Interval: DeviceAuthPollInterval}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return DeviceAuthFlowPollRecord{}, fmt.Errorf("commit approved device auth poll: %w", err)
	}
	return DeviceAuthFlowPollRecord{
		Status:   DeviceAuthFlowStatusApproved,
		Token:    preparedToken.token,
		Interval: DeviceAuthPollInterval,
	}, nil
}

func NormalizeDeviceUserCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return code
}

func randomUserCode() (string, error) {
	raw, err := randomTokenPart(5)
	if err != nil {
		return "", fmt.Errorf("generate user code: %w", err)
	}
	code := strings.ToUpper(raw)
	return code[:5] + "-" + code[5:], nil
}
