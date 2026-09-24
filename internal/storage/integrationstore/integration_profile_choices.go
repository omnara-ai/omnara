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

func (s *Store) EnsureIntegrationProfileChoice(
	ctx context.Context, lease IntegrationInboxLease, input EnsureIntegrationProfileChoiceInput,
) (IntegrationProfileChoiceRecord, bool, error) {
	var result IntegrationProfileChoiceRecord
	if err := validateIntegrationProfileChoice(input); err != nil {
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
	if err := LockConversationTx(ctx, tx, lease.ProjectID, work.record.IntegrationID, input.Address); err != nil {
		return result, false, err
	}
	row, created, err := ensureIntegrationProfileChoiceTx(ctx, work.q, work.record, input)
	if err != nil {
		return result, false, err
	}
	if err := work.CheckLease(ctx); err != nil {
		return result, false, err
	}
	result = row
	if err := tx.Commit(ctx); err != nil {
		return IntegrationProfileChoiceRecord{}, false, err
	}
	return result, created, nil
}

func ensureIntegrationProfileChoiceTx(
	ctx context.Context, q *dbsqlc.Queries, receipt IntegrationInboxRecord, input EnsureIntegrationProfileChoiceInput,
) (IntegrationProfileChoiceRecord, bool, error) {
	if input.IntegrationID != receipt.IntegrationID {
		return IntegrationProfileChoiceRecord{}, false, storeerr.ErrUnauthorized
	}
	row, err := getIntegrationProfileChoiceBySource(ctx, q, receipt.ProjectID, receipt.IntegrationID, input.SourceKey)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return row, false, err
	}
	if err == nil {
		if row.Address != input.Address {
			return row, false, storeerr.ErrConflict
		}
		if row.SelectedKey == "" && row.MessageID == "" {
			if err := checkProfileChoiceUnsettled(ctx, q, receipt, input); err != nil {
				return row, false, err
			}
		}
		if row.SelectedKey != "" || !input.HasAttachments ||
			(jsoncanonical.Equal(row.Event, input.Event) && bytes.Equal(row.Payload, input.Payload)) {
			return row, false, nil
		}
		if _, err := integrationProfileChoiceIntegration(ctx, q, receipt.ProjectID, receipt.IntegrationID); err != nil {
			return row, false, err
		}
		updated := row
		updated.Event, updated.Payload = input.Event, input.Payload
		updated, err = replaceIntegrationProfileChoice(ctx, q, updated, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return row, false, nil
		}
		return updated, false, err
	}
	integration, err := integrationProfileChoiceIntegration(ctx, q, receipt.ProjectID, receipt.IntegrationID)
	if err != nil {
		return row, false, err
	}
	if err := checkProfileChoiceUnsettled(ctx, q, receipt, input); err != nil {
		return row, false, err
	}
	state, err := q.FindPendingIntegrationProfileChoice(ctx, dbsqlc.FindPendingIntegrationProfileChoiceParams{
		ProjectID: receipt.ProjectID, IntegrationID: receipt.IntegrationID,
		AddressKind: input.Address.Kind, AddressRef: input.Address.Ref,
	})
	if err == nil {
		row, err = integrationProfileChoiceRecord(state)
		return row, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, false, err
	}
	if err := validateIntegrationProfileChoiceOptions(ctx, q, integration, input.Options); err != nil {
		return row, false, err
	}
	row = IntegrationProfileChoiceRecord{
		ProjectID: receipt.ProjectID, IntegrationID: receipt.IntegrationID, OwnerReceiptID: receipt.ID,
		Address: input.Address, SourceKey: input.SourceKey,
		Event: input.Event, Payload: input.Payload, Options: input.Options,
	}
	data, err := encodeIntegrationProfileChoice(row)
	if err != nil {
		return row, false, err
	}
	state, err = q.InsertIntegrationState(ctx, dbsqlc.InsertIntegrationStateParams{
		ProjectID: receipt.ProjectID, IntegrationID: receipt.IntegrationID, Kind: integrationProfileChoiceKind,
		Key: input.SourceKey, ScopeKind: &input.Address.Kind, ScopeRef: &input.Address.Ref,
		Data: data, LifetimeMilliseconds: time.Hour.Milliseconds(),
	})
	if err != nil {
		return row, false, err
	}
	row, err = integrationProfileChoiceRecord(state)
	return row, true, err
}

