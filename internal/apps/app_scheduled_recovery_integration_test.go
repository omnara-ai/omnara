//go:build integration

package apps

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scheduledPlanResponseFailure struct {
	AppRoutingStore
	commitErr, readErr error
	planSaved          bool
}

func (s *scheduledPlanResponseFailure) WithAppInboxLease(
	ctx context.Context,
	lease appstore.AppInboxLease,
	fn func(*appstore.AppInboxLeaseTx) error,
) error {
	var froze bool
	err := s.AppRoutingStore.WithAppInboxLease(
		ctx, lease, func(work *appstore.AppInboxLeaseTx) error {
			unplanned := len(work.Receipt().Plan) == 0
			if err := fn(work); err != nil {
				return err
			}
			froze = unplanned && len(work.Receipt().Plan) != 0
			return nil
		},
	)
	if err == nil && froze {
		s.planSaved = true
		return s.commitErr
	}
	return err
}

func (s *scheduledPlanResponseFailure) GetAppInbox(
	ctx context.Context, projectID, receiptID uuid.UUID,
) (appstore.AppInboxRecord, error) {
	if s.planSaved && s.readErr != nil {
		err := s.readErr
		s.readErr = nil
		return appstore.AppInboxRecord{}, err
	}
	return s.AppRoutingStore.GetAppInbox(ctx, projectID, receiptID)
}

