//go:build integration

package integrationstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type profileChoiceFixture struct {
	inboxFixture
	app    integrationstore.ProjectAppRecord
	input  integrationstore.EnsureAppProfileChoiceInput
	source integrationstore.IntegrationInboxRecord
}

func newProfileChoiceFixture(t *testing.T) profileChoiceFixture {
	t.Helper()
	base := newInboxFixture(t)
	base.store = integrationstore.New(base.pool, executionstore.IntegrationConnectionAccess{})
	var profileID, configID uuid.UUID
	require.NoError(t, base.pool.QueryRow(base.ctx,
		`SELECT id FROM agent_profiles WHERE project_id=$1`, base.project).Scan(&profileID))
	require.NoError(t, base.pool.QueryRow(base.ctx,
		`SELECT id FROM agent_configs WHERE project_id=$1 LIMIT 1`, base.project).Scan(&configID))
	second, err := executionstore.New(base.pool, executionstore.Config{}).
		CreateAgentProfile(base.ctx, executionstore.CreateAgentProfileInput{
			OrgID: base.org, ProjectID: base.project, Name: "Reviewer", CurrentConfigID: configID,
		})
	require.NoError(t, err)
	connection, err := publicid.Encode(publicid.KindIntegrationConnection, base.connection)
	require.NoError(t, err)
	app, err := base.store.CreateProjectApp(base.ctx, integrationstore.SaveProjectAppInput{
		OrgID: base.org, ProjectID: base.project, Name: "Support", DefinitionID: appdefinition.Slack, Enabled: true,
		Settings: integrationstore.ProjectAppSettings{
			Resource: agentconfig.AgentConfigAppResourceSource{Connection: connection},
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{
					{Key: "support", AgentProfileID: &profileID}, {Key: "review", AgentProfileID: &second.ID},
				}},
		},
	})
	require.NoError(t, err)
	f := profileChoiceFixture{inboxFixture: base, app: app}
	f.input = integrationstore.EnsureAppProfileChoiceInput{
		AppID: app.ID, Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
		SourceKey: "message-1", Event: json.RawMessage(`{"semantic_key":"message-1","text":"original","actor":"U123"}`),
		Payload: []byte("  {\"event\":{\"text\":\"original\"}}\n"),
		Options: []integrationstore.AppProfileChoiceOption{
			{Key: "support", ProfileID: profileID, Name: "Support"},
			{Key: "review", ProfileID: second.ID, Name: "Reviewer"},
		},
	}
	f.source = f.receipt(t, "source-1", f.input.Payload)
	return f
}

func (f profileChoiceFixture) receipt(
	t *testing.T, key string, payload []byte,
) integrationstore.IntegrationInboxRecord {
	t.Helper()
	accepted, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, ConnectionID: f.connection, ReceiptKey: key, Payload: payload,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Nil(t, accepted.Events, "ordinary ingress cannot populate trusted app events")
	claimed, found, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, ConnectionID: f.connection, LeaseDuration: integrationstore.IntegrationInboxMaxLease,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accepted.ID, claimed.ID)
	return claimed
}

func (f profileChoiceFixture) menu(t *testing.T) integrationstore.AppProfileChoiceRecord {
	t.Helper()
	record, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.connection, record.ID, "C123", "menu-1"))
	return f.readChoice(t, record.ID)
}

func (f profileChoiceFixture) readChoice(t *testing.T, id uuid.UUID) integrationstore.AppProfileChoiceRecord {
	t.Helper()
	record, err := f.store.GetAppProfileChoice(f.ctx, f.project, f.connection, id)
	require.NoError(t, err)
	return record
}

