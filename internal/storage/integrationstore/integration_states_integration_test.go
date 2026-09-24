//go:build integration

package integrationstore_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationStateIdentityAndRevision(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	q := dbsqlc.New(f.pool)
	input := dbsqlc.InsertIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: "onboarding", Key: "user-1",
		Data: json.RawMessage(`{"answers":{}}`),
	}
	state, err := q.InsertIntegrationState(f.ctx, input)
	require.NoError(t, err)
	require.Nil(t, state.ExpiresAt)
	require.Nil(t, state.ScopeRef)
	require.EqualValues(t, 1, state.Revision)
	otherIntegration := f.addIntegration(t, "other-state-integration", f.integration.Settings)
	_, err = q.GetIntegrationState(f.ctx, dbsqlc.GetIntegrationStateParams{
		ProjectID: f.project, IntegrationID: otherIntegration.ID, Kind: input.Kind, ID: state.ID,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = f.store.GetIntegrationProfileChoice(f.ctx, f.project, f.integrationID, state.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "another state kind cannot be used as a menu")
	_, err = f.store.GetIntegrationProfileChoiceIntegrationID(f.ctx, state.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "callback owner discovery also checks kind")

	update := dbsqlc.ReplaceIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: input.Kind, ID: state.ID,
		ExpectedRevision: state.Revision, RequireUnexpired: true, Data: json.RawMessage(`{"answers":{"name":"Ada"}}`),
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := q.ReplaceIntegrationState(f.ctx, update); errs <- err })
	}
	wg.Wait()
	close(errs)
	var winners, conflicts int
	for err := range errs {
		if err == nil {
			winners++
		} else {
			require.ErrorIs(t, err, pgx.ErrNoRows)
			conflicts++
		}
	}
	require.Equal(t, 1, winners)
	require.Equal(t, 1, conflicts)
	state, err = q.GetIntegrationStateByKey(f.ctx, dbsqlc.GetIntegrationStateByKeyParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: input.Kind, Key: input.Key,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, state.Revision)
	require.JSONEq(t, string(update.Data), string(state.Data))

	require.NoError(t, q.ExpireIntegrationState(f.ctx, dbsqlc.ExpireIntegrationStateParams{

		ProjectID:        f.project,
		IntegrationID:    f.integrationID,
		Kind:             state.Kind,
		ID:               state.ID,
		ExpectedRevision: state.Revision,
	}))
	expired, err := q.GetIntegrationState(f.ctx, dbsqlc.GetIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: state.Kind, ID: state.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, expired.ExpiresAt)
	require.Equal(t, state.Revision+1, expired.Revision)
	require.Equal(t, state.Data, expired.Data)
}

