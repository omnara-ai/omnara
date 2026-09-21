//go:build integration

package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func pendingPermissionPresentation(
	t *testing.T,
	f integrationToolFixture,
	runner BackgroundRunner,
) executionstore.AgentInteractionRecord {
	t.Helper()
	ctx := t.Context()
	prepareInteractionPromptFixture(t, ctx, f)
	turn := f.turn()
	turn.Tools = map[string]ToolSpec{
		"list_processes": {Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)},
	}
	call := f.recordPendingToolCall(t, ctx, "pending-presentation", "list_processes", `{}`, f.Now)
	require.NoError(t, (Executor{Store: f.Store, BackgroundRunner: runner}).PrepareToolCallPermission(ctx, turn, call))
	return integrationToolInteraction(
		t,
		ctx,
		f,
		f.toolCallID(t, ctx, call.ID),
		executionstore.AgentInteractionKindPermission,
	)
}

func TestInteractionDeliveryRecoversUnstartedQueueWork(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "queue_full", true: "restart_before_execution"}[queued], func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "unstarted-presentation")
			var offers int
			runner := backgroundRunnerFunc(func(string, func(context.Context) error) bool { offers++; return queued })
			interaction := pendingPermissionPresentation(t, f, runner)
			require.Equal(t, 1, offers)
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				if r.URL.Path != "/chat.postMessage" {
					t.Errorf("unexpected request %s", r.URL.Path)
					http.Error(w, "unexpected request", http.StatusInternalServerError)
					return
				}
				posts.Add(1)
				writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
			}))
			defer server.Close()
			// A new presenter/runner has no access to the previous process's queue.
			presenter := integration.InteractionPresenter{
				Store:      f.Store,
				HTTPClient: integrationProviderTestClient(server),
			}
			require.NoError(t, presenter.EnqueuePending(ctx, immediateIntegrationBackgroundRunner(ctx)))
			require.NoError(t, presenter.EnqueuePending(ctx, immediateIntegrationBackgroundRunner(ctx)))
			require.EqualValues(t, 1, posts.Load())
			current, found, err := f.Store.Execution().GetAgentInteraction(
				ctx,
				toolsTestProjectID,
				f.Agent.ID,
				interaction.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.NotEmpty(t, current.PresentationReceipt)
		})
	}
}

func TestInteractionDeliveryConcurrentPresentersPublishOnce(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "concurrent-presentation")
	interaction := pendingPermissionPresentation(t, f, nil)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()
	presenter := integration.InteractionPresenter{Store: f.Store, HTTPClient: integrationProviderTestClient(server)}
	var group sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		group.Go(func() { errors <- presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID) })
	}
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, posts.Load())
}

func TestInteractionDeliveryNeverRepostsClaimedWork(t *testing.T) {
	for _, scenario := range []string{"crash_after_claim", "uncertain_publication", "receipt_write_failure", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "claimed-presentation")
			interaction := pendingPermissionPresentation(t, f, nil)
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				switch r.URL.Path {
				case "/chat.postMessage":
					posts.Add(1)
					if scenario == "uncertain_publication" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
				case "/conversations.replies":
					writeToolTestJSON(w, map[string]any{"ok": true, "messages": []any{}, "has_more": false})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.Error(w, "unexpected request", http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			presenter := integration.InteractionPresenter{
				Store:      f.Store,
				HTTPClient: integrationProviderTestClient(server),
			}
			switch scenario {
			case "crash_after_claim":
				claimed, err := f.Store.Execution().ClaimInteractionPresentation(
					ctx,
					toolsTestProjectID,
					f.Agent.ID,
					interaction.ID,
				)
				require.NoError(t, err)
				require.True(t, claimed)
			case "receipt_write_failure":
				_, err := f.Pool.Exec(
					ctx,
					`CREATE FUNCTION reject_presentation_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF NEW.presentation_receipt IS NOT NULL THEN RAISE EXCEPTION 'receipt store unavailable';
                END IF; RETURN NEW; END $$;
				CREATE TRIGGER reject_presentation_receipt BEFORE UPDATE ON agent_interactions
                FOR EACH ROW EXECUTE FUNCTION reject_presentation_receipt()`,
				)
				require.NoError(t, err)
			case "revoked":
				_, err := f.Store.Integrations().DisconnectProjectApp(
					ctx,
					integrationstore.DisconnectProjectAppInput{
						ProjectID:             toolsTestProjectID,
						AppID:                 f.Install.ID,
						ExpectedSetupRevision: &f.Install.SetupRevision,
					},
				)
				require.NoError(t, err)
			}
			err := presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
			if scenario == "crash_after_claim" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if scenario == "receipt_write_failure" {
				_, err := f.Pool.Exec(ctx, `DROP TRIGGER reject_presentation_receipt ON agent_interactions`)
				require.NoError(t, err)
			}
			require.NoError(t, presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID))
			require.NoError(t, presenter.EnqueuePending(ctx, immediateIntegrationBackgroundRunner(ctx)))
			want := int32(1)
			if scenario == "crash_after_claim" || scenario == "revoked" {
				want = 0
			}
			require.Equal(t, want, posts.Load())
			pending, err := f.Store.Execution().ListPendingInteractionPresentations(
				ctx,
				[]string{string(appdefinition.SlackThread)},
				10,
			)
			require.NoError(t, err)
			require.Empty(t, pending)
			current, found, err := f.Store.Execution().GetAgentInteraction(
				ctx,
				toolsTestProjectID,
				f.Agent.ID,
				interaction.ID,
			)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.Empty(t, current.PresentationReceipt)
		})
	}
}

func TestInteractionDeliveryRetriesOnlyConfirmedReceipt(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "retry-presentation-receipt")
	interaction := pendingPermissionPresentation(t, f, nil)
	_, err := f.Pool.Exec(ctx, `CREATE SEQUENCE presentation_receipt_attempts;
	CREATE FUNCTION fail_first_presentation_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.presentation_receipt IS NOT NULL AND nextval('presentation_receipt_attempts') = 1
	THEN RAISE EXCEPTION 'temporary receipt storage failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER fail_first_presentation_receipt BEFORE UPDATE ON agent_interactions
	FOR EACH ROW EXECUTE FUNCTION fail_first_presentation_receipt()`)
	require.NoError(t, err)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()
	presenter := integration.InteractionPresenter{Store: f.Store, HTTPClient: integrationProviderTestClient(server)}
	require.NoError(t, presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID))
	require.NoError(t, presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID))
	require.EqualValues(t, 1, posts.Load())
	current, found, err := f.Store.Execution().GetAgentInteraction(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, current.PresentationReceipt)
	var attempts int
	require.NoError(t, f.Pool.QueryRow(ctx, `SELECT last_value FROM presentation_receipt_attempts`).Scan(&attempts))
	require.Equal(t, 2, attempts)
}
