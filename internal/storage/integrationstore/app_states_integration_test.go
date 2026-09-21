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

// A second app-defined document uses the same primitives without chooser fields,
// an agent, a conversation, or a deadline. These are internal store operations;
// semantic workflows still own lifecycle gates and authorization.
func TestAppStateIdentityAndRevision(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	q := dbsqlc.New(f.pool)
	input := dbsqlc.InsertAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: "onboarding", Key: "user-1",
		Data: json.RawMessage(`{"answers":{}}`),
	}
	state, err := q.InsertAppState(f.ctx, input)
	require.NoError(t, err)
	require.Nil(t, state.ExpiresAt)
	require.Nil(t, state.ScopeRef)
	require.EqualValues(t, 1, state.Revision)
	otherApp := f.addApp(t, "other-state-app", f.app.Settings)
	_, err = q.GetAppState(f.ctx, dbsqlc.GetAppStateParams{
		ProjectID: f.project, AppID: otherApp.ID, Kind: input.Kind, ID: state.ID,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = f.store.GetAppProfileChoice(f.ctx, f.project, f.appID, state.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "another state kind cannot be used as a menu")
	_, err = f.store.GetAppProfileChoiceAppID(f.ctx, state.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "callback owner discovery also checks kind")

	update := dbsqlc.ReplaceAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: input.Kind, ID: state.ID,
		ExpectedRevision: state.Revision, RequireUnexpired: true, Data: json.RawMessage(`{"answers":{"name":"Ada"}}`),
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := q.ReplaceAppState(f.ctx, update); errs <- err })
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
	state, err = q.GetAppStateByKey(f.ctx, dbsqlc.GetAppStateByKeyParams{
		ProjectID: f.project, AppID: f.appID, Kind: input.Kind, Key: input.Key,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, state.Revision)
	require.JSONEq(t, string(update.Data), string(state.Data))

	// Indefinite state is live until explicitly expired; expiry preserves data.
	require.NoError(t, q.ExpireAppState(f.ctx, dbsqlc.ExpireAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: state.Kind, ID: state.ID, ExpectedRevision: state.Revision,
	}))
	expired, err := q.GetAppState(f.ctx, dbsqlc.GetAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: state.Kind, ID: state.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, expired.ExpiresAt)
	require.Equal(t, state.Revision+1, expired.Revision)
	require.Equal(t, state.Data, expired.Data)
}

func TestAppStateDeadlineAndDeletedOwnership(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	q := dbsqlc.New(f.pool)
	state, err := q.InsertAppState(f.ctx, dbsqlc.InsertAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: "confirmation", Key: "request-1",
		Data: json.RawMessage(`{"approved":false}`), LifetimeMilliseconds: time.Hour.Milliseconds(),
	})
	require.NoError(t, err)
	// Time passing does not change the revision. A write requiring a live
	// decision deadline must still reject, independently of the revision fence.
	f.exec(t, `UPDATE app_states SET expires_at=now()-interval '8 days' WHERE id=$1`, state.ID)
	_, err = q.ReplaceAppState(f.ctx, dbsqlc.ReplaceAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: state.Kind, ID: state.ID,
		ExpectedRevision: state.Revision, RequireUnexpired: true, Data: state.Data,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	retained, err := q.GetAppState(f.ctx, dbsqlc.GetAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: state.Kind, ID: state.ID,
	})
	require.NoError(t, err, "expired state remains readable for workflow recovery")
	require.Equal(t, state.Revision, retained.Revision)
	count, err := f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.Zero(t, count, "a deadline is not a deletion policy for arbitrary state")
	indefinite, err := q.InsertAppState(f.ctx, dbsqlc.InsertAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: "draft", Key: "draft-1", Data: json.RawMessage(`{"draft":true}`),
	})
	require.NoError(t, err)
	require.Nil(t, indefinite.ExpiresAt)
	require.NoError(t, f.store.DeleteProjectApp(f.ctx, f.org, f.project, f.appID))
	count, err = f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "deleted ownership reclaims every state kind")
	count, err = f.store.CleanupAppStates(f.ctx, integrationstore.AppProfileChoiceMinRetention, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "deleted ownership also reclaims records without deadlines")
	_, err = q.GetAppState(f.ctx, dbsqlc.GetAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: indefinite.Kind, ID: indefinite.ID,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = q.GetAppState(f.ctx, dbsqlc.GetAppStateParams{
		ProjectID: f.project, AppID: f.appID, Kind: state.Kind, ID: state.ID,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestAppProfileChoiceMaximumSourceRoundTrip(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	input := f.input
	input.Payload = bytes.Repeat([]byte{0, 255, '<', '\n'}, integrationstore.IntegrationInboxMaxPayloadBytes/4)
	input.Event = json.RawMessage(`{"text":"` + strings.Repeat("a", integrationstore.IntegrationInboxMaxEventsBytes-512) + `"}`)
	input.SourceKey = "maximum-source"
	receipt := f.receipt(t, "maximum-source", input.Payload)
	choice, created, err := f.store.EnsureAppProfileChoice(f.ctx, receipt.Lease(), input)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, input.Payload, choice.Payload, "base64 preserves exact arbitrary provider bytes")
	require.JSONEq(t, string(input.Event), string(choice.Event))
	require.NoError(t, f.store.RecordAppProfileChoiceMessage(f.ctx, f.project, f.appID, choice.ID, "C123", "large-menu"))
	choice = f.readChoice(t, choice.ID)
	selected, err := f.store.ChooseAppProfile(f.ctx, f.chooseInput(t, choice, "support"))
	require.NoError(t, err)
	require.Equal(t, input.Payload, selected.Payload)
	var payload []byte
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT payload FROM integration_inbox WHERE project_id=$1 AND app_id=$2 AND receipt_key=$3`,
		f.project, f.appID, "choice:"+choice.ID.String()).Scan(&payload))
	require.Equal(t, input.Payload, payload, "trusted handoff preserves the complete source")
}

func TestAppProfileChoicePendingLookupAcrossRetainedHistory(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceFixture(t)
	live := f.menu(t)
	f.mutate(t, f.source, func(work *integrationstore.IntegrationInboxLeaseTx) error {
		return work.Fail(f.ctx, "publication owner no longer runnable")
	})
	// Older abandoned records must not hide the live published menu. Every
	// candidate filter belongs before LIMIT; no capped Go scan can prove absence.
	f.exec(t, `INSERT INTO app_states
 (project_id,app_id,kind,key,scope_kind,scope_ref,data,expires_at)
 SELECT project_id,app_id,kind,'abandoned-'||n,scope_kind,scope_ref,
        data-'message_id'-'message_channel_id',expires_at-interval '30 minutes'
 FROM app_states CROSS JOIN generate_series(1,200) n WHERE id=$1`, live.ID)
	// Another kind may use the same JSON key with a completely different type.
	f.exec(t, `INSERT INTO app_states
 (project_id,app_id,kind,key,scope_kind,scope_ref,data,expires_at)
 SELECT project_id,app_id,'onboarding','unrelated',scope_kind,scope_ref,
        '{"owner_receipt_id":"not-a-uuid"}'::jsonb,expires_at
 FROM app_states WHERE id=$1`, live.ID)
	input := f.input
	input.SourceKey = "later-request"
	receipt := f.receipt(t, "later-request", input.Payload)
	found, created, err := f.store.EnsureAppProfileChoice(f.ctx, receipt.Lease(), input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, live, found)
}