func (f profileChoiceFixture) chooseInput(
	t *testing.T, record integrationstore.AppProfileChoiceRecord, key string,
) integrationstore.ChooseAppProfileInput {
	t.Helper()
	connection, err := f.store.GetIntegrationConnection(f.ctx, f.project, f.connection)
	require.NoError(t, err)
	var event map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(record.Event, &event))
	var profileID uuid.UUID
	for _, option := range record.Options {
		if option.Key == key {
			profileID = option.ProfileID
		}
	}
	launches, err := json.Marshal([]map[string]any{{"app_id": record.AppID, "slot": key, "profile_id": profileID}})
	require.NoError(t, err)
	event["launches"] = launches
	event["directed"] = json.RawMessage(`true`)
	events, err := json.Marshal([]map[string]json.RawMessage{event})
	require.NoError(t, err)
	return integrationstore.ChooseAppProfileInput{
		ProjectID: f.project, ConnectionID: f.connection, ID: record.ID, Key: key, ActorID: "U456",
		MessageChannelID: record.MessageChannelID, MessageID: record.MessageID,
		SourceChoiceUpdatedAt: record.UpdatedAt, SourceConnectionUpdatedAt: connection.UpdatedAt, Events: events,
	}
}

func (f profileChoiceFixture) decidedReceipt(t *testing.T, id uuid.UUID) integrationstore.IntegrationInboxRecord {
	t.Helper()
	var receiptID uuid.UUID
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT id FROM integration_inbox WHERE project_id=$1 AND connection_id=$2 AND receipt_key=$3`,
		f.project, f.connection, "choice:"+id.String()).Scan(&receiptID))
	return f.read(t, receiptID)
}

func TestAppProfileChoiceConcurrentEnsureAndChoose(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	secondSource := f.receipt(t, "same-source-second-receipt", f.input.Payload)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var id, owner uuid.UUID
	createdCount := 0
	for i := range 8 {
		wg.Go(func() {
			source := f.source
			if i%2 != 0 {
				source = secondSource
			}
			record, created, err := f.store.EnsureAppProfileChoice(f.ctx, source.Lease(), f.input)
			if err != nil {
				t.Errorf("ensure choice: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if id == uuid.Nil {
				id, owner = record.ID, record.OwnerReceiptID
			}
			if owner != record.OwnerReceiptID {
				t.Errorf("concurrent receipt replaced publication owner: %s", record.OwnerReceiptID)
			}
			if created && record.OwnerReceiptID != source.ID {
				t.Errorf("creator did not retain publication ownership: %s", record.OwnerReceiptID)
			}
			if id != record.ID {
				t.Errorf("concurrent ensure allocated another choice: %s", record.ID)
			}
			if created {
				createdCount++
			}
		})
	}
	wg.Wait()
	require.Equal(t, 1, createdCount)
	require.Contains(t, []uuid.UUID{f.source.ID, secondSource.ID}, owner)
	pending := f.readChoice(t, id)
	require.InDelta(t, time.Hour.Seconds(), pending.ExpiresAt.Sub(pending.CreatedAt).Seconds(), 0.001)
	var agents, inputs int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT
    (SELECT count(*) FROM agents WHERE project_id=$1),
    (SELECT count(*) FROM agent_inputs WHERE project_id=$1)`, f.project).Scan(&agents, &inputs))
	require.Zero(t, agents)
	require.Zero(t, inputs)
	require.Equal(t, f.input.Payload, pending.Payload)
	require.Equal(t, f.input.Options, pending.Options)
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.connection, id, "C123", "menu-1"))
	pending = f.readChoice(t, id)
	first := f.chooseInput(t, pending, "support")
	second := f.chooseInput(t, pending, "review")
	second.ActorID = "U789" // Choosing is not restricted to the original author.
	winners := make(chan integrationstore.AppProfileChoiceRecord, 8)
	for index := range 8 {
		wg.Go(func() {
			input := first
			if index%2 != 0 {
				input = second
			}
			record, err := f.store.ChooseAppProfile(f.ctx, input)
			if err != nil {
				t.Errorf("choose profile: %v", err)
				return
			}
			winners <- record
		})
	}
	wg.Wait()
	close(winners)
	selected := f.readChoice(t, id)
	require.NotEmpty(t, selected.SelectedKey)
	for record := range winners {
		require.Equal(t, selected.SelectedKey, record.SelectedKey)
		require.Equal(t, selected.SelectedBy, record.SelectedBy)
	}
	receipt := f.decidedReceipt(t, id)
	require.Equal(t, f.input.Payload, receipt.Payload)
	winnerInput := first
	if selected.SelectedKey == second.Key {
		winnerInput = second
	}
	require.JSONEq(t, string(winnerInput.Events), string(receipt.Events), "receipt contains only the winner's intent")
	require.Nil(t, receipt.Plan)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_inbox WHERE project_id=$1`, f.project).Scan(&count))
	require.Equal(t, 3, count, "two source receipts share one decided receipt")
	claimed := f.claim(t)
	require.Equal(t, receipt.ID, claimed.ID)
	require.JSONEq(t, string(receipt.Events), string(claimed.Events))
	f.mutate(t, claimed, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		detached := work.Receipt()
		detached.Events[0] = '{'
		require.JSONEq(t, string(receipt.Events), string(work.Receipt().Events))
		return work.FreezePlan(f.ctx, json.RawMessage(`{}`))
	})
	// Deleting setup does not revoke an already committed decision or its receipt.
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.app.ID))
	replay, err := f.store.ChooseAppProfile(f.ctx, first)
	require.NoError(t, err)
	require.Equal(t, selected, replay)
	bySource, found, err := f.store.GetAppProfileChoiceBySource(
		f.ctx, f.project, f.connection, f.app.ID, f.input.SourceKey)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, selected, bySource)
}

func TestAppProfileChoiceSiblingSourceAndRevision(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	original := f.menu(t)
	stale := f.chooseInput(t, original, "support")
	unrelated := f.input
	unrelated.SourceKey = "message-2"
	unrelated.Event = json.RawMessage(`{"semantic_key":"message-2","files":["unrelated"]}`)
	unrelated.Payload = []byte(`{"event":{"files":["unrelated"]}}`)
	unrelated.HasAttachments = true
	other := f.receipt(t, "other-message", unrelated.Payload)
	reused, created, err := f.store.EnsureAppProfileChoice(f.ctx, other.Lease(), unrelated)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, original, reused, "a new mention cannot replace the original request or options")
	_, found, err := f.store.GetAppProfileChoiceBySource(f.ctx, f.project, f.connection, f.app.ID, unrelated.SourceKey)
	require.NoError(t, err)
	require.False(t, found)

	sibling := f.input
	sibling.HasAttachments = true
	sibling.Event = json.RawMessage(`{"semantic_key":"message-1-files","text":"original","files":["F123"]}`)
	sibling.Payload = []byte(`{"event":{"text":"original","files":[{"id":"F123"}]}}`)
	files := f.receipt(t, "same-message-files", sibling.Payload)
	merged, created, err := f.store.EnsureAppProfileChoice(f.ctx, files.Lease(), sibling)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, original.ID, merged.ID)
	require.Equal(t, f.source.ID, merged.OwnerReceiptID, "file sibling cannot take publication ownership")
	require.Equal(t, original.ExpiresAt, merged.ExpiresAt)
	require.Equal(t, original.Options, merged.Options)
	require.Equal(t, sibling.Payload, merged.Payload)
	require.JSONEq(t, string(sibling.Event), string(merged.Event))
	require.True(t, merged.UpdatedAt.After(original.UpdatedAt))
	_, err = f.store.ChooseAppProfile(f.ctx, stale)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	duplicate, _, err := f.store.EnsureAppProfileChoice(f.ctx, files.Lease(), sibling)
	require.NoError(t, err)
	require.Equal(t, merged.UpdatedAt, duplicate.UpdatedAt, "identical file replay preserves source revision")
	textReplay, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	require.Equal(t, merged, textReplay, "text sibling cannot remove attachments")
	chosen, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, merged, "review"))
	require.NoError(t, err)
	receipt := f.decidedReceipt(t, chosen.ID)
	require.Equal(t, sibling.Payload, receipt.Payload)
	later, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	require.Equal(t, chosen, later)
	lateFiles := sibling
	lateFiles.Event = json.RawMessage(`{"semantic_key":"message-1-files","files":["changed"]}`)
	later, _, err = f.store.EnsureAppProfileChoice(f.ctx, files.Lease(), lateFiles)
	require.NoError(t, err)
	require.Equal(t, chosen, later, "selected source and payload are immutable")
}

func TestAppProfileChoiceSourceChooseRace(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	original := f.menu(t)
	click := f.chooseInput(t, original, "support")
	files := f.input
	files.HasAttachments = true
	files.Event = json.RawMessage(`{"text":"original","files":["F123"]}`)
	files.Payload = []byte(`{"event":{"files":[{"id":"F123"}]}}`)
	sibling := f.receipt(t, "racing-files", files.Payload)
	var wg sync.WaitGroup
	var chosen integrationstore.AppProfileChoiceRecord
	var chooseErr, mergeErr error
	wg.Go(func() { chosen, chooseErr = f.store.ChooseAppProfile(f.ctx, click) })
	wg.Go(func() { _, _, mergeErr = f.store.EnsureAppProfileChoice(f.ctx, sibling.Lease(), files) })
	wg.Wait()
	require.NoError(t, mergeErr)
	if errors.Is(chooseErr, storeerr.ErrConflict) {
		latest := f.readChoice(t, original.ID)
		chosen, chooseErr = f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, latest, "support"))
	}
	require.NoError(t, chooseErr)
	receipt := f.decidedReceipt(t, chosen.ID)
	if bytes.Equal(chosen.Payload, files.Payload) {
		mergedInput := f.chooseInput(t, chosen, "support")
		require.JSONEq(t, string(mergedInput.Events), string(receipt.Events))
	} else {
		require.Equal(t, f.input.Payload, chosen.Payload)
		require.JSONEq(t, string(click.Events), string(receipt.Events))
	}
	require.Equal(t, chosen.Payload, receipt.Payload, "payload and normalized events cannot come from different revisions")
}

func TestAppProfileChoiceAuthorizationAndStaleness(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"project", "connection", "message", "channel", "unoffered", "expired",
		"disabled-app", "deleted-app", "remapped-slot", "removed-profile", "disabled-connection", "rotated-connection"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceFixture(t)
			record := f.menu(t)
			input := f.chooseInput(t, record, "support")
			want := storeerr.ErrStateTransitionConflict
			switch scenario {
			case "project":
				input.ProjectID = uuid.New()
				want = storeerr.ErrNotFound
			case "connection":
				input.ConnectionID = uuid.New()
				want = storeerr.ErrNotFound
			case "message":
				input.MessageID = "unrelated-message"
				want = storeerr.ErrUnauthorized
			case "channel":
				input.MessageChannelID = "other-channel"
				want = storeerr.ErrUnauthorized
			case "unoffered":
				input.Key = "never-offered"
			case "expired":
				f.exec(t, `UPDATE app_profile_choices SET expires_at=now()-interval '1 second' WHERE id=$1`, record.ID)
			case "disabled-app":
				f.exec(t, `UPDATE project_apps SET enabled=false WHERE id=$1`, f.app.ID)
			case "deleted-app":
				require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.app.ID))
			case "remapped-slot":
				f.exec(t, `UPDATE project_apps SET settings=jsonb_set(settings,
                    '{launcher,slots,0,agent_profile_id}',to_jsonb($2::uuid::text)) WHERE id=$1`,
					f.app.ID, f.input.Options[1].ProfileID)
			case "removed-profile":
				f.exec(t, `UPDATE agent_profiles SET deleted_at=now() WHERE id=$1`, f.input.Options[0].ProfileID)
			case "disabled-connection":
				_, err := f.store.DisableIntegrationConnection(f.ctx,
					integrationstore.DisableIntegrationConnectionInput{
						ProjectID: f.project, ID: f.connection, ExpectedOAuthFlowID: &uuid.Nil,
					})
				require.NoError(t, err)
				want = storeerr.ErrUnauthorized
			case "rotated-connection":
				f.exec(t, `UPDATE integration_connections SET updated_at=clock_timestamp() WHERE id=$1`, f.connection)
				want = storeerr.ErrUnauthorized
			}
			returned, err := f.store.ChooseAppProfile(f.ctx, input)
			require.ErrorIs(t, err, want)
			stored := f.readChoice(t, record.ID)
			require.Empty(t, stored.SelectedKey)
			switch scenario {
			case "disabled-app", "deleted-app", "remapped-slot", "removed-profile":
				require.True(t, stored.ExpiresAt.Before(record.ExpiresAt), "stale setup retires the unusable menu")
				require.False(t, stored.ExpiresAt.After(stored.UpdatedAt))
				require.Equal(t, stored, returned, "caller receives the committed expiry for safe dismissal")
			case "unoffered":
				require.Equal(t, record, returned, "unknown keys cannot retire a valid menu")
			case "project", "connection", "message", "channel", "disabled-connection", "rotated-connection":
				require.Equal(t, integrationstore.AppProfileChoiceRecord{}, returned)
			}
			var count int
			require.NoError(t, f.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM integration_inbox WHERE project_id=$1`, f.project).Scan(&count))
			require.Equal(t, 1, count, "rejected choices must not enqueue work")
		})
	}
}