func checkProfileChoiceUnsettled(
	ctx context.Context, q *dbsqlc.Queries, receipt IntegrationInboxRecord, input EnsureIntegrationProfileChoiceInput,
) error {
	targets, err := q.ListConversationSelections(ctx, dbsqlc.ListConversationSelectionsParams{
		ProjectID: receipt.ProjectID, IntegrationID: receipt.IntegrationID,
		Kind: input.Address.Kind, Ref: input.Address.Ref,
	})
	if err != nil {
		return err
	}
	if len(targets) != 0 {
		return ErrIntegrationSelectionSettled
	}
	return nil
}

func (s *Store) GetIntegrationProfileChoice(
	ctx context.Context, projectID, integrationID, id uuid.UUID,
) (IntegrationProfileChoiceRecord, error) {
	if projectID == uuid.Nil || integrationID == uuid.Nil || id == uuid.Nil {
		return IntegrationProfileChoiceRecord{}, inboxInvalid("project, integration and choice are required")
	}
	row, err := getIntegrationProfileChoice(ctx, s.q, projectID, integrationID, id)
	if err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	return row, nil
}

func (s *Store) GetIntegrationProfileChoiceBySource(
	ctx context.Context, projectID, integrationID uuid.UUID, sourceKey string,
) (IntegrationProfileChoiceRecord, bool, error) {
	if projectID == uuid.Nil || integrationID == uuid.Nil ||
		!choiceText(sourceKey, IntegrationInboxMaxReceiptKeyBytes) {
		return IntegrationProfileChoiceRecord{}, false, inboxInvalid("project, integration and source key are required")
	}
	row, err := getIntegrationProfileChoiceBySource(ctx, s.q, projectID, integrationID, sourceKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationProfileChoiceRecord{}, false, nil
	}
	if err != nil {
		return IntegrationProfileChoiceRecord{}, false, err
	}
	return row, true, nil
}

func (s *Store) GetIntegrationProfileChoiceInbox(
	ctx context.Context, projectID, integrationID, choiceID uuid.UUID,
) (IntegrationInboxRecord, bool, error) {
	if projectID == uuid.Nil || integrationID == uuid.Nil || choiceID == uuid.Nil {
		return IntegrationInboxRecord{}, false, inboxInvalid("project, integration and choice are required")
	}
	row, err := s.q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
		ProjectID: projectID, IntegrationID: integrationID, ReceiptKey: "choice:" + choiceID.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationInboxRecord{}, false, nil
	}
	if err != nil {
		return IntegrationInboxRecord{}, false, fmt.Errorf("get integration profile choice inbox: %w", err)
	}
	return inboxRecord(row), true, nil
}

