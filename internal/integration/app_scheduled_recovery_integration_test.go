//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledDiscordRecoversRootWithoutFrozenPlan(t *testing.T) {
	for _, test := range []struct {
		name          string
		deleteProfile bool
	}{
		{name: "plan write fails then recovers"},
		{name: "profile deleted during publication", deleteProfile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newScheduledProviderJourney(t, appdefinition.ProviderDiscord)
			remote, provider := newDiscordInboxFixture(t)
			var err error
			remote.appSetup, err = f.store.Integrations().GetProjectAppByID(t.Context(), f.appID)
			require.NoError(t, err)
			remote.identity.ApplicationID = remote.appSetup.ProviderTenantID
			// The saved root can be fetched by ID for thread creation without a nonce.
			remote.message = discord.Message{ID: "500", ChannelID: "300", Author: discord.User{ID: "22", Bot: true}}
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
			f.consumer.providers[appdefinition.ProviderDiscord] = provider
			receipt := f.fire()
			if !test.deleteProfile {
				// Fail the real plan transaction after a confirmed provider send. The
				// consumer must roll it back and separately retain the root receipt.
				_, err := f.pool.Exec(t.Context(), `
CREATE FUNCTION fail_scheduled_plan() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'injected scheduled plan failure';
END $$;
CREATE TRIGGER fail_scheduled_plan BEFORE UPDATE OF plan ON integration_inbox
FOR EACH ROW WHEN (OLD.plan IS NULL AND NEW.plan IS NOT NULL)
EXECUTE FUNCTION fail_scheduled_plan()`)
				require.NoError(t, err)
			}
			worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
			err = worker.consume(t.Context(), receipt)
			if test.deleteProfile {
				require.Error(t, err)
			} else {
				require.ErrorContains(t, err, "injected scheduled plan failure")
			}
			saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxPending, saved.State)
			require.Empty(t, saved.Plan, "publication succeeded but routing was never frozen")
			preparation, err := saved.ScheduledPreparation()
			require.NoError(t, err)
			require.NotNil(t, preparation.AttemptedAt)
			require.Equal(t, &f.provider.root, preparation.Root)
			require.EqualValues(t, 1, posts.Load())
			require.Zero(t, remote.posts, "thread preparation must wait for a frozen plan")
			var agents int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Zero(t, agents)

			if !test.deleteProfile {
				_, err := f.pool.Exec(t.Context(), `DROP TRIGGER fail_scheduled_plan ON integration_inbox`)
				require.NoError(t, err)
			}
			_, err = f.pool.Exec(t.Context(),
				`UPDATE integration_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
			require.NoError(t, err)
			resumed := f.claim()
			require.NotEqual(t, receipt.ClaimToken, resumed.ClaimToken)
			err = worker.consume(t.Context(), resumed)
			if test.deleteProfile {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			after, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.JSONEq(t, string(saved.Preparation), string(after.Preparation))
			require.EqualValues(t, 1, posts.Load(), "a new lease must reuse the confirmed opening")
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			if test.deleteProfile {
				require.Zero(t, agents, "profile deletion after publication must never leave an orphan agent")
				require.Empty(t, after.Plan)
				require.Equal(t, integrationstore.IntegrationInboxPending, after.State)
				require.Zero(t, remote.posts)
			} else {
				require.Equal(t, 1, agents)
				require.Equal(t, integrationstore.IntegrationInboxCompleted, after.State)
				plan, err := decodeAppInboxPlan(after.Plan)
				require.NoError(t, err)
				require.Len(t, plan, 1)
				require.Equal(t, *preparation.Root, plan["scheduled"].Scope)
				require.Equal(t, 1, remote.posts, "recovery creates the thread for the saved root")
			}
		})
	}
}