func TestAppProfileChoiceExpiryAndCleanup(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	expired := f.menu(t)
	f.exec(t, `UPDATE app_profile_choices SET expires_at=now()-interval '1 second' WHERE id=$1`, expired.ID)
	replay, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, expired.ID, replay.ID)
	require.True(t, replay.ExpiresAt.Before(time.Now()))
	newInput := f.input
	newInput.SourceKey = "message-2"
	freshSource := f.receipt(t, "source-2", newInput.Payload)
	fresh, created, err := f.store.EnsureAppProfileChoice(f.ctx, freshSource.Lease(), newInput)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, expired.ID, fresh.ID)
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.connection, fresh.ID, "C123", "menu-2"))
	fresh = f.readChoice(t, fresh.ID)
	_, err = f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, fresh, "support"))
	require.NoError(t, err)
	_, err = f.store.CleanupAppProfileChoices(f.ctx, time.Hour, 100)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	count, err := f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.Zero(t, count, "expired source identities survive at least seven days")
	f.exec(t, `UPDATE app_profile_choices SET expires_at=now()-interval '8 days' WHERE project_id=$1`, f.project)
	count, err = f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "selected pending work keeps its source bookkeeping")
	selectedReceipt := f.decidedReceipt(t, fresh.ID)
	f.exec(t, `UPDATE integration_inbox SET state='processing',claim_token=$2,
        claim_expires_at=now()+interval '1 minute' WHERE id=$1`, selectedReceipt.ID, uuid.New())
	count, err = f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.Zero(t, count, "in-flight decided work keeps its source bookkeeping")
	f.exec(t, `UPDATE integration_inbox SET state='failed',claim_token=NULL,claim_expires_at=NULL WHERE id=$1`,
		selectedReceipt.ID)
	count, err = f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.Zero(t, count, "failed decided work remains recoverable")
	f.exec(t, `UPDATE integration_inbox SET state='completed',completed_at=now() WHERE id=$1`, selectedReceipt.ID)
	count, err = f.store.CleanupAppProfileChoices(f.ctx, integrationstore.AppProfileChoiceMinRetention, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	_, found, err := f.store.GetAppProfileChoiceBySource(f.ctx, f.project, f.connection, f.app.ID, fresh.SourceKey)
	require.NoError(t, err)
	require.False(t, found)
}

