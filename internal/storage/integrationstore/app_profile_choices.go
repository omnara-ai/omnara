package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// EnsureAppProfileChoice stores app-owned pre-launch state under the source
// receipt's lease. Scope matching is app policy; storage rechecks the live setup
// and offered profile identities without reading configs or allocating agents.
func (s *Store) EnsureAppProfileChoice(
	ctx context.Context, lease IntegrationInboxLease, input EnsureAppProfileChoiceInput,
) (AppProfileChoiceRecord, bool, error) {
	var result AppProfileChoiceRecord
	if err := validateAppProfileChoice(input); err != nil {
		return result, false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	work, err := s.LockIntegrationInboxLeaseTx(ctx, tx, lease)
	if err != nil {
		return result, false, err
	}
	if !bytes.Equal(work.record.Payload, input.Payload) || len(work.record.Events) != 0 {
		return result, false, inboxInvalid("choice source must be the verified provider receipt")
	}
	if err := LockConversationTx(ctx, tx, lease.ProjectID, work.record.AppID, input.Address); err != nil {
		return result, false, err
	}
	row, created, err := ensureAppProfileChoiceTx(ctx, work.q, work.record, input)
	if err != nil {
		return result, false, err
	}
	// A conversation or setup edit may have held us beyond the lease deadline.
	if err := work.checkLease(ctx); err != nil {
		return result, false, err
	}
	result, err = appProfileChoiceRecord(row)
	if err != nil {
		return result, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppProfileChoiceRecord{}, false, err
	}
	return result, created, nil
}

func ensureAppProfileChoiceTx(
	ctx context.Context, q *dbsqlc.Queries, receipt IntegrationInboxRecord, input EnsureAppProfileChoiceInput,
) (dbsqlc.AppProfileChoice, bool, error) {
	if input.AppID != receipt.AppID {
		return dbsqlc.AppProfileChoice{}, false, storeerr.ErrUnauthorized
	}
	row, err := q.GetAppProfileChoiceBySource(ctx, dbsqlc.GetAppProfileChoiceBySourceParams{
		ProjectID: receipt.ProjectID, AppID: receipt.AppID, SourceKey: input.SourceKey,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return row, false, err
	}
	if err == nil {
		if row.AddressKind != input.Address.Kind || row.AddressRef != input.Address.Ref {
			return row, false, storeerr.ErrConflict
		}
		if row.SelectedKey == nil && row.MessageID == nil {
			if err := checkProfileChoiceUnsettled(ctx, q, receipt, input); err != nil {
				return row, false, err
			}
		}
		// Selected and expired source identities remain replay barriers. A text
		// sibling cannot remove files and a replay cannot replace the offered menu.
		if row.SelectedKey != nil || !input.HasAttachments ||
			(jsoncanonical.Equal(row.Event, input.Event) && bytes.Equal(row.Payload, input.Payload)) {
			return row, false, nil
		}
		if _, err := appProfileChoiceApp(ctx, q, receipt.ProjectID, receipt.AppID); err != nil {
			return row, false, err
		}
		updated, err := q.UpdateAppProfileChoiceSource(ctx, dbsqlc.UpdateAppProfileChoiceSourceParams{
			ProjectID: receipt.ProjectID, AppID: receipt.AppID, ID: row.ID,
			SourceKey: input.SourceKey, Event: input.Event, Payload: input.Payload,
		})
		if errors.Is(err, pgx.ErrNoRows) { // Expiry never extends or revives the source.
			return row, false, nil
		}
		return updated, false, err
	}
	app, err := appProfileChoiceApp(ctx, q, receipt.ProjectID, receipt.AppID)
	if err != nil {
		return row, false, err
	}
	if err := checkProfileChoiceUnsettled(ctx, q, receipt, input); err != nil {
		return row, false, err
	}
	row, err = q.FindPendingAppProfileChoice(ctx, dbsqlc.FindPendingAppProfileChoiceParams{
		ProjectID: receipt.ProjectID, AppID: receipt.AppID,
		AddressKind: input.Address.Kind, AddressRef: input.Address.Ref,
	})
	if err == nil {
		return row, false, nil // An unrelated mention cannot replace the original request.
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, false, err
	}
	if err := validateAppProfileChoiceOptions(ctx, q, app, input.Options); err != nil {
		return row, false, err
	}
	options, err := json.Marshal(input.Options)
	if err != nil {
		return row, false, err
	}
	row, err = q.InsertAppProfileChoice(ctx, dbsqlc.InsertAppProfileChoiceParams{
		ProjectID: receipt.ProjectID, AppID: receipt.AppID,
		OwnerReceiptID: receipt.ID,
		AddressKind:    input.Address.Kind, AddressRef: input.Address.Ref, SourceKey: input.SourceKey,
		Event: input.Event, Payload: input.Payload, Options: options,
	})
	return row, err == nil, err
}

func checkProfileChoiceUnsettled(
	ctx context.Context, q *dbsqlc.Queries, receipt IntegrationInboxRecord, input EnsureAppProfileChoiceInput,
) error {
	// Routing was only a snapshot. A concurrent admission may have settled this
	// app before we acquired the conversation gate, including a retired target.
	targets, err := q.ListConversationSelections(ctx, dbsqlc.ListConversationSelectionsParams{
		ProjectID: receipt.ProjectID, AppID: receipt.AppID,
		Kind: input.Address.Kind, Ref: input.Address.Ref,
	})
	if err != nil {
		return err
	}
	if len(targets) != 0 {
		return ErrAppSelectionSettled
	}
	return nil
}

func (s *Store) GetAppProfileChoice(
	ctx context.Context, projectID, appID, id uuid.UUID,
) (AppProfileChoiceRecord, error) {
	if projectID == uuid.Nil || appID == uuid.Nil || id == uuid.Nil {
		return AppProfileChoiceRecord{}, inboxInvalid("project, app and choice are required")
	}
	row, err := getAppProfileChoice(ctx, s.q, projectID, appID, id)
	if err != nil {
		return AppProfileChoiceRecord{}, err
	}
	return appProfileChoiceRecord(row)
}

// GetAppProfileChoiceBySource retrieves retained app bookkeeping for exact late
// siblings. This read grants no launch authority; ordinary admission rechecks it.
func (s *Store) GetAppProfileChoiceBySource(
	ctx context.Context, projectID, appID uuid.UUID, sourceKey string,
) (AppProfileChoiceRecord, bool, error) {
	if projectID == uuid.Nil || appID == uuid.Nil ||
		!choiceText(sourceKey, IntegrationInboxMaxReceiptKeyBytes) {
		return AppProfileChoiceRecord{}, false, inboxInvalid("project, app and source key are required")
	}
	row, err := s.q.GetAppProfileChoiceBySource(ctx, dbsqlc.GetAppProfileChoiceBySourceParams{
		ProjectID: projectID, AppID: appID, SourceKey: sourceKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AppProfileChoiceRecord{}, false, nil
	}
	if err != nil {
		return AppProfileChoiceRecord{}, false, err
	}
	record, err := appProfileChoiceRecord(row)
	return record, err == nil, err
}

// RecordAppProfileChoiceMessage binds the first confirmed provider message. A
// duplicate publication cannot replace the identity callbacks must authenticate.
func (s *Store) RecordAppProfileChoiceMessage(
	ctx context.Context, projectID, appID, id uuid.UUID, channel, message string,
) error {
	if !choiceText(channel, 2048) || !choiceText(message, 2048) {
		return inboxInvalid("choice message requires a bounded channel and message identity")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := s.lockAppProfileChoice(ctx, tx, projectID, appID, id)
	if err != nil {
		return err
	}
	if row.MessageID != nil {
		if *row.MessageID != message || *row.MessageChannelID != channel {
			return storeerr.ErrConflict
		}
		return nil
	}
	rows, err := dbsqlc.New(tx).RecordAppProfileChoiceMessage(ctx, dbsqlc.RecordAppProfileChoiceMessageParams{
		ProjectID: projectID, AppID: appID, ID: id, MessageChannelID: &channel, MessageID: &message,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return storeerr.ErrNotFound
	}
	return tx.Commit(ctx)
}

// ChooseAppProfile commits the first valid selection and its decided inbox
// receipt atomically. Repeated/competing clicks return the original winner.
func (s *Store) ChooseAppProfile(ctx context.Context, input ChooseAppProfileInput) (AppProfileChoiceRecord, error) {
	var result AppProfileChoiceRecord
	if !choiceText(input.Key, 64) || !choiceText(input.ActorID, 2048) ||
		!choiceText(input.MessageChannelID, 2048) || !choiceText(input.MessageID, 2048) ||
		input.SourceChoiceUpdatedAt.IsZero() || input.SourceSetupRevision <= 0 {
		return result, inboxInvalid("choice requires offered key, actor, message and source revisions")
	}
	if err := validateChoiceJSON(input.Events, '[', IntegrationInboxMaxEventsBytes); err != nil {
		return result, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	row, err := s.lockAppProfileChoice(ctx, tx, input.ProjectID, input.AppID, input.ID)
	if err != nil {
		return result, err
	}
	app, err := getProjectApp(ctx, q, input.ProjectID, input.AppID)
	if err != nil {
		return result, err
	}
	if app.SetupRevision != input.SourceSetupRevision || row.MessageID == nil ||
		*row.MessageID != input.MessageID || *row.MessageChannelID != input.MessageChannelID {
		return result, storeerr.ErrUnauthorized
	}
	result, err = appProfileChoiceRecord(row)
	if err != nil || row.SelectedKey != nil {
		return result, err
	}
	if !row.UpdatedAt.Equal(input.SourceChoiceUpdatedAt) {
		return result, storeerr.ErrConflict
	}
	var offered []AppProfileChoiceOption
	for _, option := range result.Options {
		if option.Key == input.Key {
			offered = append(offered, option)
		}
	}
	if len(offered) != 1 {
		return result, storeerr.ErrStateTransitionConflict
	}
	// Unknown keys cannot retire a valid menu. Only an authenticated click on
	// an actually offered option can discover that its setup/profile is stale.
	app, err = appProfileChoiceApp(ctx, q, input.ProjectID, input.AppID)
	if err == nil {
		err = validateAppProfileChoiceOptions(ctx, q, app, offered)
	}
	if errors.Is(err, storeerr.ErrStateTransitionConflict) {
		expired, expireErr := commitAppProfileChoiceExpiry(ctx, tx, row)
		if expireErr != nil {
			return AppProfileChoiceRecord{}, expireErr
		}
		return expired, err
	}
	if err != nil {
		return result, err
	}
	row, err = q.SelectAppProfileChoice(ctx, dbsqlc.SelectAppProfileChoiceParams{
		ProjectID: input.ProjectID, AppID: input.AppID, ID: input.ID,
		SelectedKey: &input.Key, SelectedBy: &input.ActorID, ExpectedUpdatedAt: input.SourceChoiceUpdatedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, storeerr.ErrStateTransitionConflict // Expired while waiting for a gate.
	}
	if err != nil {
		return result, err
	}
	_, err = q.InsertAppProfileChoiceInboxReceipt(ctx, dbsqlc.InsertAppProfileChoiceInboxReceiptParams{
		ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: "choice:" + input.ID.String(),
		Payload: row.Payload, Events: &input.Events,
	})
	// A pre-existing receipt with an unselected choice cannot be a valid replay:
	// both records are committed together. Never adopt unrelated provider bytes.
	if errors.Is(err, pgx.ErrNoRows) {
		return result, storeerr.ErrConflict
	}
	if err != nil {
		return result, err
	}
	result, err = appProfileChoiceRecord(row)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppProfileChoiceRecord{}, err
	}
	return result, nil
}

// ExpireAppProfileChoice releases an unusable unselected menu, preserving its
// source replay barrier. A selected choice and its accepted work are unchanged.
func (s *Store) ExpireAppProfileChoice(ctx context.Context, projectID, appID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := s.lockAppProfileChoice(ctx, tx, projectID, appID, id)
	if err != nil {
		return err
	}
	_, err = commitAppProfileChoiceExpiry(ctx, tx, row)
	return err
}

func commitAppProfileChoiceExpiry(
	ctx context.Context, tx pgx.Tx, row dbsqlc.AppProfileChoice,
) (AppProfileChoiceRecord, error) {
	q := dbsqlc.New(tx)
	if err := q.ExpireAppProfileChoice(ctx, dbsqlc.ExpireAppProfileChoiceParams{
		ProjectID: row.ProjectID, AppID: row.AppID, ID: row.ID,
	}); err != nil {
		return AppProfileChoiceRecord{}, err
	}
	row, err := getAppProfileChoice(ctx, q, row.ProjectID, row.AppID, row.ID)
	if err != nil {
		return AppProfileChoiceRecord{}, err
	}
	record, err := appProfileChoiceRecord(row)
	if err != nil {
		return record, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AppProfileChoiceRecord{}, err
	}
	return record, nil
}

func (s *Store) lockAppProfileChoice(
	ctx context.Context, tx pgx.Tx, projectID, appID, id uuid.UUID,
) (dbsqlc.AppProfileChoice, error) {
	if projectID == uuid.Nil || appID == uuid.Nil || id == uuid.Nil {
		return dbsqlc.AppProfileChoice{}, inboxInvalid("project, app and choice are required")
	}
	if err := s.enterInboxApp(ctx, tx, projectID, appID); err != nil {
		return dbsqlc.AppProfileChoice{}, err
	}
	q := dbsqlc.New(tx)
	row, err := getAppProfileChoice(ctx, q, projectID, appID, id)
	if err != nil {
		return row, err
	}
	if err := LockConversationTx(ctx, tx, projectID, appID,
		ConversationAddress{Kind: row.AddressKind, Ref: row.AddressRef}); err != nil {
		return row, err
	}
	return getAppProfileChoice(ctx, q, projectID, appID, id)
}

func getAppProfileChoice(
	ctx context.Context, q *dbsqlc.Queries, projectID, appID, id uuid.UUID,
) (dbsqlc.AppProfileChoice, error) {
	row, err := q.GetAppProfileChoice(ctx, dbsqlc.GetAppProfileChoiceParams{
		ProjectID: projectID, AppID: appID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = storeerr.ErrNotFound
	}
	return row, err
}

func appProfileChoiceApp(
	ctx context.Context, q *dbsqlc.Queries, projectID, appID uuid.UUID,
) (ProjectAppRecord, error) {
	row, err := q.GetAppProfileChoiceAppForShare(ctx, dbsqlc.GetAppProfileChoiceAppForShareParams{
		ProjectID: projectID, ID: appID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAppRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return ProjectAppRecord{}, err
	}
	if ProjectAppState(row.State) != ProjectAppStateActive {
		return ProjectAppRecord{}, storeerr.ErrStateTransitionConflict
	}
	app, err := projectAppRecord(row)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	if app.Settings.Launcher == nil {
		return ProjectAppRecord{}, storeerr.ErrStateTransitionConflict
	}
	return app, nil
}

func validateAppProfileChoiceOptions(
	ctx context.Context, q *dbsqlc.Queries, app ProjectAppRecord, options []AppProfileChoiceOption,
) error {
	// appProfileChoiceApp already checked the live launcher under its row lock.
	ids := make([]uuid.UUID, 0, len(options))
	for _, option := range options {
		found := false
		for _, slot := range app.Settings.Launcher.Slots {
			if slot.Key == option.Key && slot.AgentProfileID != nil && *slot.AgentProfileID == option.ProfileID {
				found = true
				break
			}
		}
		if !found {
			return storeerr.ErrStateTransitionConflict
		}
		ids = append(ids, option.ProfileID)
	}
	// Profile deletion checks app references under its own profile lock. Do not
	// invert app-save's profile-before-app ordering with a profile row lock here.
	profiles, err := q.GetAgentProfileDisplayNames(ctx, dbsqlc.GetAgentProfileDisplayNamesParams{
		ProjectID: app.ProjectID, ProfileIds: ids,
	})
	if err != nil {
		return err
	}
	live := make(map[uuid.UUID]bool, len(profiles))
	for _, profile := range profiles {
		live[profile.ID] = true
	}
	for _, id := range ids {
		if !live[id] {
			return storeerr.ErrStateTransitionConflict
		}
	}
	return nil
}

func appProfileChoiceRecord(row dbsqlc.AppProfileChoice) (AppProfileChoiceRecord, error) {
	r := AppProfileChoiceRecord{
		ID: row.ID, ProjectID: row.ProjectID, AppID: row.AppID,
		OwnerReceiptID: row.OwnerReceiptID,
		Address:        ConversationAddress{Kind: row.AddressKind, Ref: row.AddressRef}, SourceKey: row.SourceKey,
		Event: row.Event, Payload: row.Payload, SelectedKey: inboxErrorText(row.SelectedKey),
		SelectedBy: inboxErrorText(row.SelectedBy), MessageChannelID: inboxErrorText(row.MessageChannelID),
		MessageID: inboxErrorText(row.MessageID), ExpiresAt: row.ExpiresAt,
		UpdatedAt: row.UpdatedAt, CreatedAt: row.CreatedAt,
	}
	if err := json.Unmarshal(row.Options, &r.Options); err != nil {
		return AppProfileChoiceRecord{}, fmt.Errorf("decode app profile options: %w", err)
	}
	return r, nil
}

func validateAppProfileChoice(input EnsureAppProfileChoiceInput) error {
	if input.AppID == uuid.Nil || !choiceText(input.SourceKey, IntegrationInboxMaxReceiptKeyBytes) {
		return inboxInvalid("choice requires an app and bounded source key")
	}
	if err := input.Address.Validate(); err != nil {
		return err
	}
	if len(input.Payload) == 0 || len(input.Payload) > IntegrationInboxMaxPayloadBytes {
		return inboxInvalid("choice payload exceeds bounds")
	}
	if err := validateChoiceJSON(input.Event, '{', IntegrationInboxMaxEventsBytes); err != nil {
		return err
	}
	if len(input.Options) == 0 || len(input.Options) > MaxAppLaunchSlots {
		return inboxInvalid("choice requires one to sixteen offered profiles")
	}
	seen := make(map[string]bool, len(input.Options))
	for _, option := range input.Options {
		if !choiceText(option.Key, 64) || strings.TrimSpace(option.Key) != option.Key || seen[option.Key] ||
			option.ProfileID == uuid.Nil || !choiceText(option.Name, 512) {
			return inboxInvalid("choice options require distinct keys, profiles and bounded names")
		}
		seen[option.Key] = true
	}
	return nil
}

func choiceText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && dbsafe.Text(value) == nil
}

func validateChoiceJSON(value json.RawMessage, kind byte, limit int) error {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || len(value) > limit || value[0] != kind || !json.Valid(value) {
		return inboxInvalid("choice JSON has invalid shape or exceeds bounds")
	}
	return nil
}

// CleanupAppProfileChoices retains live replay facts and recoverable selected work.
// Soft-deleted app, project or organization scopes bypass that retention.
func (s *Store) CleanupAppProfileChoices(ctx context.Context, retention time.Duration, limit int) (int64, error) {
	if retention < AppProfileChoiceMinRetention {
		return 0, inboxInvalid("profile choices must be retained for at least seven days beyond expiry")
	}
	if err := validateInboxBatch(limit); err != nil {
		return 0, err
	}
	return s.q.CleanupAppProfileChoices(ctx, dbsqlc.CleanupAppProfileChoicesParams{
		RetentionMilliseconds: retention.Milliseconds(), RowLimit: int32(limit),
	})
}

// GetAppProfileChoiceAppID identifies the setup that must verify a callback.
// It grants no access to the choice; selection rechecks its app and receipt.
func (s *Store) GetAppProfileChoiceAppID(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	appID, err := s.q.GetAppProfileChoiceAppID(ctx, dbsqlc.GetAppProfileChoiceAppIDParams{ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return appID, err
}