func TestScheduledLaunchReusesCommittedPlanAfterLostResponse(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		name := "commit response lost"
		if failRead {
			name = "read response lost"
		}
		t.Run(name, func(t *testing.T) {
			f := newScheduledJourney(t)
			receipt := f.fire()
			failure := errors.New("injected plan response lost")
			store := &scheduledPlanResponseFailure{AppRoutingStore: f.store.Apps()}
			if failRead {
				store.readErr = failure
			} else {
				store.commitErr = failure
			}
			f.consumer.inbox = store
			f.handler.inbox = store
			f.consumer.router.apps = store
			worker := NewAppInboxWorker(f.store.Apps(), f.consumer, AppInboxWorkerOptions{})
			err := worker.consume(t.Context(), receipt)
			if failRead {
				require.ErrorIs(t, err, failure)
				require.NotErrorIs(t, err, ErrScheduledActionFailed)
				saved, readErr := f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
				require.NoError(t, readErr)
				require.Equal(t, appstore.AppInboxPending, saved.State)
				require.NotEmpty(t, saved.Plan)
				_, err = f.pool.Exec(t.Context(),
					`UPDATE app_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
				require.NoError(t, err)
				receipt = f.claim()
				err = worker.consume(t.Context(), receipt)
			}
			require.NoError(t, err)
			require.True(t, store.planSaved)
			saved, err := f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, appstore.AppInboxCompleted, saved.State)
			require.NotEmpty(t, saved.Plan)
			var agents int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Equal(t, 1, agents)
			require.Equal(t, 1, f.provider.posts, "a saved plan must prevent reposting after either lost response")
		})
	}
}

func TestScheduledDiscordStartupFailureIsTerminal(t *testing.T) {
	for _, test := range []struct {
		name          string
		deleteProfile bool
		unknown       bool
		wantPosts     int
	}{
		{name: "plan write fails after publication", wantPosts: 1},
		{name: "profile deleted during publication", deleteProfile: true, wantPosts: 1},
		{name: "publication remains unknown", unknown: true, wantPosts: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, remote := newScheduledDiscordJourney(t)
			receipt := f.fire()
			nonce := base64.RawURLEncoding.EncodeToString(receipt.ID[:])
			var posts atomic.Int32
			remote.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/v10/channels/300/messages" {
					return false
				}
				if !assert.Equal(t, http.MethodPost, r.Method, "recovery must not scan history") {
					w.WriteHeader(http.StatusNotFound)
					return true
				}
				posts.Add(1)
				var body struct {
					Nonce string `json:"nonce"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return true
				}
				assert.Equal(t, nonce, body.Nonce)
				if test.unknown {
					w.WriteHeader(http.StatusInternalServerError)
					return true
				}
				if test.deleteProfile {
					if !assert.NoError(t, f.store.Execution().DeleteAgentProfile(r.Context(), f.ids.ProjectID, f.profile.ID)) {
						w.WriteHeader(http.StatusInternalServerError)
						return true
					}
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"id": "500", "channel_id": "300", "author": map[string]any{"id": "22", "bot": true},
					"nonce": body.Nonce,
				}))
				return true
			}
			if !test.deleteProfile && !test.unknown {
				_, err := f.pool.Exec(t.Context(), `
CREATE FUNCTION fail_scheduled_plan() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected scheduled plan failure';
END $$;
CREATE TRIGGER fail_scheduled_plan BEFORE UPDATE OF plan ON app_inbox
FOR EACH ROW WHEN (OLD.plan IS NULL AND NEW.plan IS NOT NULL)
EXECUTE FUNCTION fail_scheduled_plan()`)
				require.NoError(t, err)
			}
			worker := NewAppInboxWorker(f.store.Apps(), f.consumer, AppInboxWorkerOptions{})
			err := worker.consume(t.Context(), receipt)
			require.ErrorIs(t, err, ErrScheduledActionFailed)
			if !test.deleteProfile && !test.unknown {
				require.ErrorContains(t, err, "injected scheduled plan failure")
			}
			saved, err := f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, appstore.AppInboxFailed, saved.State)
			require.Equal(t, 1, saved.AttemptCount)
			require.Empty(t, saved.Plan)
			require.EqualValues(t, test.wantPosts, posts.Load())
			require.Zero(t, remote.posts, "thread preparation must wait for a frozen plan")
			var agents int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Zero(t, agents, "a failed plan or deleted profile must never leave an orphan agent")
			_, err = f.pool.Exec(t.Context(),
				`UPDATE app_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
			require.NoError(t, err)
			worked, err := worker.RunOnce(t.Context())
			require.NoError(t, err)
			require.False(t, worked, "a terminal startup failure must not repost through the retry budget")
			require.EqualValues(t, test.wantPosts, posts.Load())
		})
	}
}

func TestScheduledDiscordLeaseLossBeforePlanCanLeaveUnusedHeading(t *testing.T) {
	f, remote := newScheduledDiscordJourney(t)
	receipt := f.fire()
	nonce := base64.RawURLEncoding.EncodeToString(receipt.ID[:])
	var posts atomic.Int32
	remote.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v10/channels/300/messages" {
			return false
		}
		if !assert.Equal(t, http.MethodPost, r.Method) {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		var body struct {
			Nonce string `json:"nonce"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		assert.Equal(t, nonce, body.Nonce, "receipt nonce stays stable even across lease recovery")
		if posts.Add(1) == 1 {
			_, err := f.pool.Exec(r.Context(),
				`UPDATE app_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
			if !assert.NoError(t, err) {
				w.WriteHeader(http.StatusInternalServerError)
				return true
			}
		} else {
			remote.message.ID = "501"
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"id": remote.message.ID, "channel_id": "300", "author": remote.message.Author, "nonce": body.Nonce,
		}))
		return true
	}
	worker := NewAppInboxWorker(f.store.Apps(), f.consumer, AppInboxWorkerOptions{})
	err := worker.consume(t.Context(), receipt)
	require.ErrorIs(t, err, appstore.ErrAppInboxLeaseLost)
	saved, err := f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, appstore.AppInboxProcessing, saved.State)
	require.Empty(t, saved.Plan)
	var agents int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Zero(t, agents)
	require.Zero(t, remote.posts)
	recovered, err := f.store.Apps().RecoverAppInbox(t.Context(), 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered)
	resumed := f.claim()
	require.Equal(t, receipt.ID, resumed.ID)
	require.NotEqual(t, receipt.ClaimToken, resumed.ClaimToken)
	require.NoError(t, worker.consume(t.Context(), resumed))
	saved, err = f.store.Apps().GetAppInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, appstore.AppInboxCompleted, saved.State)
	plan, err := decodeAppInboxPlan(saved.Plan)
	require.NoError(t, err)
	require.Equal(t, "501", plan["scheduled"].Scope.Discord.ThreadID)
	replayed, err := f.consumer.Consume(t.Context(), resumed.Lease())
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	require.False(t, replayed[0].Launch.Created)
	require.Equal(t, plan["scheduled"].AgentID, replayed[0].Launch.Agent.ID)
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
	require.Equal(t, 1, agents, "the unused first heading must not have an agent")
	require.EqualValues(t, 2, posts.Load())
	require.Equal(t, 1, remote.posts, "only the replacement heading gets a thread")
}

func newScheduledDiscordJourney(t *testing.T) (*scheduledJourney, *discordInboxFixture) {
	t.Helper()
	f := newScheduledProviderJourney(t, appdefinition.ProviderDiscord)
	remote, provider := newDiscordInboxFixture(t)
	var err error
	remote.appSetup, err = f.store.Apps().GetProjectAppByID(t.Context(), f.appID)
	require.NoError(t, err)
	remote.identity.ApplicationID = remote.appSetup.ProviderTenantID
	remote.message = discord.Message{ID: "500", ChannelID: "300", Author: discord.User{ID: "22", Bot: true}}
	f.handler.provider = provider
	return f, remote
}