func TestAppProfileChoiceMessageFirstWinsAndLeaseFencing(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	record, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	unrecorded := f.chooseInput(t, record, "support")
	unrecorded.MessageChannelID, unrecorded.MessageID = "C123", "unrecorded-menu"
	_, err = f.store.ChooseAppProfile(f.ctx, unrecorded)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "unrecorded menus cannot choose")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			err := f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.connection, record.ID, "C123",
				fmt.Sprintf("menu-%d", i))
			if err != nil && !errors.Is(err, storeerr.ErrConflict) {
				t.Errorf("record choice message: %v", err)
			}
		})
	}
	wg.Wait()
	bound := f.readChoice(t, record.ID)
	require.NotEmpty(t, bound.MessageID)
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.connection, record.ID,
		bound.MessageChannelID, bound.MessageID))
	require.Equal(t, bound.UpdatedAt, f.readChoice(t, record.ID).UpdatedAt)
	_, err = f.store.GetAppProfileChoice(f.ctx, uuid.New(), f.connection, record.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, found, err := f.store.GetAppProfileChoiceBySource(f.ctx, f.project, uuid.New(), f.app.ID, f.input.SourceKey)
	require.NoError(t, err)
	require.False(t, found)
	f.exec(t, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, f.source.ID)
	_, _, err = f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
	require.Equal(t, bound, f.readChoice(t, record.ID))
}