func (s *Store) RecordIntegrationProfileChoiceMessage(
	ctx context.Context, projectID, integrationID, id uuid.UUID, channel, message string,
) error {
	if !choiceText(channel, 2048) || !choiceText(message, 2048) {
		return inboxInvalid("choice message requires a bounded channel and message identity")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := s.lockIntegrationProfileChoice(ctx, tx, projectID, integrationID, id)
	if err != nil {
		return err
	}
	if row.MessageID != "" {
		if row.MessageID != message || row.MessageChannelID != channel {
			return storeerr.ErrConflict
		}
		return nil
	}
	row.MessageChannelID, row.MessageID = channel, message
	if _, err := replaceIntegrationProfileChoice(ctx, dbsqlc.New(tx), row, false); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ChooseIntegrationProfile(
	ctx context.Context,
	input ChooseIntegrationProfileInput,
) (IntegrationProfileChoiceRecord, error) {
	var result IntegrationProfileChoiceRecord
	if !choiceText(input.Key, 64) || !choiceText(input.ActorID, 2048) ||
		!choiceText(input.MessageChannelID, 2048) || !choiceText(input.MessageID, 2048) ||
		input.SourceChoiceRevision <= 0 || input.SourceSetupRevision <= 0 {
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
	row, err := s.lockIntegrationProfileChoice(ctx, tx, input.ProjectID, input.IntegrationID, input.ID)
	if err != nil {
		return result, err
	}
	integration, err := getProjectIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return result, err
	}
	if integration.SetupRevision != input.SourceSetupRevision || row.MessageID == "" ||
		row.MessageID != input.MessageID || row.MessageChannelID != input.MessageChannelID {
		return result, storeerr.ErrUnauthorized
	}
	result = row
	if row.SelectedKey != "" {
		return result, nil
	}
	if row.Revision != input.SourceChoiceRevision {
		return result, storeerr.ErrConflict
	}
	var offered []IntegrationProfileChoiceOption
	for _, option := range result.Options {
		if option.Key == input.Key {
			offered = append(offered, option)
		}
	}
	if len(offered) != 1 {
		return result, storeerr.ErrStateTransitionConflict
	}
	integration, err = integrationProfileChoiceIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err == nil {
		err = validateIntegrationProfileChoiceOptions(ctx, q, integration, offered)
	}
	if errors.Is(err, storeerr.ErrStateTransitionConflict) {
		expired, expireErr := commitIntegrationProfileChoiceExpiry(ctx, tx, row)
		if expireErr != nil {
			return IntegrationProfileChoiceRecord{}, expireErr
		}
		return expired, err
	}
	if err != nil {
		return result, err
	}
	row.SelectedKey, row.SelectedBy = input.Key, input.ActorID
	row, err = replaceIntegrationProfileChoice(ctx, q, row, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return result, err
	}
	_, err = q.InsertIntegrationProfileChoiceInboxReceipt(ctx, dbsqlc.InsertIntegrationProfileChoiceInboxReceiptParams{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, ReceiptKey: "choice:" + input.ID.String(),
		Payload: row.Payload, Events: &input.Events,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return result, storeerr.ErrConflict
	}
	if err != nil {
		return result, err
	}
	result = row
	if err := tx.Commit(ctx); err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	return result, nil
}

func (s *Store) ExpireIntegrationProfileChoice(ctx context.Context, projectID, integrationID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := s.lockIntegrationProfileChoice(ctx, tx, projectID, integrationID, id)
	if err != nil {
		return err
	}
	_, err = commitIntegrationProfileChoiceExpiry(ctx, tx, row)
	return err
}

func commitIntegrationProfileChoiceExpiry(
	ctx context.Context, tx pgx.Tx, row IntegrationProfileChoiceRecord,
) (IntegrationProfileChoiceRecord, error) {
	q := dbsqlc.New(tx)
	if row.SelectedKey == "" {
		if err := q.ExpireIntegrationState(ctx, dbsqlc.ExpireIntegrationStateParams{
			ProjectID: row.ProjectID, IntegrationID: row.IntegrationID, Kind: integrationProfileChoiceKind,
			ID: row.ID, ExpectedRevision: row.Revision,
		}); err != nil {
			return IntegrationProfileChoiceRecord{}, err
		}
	}
	row, err := getIntegrationProfileChoice(ctx, q, row.ProjectID, row.IntegrationID, row.ID)
	if err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	return row, nil
}

func (s *Store) lockIntegrationProfileChoice(
	ctx context.Context, tx pgx.Tx, projectID, integrationID, id uuid.UUID,
) (IntegrationProfileChoiceRecord, error) {
	if projectID == uuid.Nil || integrationID == uuid.Nil || id == uuid.Nil {
		return IntegrationProfileChoiceRecord{}, inboxInvalid("project, integration and choice are required")
	}
	if err := s.enterInboxIntegration(ctx, tx, projectID, integrationID); err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	q := dbsqlc.New(tx)
	row, err := getIntegrationProfileChoice(ctx, q, projectID, integrationID, id)
	if err != nil {
		return row, err
	}
	if err := LockConversationTx(ctx, tx, projectID, integrationID,
		row.Address); err != nil {
		return row, err
	}
	return getIntegrationProfileChoice(ctx, q, projectID, integrationID, id)
}

func getIntegrationProfileChoice(
	ctx context.Context, q *dbsqlc.Queries, projectID, integrationID, id uuid.UUID,
) (IntegrationProfileChoiceRecord, error) {
	state, err := q.GetIntegrationState(ctx, dbsqlc.GetIntegrationStateParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: integrationProfileChoiceKind, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	return integrationProfileChoiceRecord(state)
}

func getIntegrationProfileChoiceBySource(
	ctx context.Context, q *dbsqlc.Queries, projectID, integrationID uuid.UUID, sourceKey string,
) (IntegrationProfileChoiceRecord, error) {
	state, err := q.GetIntegrationStateByKey(ctx, dbsqlc.GetIntegrationStateByKeyParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: integrationProfileChoiceKind, Key: sourceKey,
	})
	if err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	return integrationProfileChoiceRecord(state)
}

func replaceIntegrationProfileChoice(
	ctx context.Context, q *dbsqlc.Queries, record IntegrationProfileChoiceRecord, requireUnexpired bool,
) (IntegrationProfileChoiceRecord, error) {
	data, err := encodeIntegrationProfileChoice(record)
	if err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	state, err := q.ReplaceIntegrationState(ctx, dbsqlc.ReplaceIntegrationStateParams{
		ProjectID: record.ProjectID, IntegrationID: record.IntegrationID, Kind: integrationProfileChoiceKind,
		ID: record.ID, ExpectedRevision: record.Revision, RequireUnexpired: requireUnexpired,
		Data: data,
	})
	if err != nil {
		return IntegrationProfileChoiceRecord{}, err
	}
	return integrationProfileChoiceRecord(state)
}

func integrationProfileChoiceIntegration(
	ctx context.Context, q *dbsqlc.Queries, projectID, integrationID uuid.UUID,
) (ProjectIntegrationRecord, error) {
	row, err := q.GetIntegrationProfileChoiceIntegrationForShare(
		ctx,
		dbsqlc.GetIntegrationProfileChoiceIntegrationForShareParams{ProjectID: projectID, ID: integrationID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectIntegrationRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if ProjectIntegrationState(row.State) != ProjectIntegrationStateActive {
		return ProjectIntegrationRecord{}, storeerr.ErrStateTransitionConflict
	}
	integration, err := projectIntegrationRecord(row)
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if integration.Settings.Launcher == nil {
		return ProjectIntegrationRecord{}, storeerr.ErrStateTransitionConflict
	}
	return integration, nil
}

func validateIntegrationProfileChoiceOptions(
	ctx context.Context, q *dbsqlc.Queries, integration ProjectIntegrationRecord, options []IntegrationProfileChoiceOption,
) error {
	ids := make([]uuid.UUID, 0, len(options))
	for _, option := range options {
		found := false
		for _, slot := range integration.Settings.Launcher.Slots {
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
	// Profile deletion checks integration references under its profile lock. Taking that
	// lock here would invert integration-save's profile-before-integration ordering.
	profiles, err := q.GetAgentProfileDisplayNames(ctx, dbsqlc.GetAgentProfileDisplayNamesParams{
		ProjectID: integration.ProjectID, ProfileIds: ids,
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

func validateIntegrationProfileChoice(input EnsureIntegrationProfileChoiceInput) error {
	if input.IntegrationID == uuid.Nil || !choiceText(input.SourceKey, IntegrationInboxMaxReceiptKeyBytes) {
		return inboxInvalid("choice requires an integration and bounded source key")
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
	if len(input.Options) == 0 || len(input.Options) > MaxIntegrationLaunchSlots {
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

func (s *Store) GetIntegrationProfileChoiceIntegrationID(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	// Cross-project routing hint only; callers must verify the callback against the owning integration.
	integrationID, err := s.q.GetIntegrationStateIntegrationID(ctx, dbsqlc.GetIntegrationStateIntegrationIDParams{
		Kind: integrationProfileChoiceKind, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return integrationID, err
}