func TestIntegrationStateDeadlineAndDeletedOwnership(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	q := dbsqlc.New(f.pool)
	state, err := q.InsertIntegrationState(f.ctx, dbsqlc.InsertIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: "confirmation", Key: "request-1",
		Data: json.RawMessage(`{"approved":false}`), LifetimeMilliseconds: time.Hour.Milliseconds(),
	})
	require.NoError(t, err)
	f.exec(t, `UPDATE integration_states SET expires_at=now()-interval '8 days' WHERE id=$1`, state.ID)
	_, err = q.ReplaceIntegrationState(f.ctx, dbsqlc.ReplaceIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: state.Kind, ID: state.ID,
		ExpectedRevision: state.Revision, RequireUnexpired: true, Data: state.Data,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	retained, err := q.GetIntegrationState(f.ctx, dbsqlc.GetIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: state.Kind, ID: state.ID,
	})
	require.NoError(t, err, "expired state remains readable for workflow recovery")
	require.Equal(t, state.Revision, retained.Revision)
	count, err := f.store.CleanupIntegrationStates(f.ctx, integrationstore.IntegrationProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.Zero(t, count, "a deadline is not a deletion policy for arbitrary state")
	indefinite, err := q.InsertIntegrationState(f.ctx, dbsqlc.InsertIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: "draft", Key: "draft-1", Data: json.RawMessage(`{"draft":true}`),
	})
	require.NoError(t, err)
	require.Nil(t, indefinite.ExpiresAt)
	require.NoError(t, f.store.DeleteProjectIntegration(f.ctx, f.org, f.project, f.integrationID))
	count, err = f.store.CleanupIntegrationStates(f.ctx, integrationstore.IntegrationProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "deleted ownership reclaims every state kind")
	count, err = f.store.CleanupIntegrationStates(f.ctx, integrationstore.IntegrationProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "deleted ownership also reclaims records without deadlines")
	_, err = q.GetIntegrationState(f.ctx, dbsqlc.GetIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: indefinite.Kind, ID: indefinite.ID,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = q.GetIntegrationState(f.ctx, dbsqlc.GetIntegrationStateParams{
		ProjectID: f.project, IntegrationID: f.integrationID, Kind: state.Kind, ID: state.ID,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestIntegrationProfileChoiceMaximumSourceRoundTrip(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	input := f.input
	input.Payload = bytes.Repeat([]byte{0, 255, '<', '\n'}, integrationstore.IntegrationInboxMaxPayloadBytes/4)
	input.Event = json.RawMessage(`{"text":"` + strings.Repeat("a", integrationstore.IntegrationInboxMaxEventsBytes-512) + `"}`)
	input.SourceKey = "maximum-source"
	receipt := f.receipt(t, "maximum-source", input.Payload)
	choice, created, err := f.store.EnsureIntegrationProfileChoice(f.ctx, receipt.Lease(), input)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, input.Payload, choice.Payload, "base64 preserves exact arbitrary provider bytes")
	require.JSONEq(t, string(input.Event), string(choice.Event))
	require.NoError(
		t,
		f.store.RecordIntegrationProfileChoiceMessage(f.ctx, f.project, f.integrationID, choice.ID, "C123", "large-menu"),
	)
	choice = f.readChoice(t, choice.ID)
	selected, err := f.store.ChooseIntegrationProfile(f.ctx, f.chooseInput(t, choice, "support"))
	require.NoError(t, err)
	require.Equal(t, input.Payload, selected.Payload)
	var payload []byte
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT payload FROM integration_inbox WHERE project_id=$1 AND integration_id=$2 AND receipt_key=$3`,
		f.project, f.integrationID, "choice:"+choice.ID.String()).Scan(&payload))
	require.Equal(t, input.Payload, payload, "trusted handoff preserves the complete source")
}

func TestIntegrationProfileChoicePendingLookupAcrossRetainedHistory(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	live := f.menu(t)
	f.mutate(t, f.source, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.Fail(f.ctx, "publication owner no longer runnable")
	})
	f.exec(t, `INSERT INTO integration_states
 (project_id,integration_id,kind,key,scope_kind,scope_ref,data,expires_at)
 SELECT project_id,integration_id,kind,'abandoned-'||n,scope_kind,scope_ref,
        data-'message_id'-'message_channel_id',expires_at-interval '30 minutes'
 FROM integration_states CROSS JOIN generate_series(1,200) n WHERE id=$1`, live.ID)
	f.exec(t, `INSERT INTO integration_states
 (project_id,integration_id,kind,key,scope_kind,scope_ref,data,expires_at)
 SELECT project_id,integration_id,'onboarding','unrelated',scope_kind,scope_ref,
        '{"owner_receipt_id":"not-a-uuid"}'::jsonb,expires_at
 FROM integration_states WHERE id=$1`, live.ID)
	input := f.input
	input.SourceKey = "later-request"
	receipt := f.receipt(t, "later-request", input.Payload)
	found, created, err := f.store.EnsureIntegrationProfileChoice(f.ctx, receipt.Lease(), input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, live, found)
}