func TestAppProfileChoiceInboxFailureRollsBackSelection(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	record := f.menu(t)
	f.exec(t, `CREATE FUNCTION reject_decided_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN IF NEW.events IS NOT NULL THEN RAISE EXCEPTION 'reject decided receipt'; END IF; RETURN NEW; END $$;
        CREATE TRIGGER reject_decided_receipt BEFORE INSERT ON integration_inbox
        FOR EACH ROW EXECUTE FUNCTION reject_decided_receipt()`)
	input := f.chooseInput(t, record, "support")
	_, err := f.store.ChooseAppProfile(f.ctx, input)
	require.Error(t, err)
	require.Equal(t, record, f.readChoice(t, record.ID), "failed handoff rolls back the winning choice")
	f.exec(t, `DROP TRIGGER reject_decided_receipt ON integration_inbox; DROP FUNCTION reject_decided_receipt()`)
	_, err = f.store.ChooseAppProfile(f.ctx, input)
	require.NoError(t, err)
	require.JSONEq(t, string(input.Events), string(f.decidedReceipt(t, record.ID).Events))
}

func TestAppProfileChoiceBoundsAndInboxEventsConstraint(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	for _, invalid := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`),
		json.RawMessage(`{"text":"` + string(bytes.Repeat([]byte{'x'}, 262144)) + `"}`)} {
		input := f.input
		input.Event = invalid
		_, _, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	record := f.menu(t)
	for _, invalid := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`),
		json.RawMessage(`["` + string(bytes.Repeat([]byte{'x'}, 262144)) + `"]`)} {
		input := f.chooseInput(t, record, "support")
		input.Events = invalid
		_, err := f.store.ChooseAppProfile(f.ctx, input)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		_, err = dbsqlc.New(f.pool).InsertAppProfileChoiceInboxReceipt(f.ctx,
			dbsqlc.InsertAppProfileChoiceInboxReceiptParams{
				ProjectID: f.project, ConnectionID: f.connection, ReceiptKey: uuid.NewString(),
				Payload: []byte(`{}`), Events: &invalid,
			})
		if invalid != nil {
			require.Error(t, err, "database enforces shape and size for all writers")
		}
	}
}

func TestAppProfileChoiceDoesNotInvertProfileSetupLockOrder(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	record := f.menu(t)
	input := f.chooseInput(t, record, "support")
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	// Setup update holds profile locks before it updates the app. A chooser
	// must not take that profile lock after taking its shared app row lock.
	_, err = tx.Exec(f.ctx, `SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`, f.input.Options[0].ProfileID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	selected, err := f.store.ChooseAppProfile(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "support", selected.SelectedKey)
	_, err = tx.Exec(ctx, `UPDATE project_apps SET name='Edited setup' WHERE id=$1`, f.app.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

func TestAppProfileChoiceCleanupSkipsLocksAndBoundsBatch(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	var first uuid.UUID
	for i := range 3 {
		input := f.input
		input.SourceKey = fmt.Sprintf("expired-%d", i)
		record, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), input)
		require.NoError(t, err)
		require.True(t, created)
		f.exec(t, `UPDATE app_profile_choices SET expires_at=now()-interval '8 days' WHERE id=$1`, record.ID)
		if i == 0 {
			first = record.ID
		}
	}
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT id FROM app_profile_choices WHERE id=$1 FOR UPDATE`, first)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for range 2 {
		count, err := f.store.CleanupAppProfileChoices(ctx, integrationstore.AppProfileChoiceMinRetention, 1)
		require.NoError(t, err)
		require.EqualValues(t, 1, count, "cleanup respects the requested batch size")
		require.Equal(t, first, f.readChoice(t, first).ID, "cleanup skips a busy retained choice")
	}
	count, err := f.store.CleanupAppProfileChoices(ctx, integrationstore.AppProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NoError(t, tx.Rollback(f.ctx))
	count, err = f.store.CleanupAppProfileChoices(ctx, integrationstore.AppProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.connection, first)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestAppProfileChoiceOwnerRecoveryAndReceiptRetention(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	pending, created, err := f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, f.source.ID, pending.OwnerReceiptID)
	require.Empty(t, pending.MessageID, "simulate creator interruption before provider publication")
	files := f.input
	files.HasAttachments = true
	files.Event = json.RawMessage(`{"semantic_key":"message-1-files","files":["F123"]}`)
	files.Payload = []byte(`{"event":{"files":[{"id":"F123"}]}}`)
	sibling := f.receipt(t, "file-sibling", files.Payload)
	merged, created, err := f.store.EnsureAppProfileChoice(f.ctx, sibling.Lease(), files)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, pending.OwnerReceiptID, merged.OwnerReceiptID)
	require.NotEqual(t, sibling.ID, merged.OwnerReceiptID, "sibling must not publish")
	f.mutate(t, sibling, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
			return err
		}
		return work.Complete(f.ctx)
	})
	f.exec(t, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, f.source.ID)
	count, err := f.store.RecoverIntegrationInbox(f.ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	recovered := f.claim(t)
	require.Equal(t, f.source.ID, recovered.ID)
	require.NotEqual(t, f.source.ClaimToken, recovered.ClaimToken)
	_, _, err = f.store.EnsureAppProfileChoice(f.ctx, f.source.Lease(), f.input)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
	restarted := integrationstore.New(f.pool, executionstore.IntegrationConnectionAccess{})
	resumed, created, err := restarted.EnsureAppProfileChoice(f.ctx, recovered.Lease(), f.input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, merged, resumed, "creator retry retains merged source and publication authority")
	require.NoError(t, restarted.RecordAppProfileChoiceMessage(f.ctx, f.project, f.connection, pending.ID,
		"C123", "recovered-menu"))
	f.mutate(t, recovered, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		if err := work.FreezePlan(f.ctx, json.RawMessage(`{}`)); err != nil {
			return err
		}
		return work.Complete(f.ctx)
	})
	f.exec(t, `UPDATE integration_inbox SET completed_at=now()-interval '2 seconds' WHERE id=$1`, recovered.ID)
	count, err = f.store.CleanupTerminalIntegrationInbox(f.ctx, time.Second, 100)
	require.NoError(t, err)
	require.Positive(t, count)
	_, err = f.store.GetIntegrationInbox(f.ctx, f.project, recovered.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	retained := f.readChoice(t, pending.ID)
	require.Equal(t, recovered.ID, retained.OwnerReceiptID, "owner UUID is provenance, not a receipt retention dependency")
	chosen, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, retained, "support"))
	require.NoError(t, err)
	require.Equal(t, retained.OwnerReceiptID, chosen.OwnerReceiptID)
	receipt := f.decidedReceipt(t, chosen.ID)
	require.NotEqual(t, chosen.OwnerReceiptID, receipt.ID, "decided work never acquires publication ownership")
	require.Equal(t, files.Payload, receipt.Payload)
}
