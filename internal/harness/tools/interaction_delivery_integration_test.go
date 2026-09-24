//go:build integration

package tools

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
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
	presenter := integrationruntime.InteractionPresenter{Store: f.Store, HTTPClient: integrationProviderTestClient(server)}
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

func TestInteractionDeliveryRetriesPreflightWithinAttempt(t *testing.T) {
	for _, scenario := range []string{
		"identity_transient", "identity_short_throttle", "identity_long_throttle", "identity_forbidden",
		"identity_exhausted", "revoked_during_retry", "send_5xx_ratelimited",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixture(t, ctx, "preflight-presentation")
			interaction := pendingPermissionPresentation(t, f, nil)
			var identities, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.test" {
					attempt := identities.Add(1)
					if scenario == "identity_forbidden" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if scenario == "identity_short_throttle" && attempt == 1 {
						w.Header().Set("Retry-After", "1")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if scenario == "identity_long_throttle" {
						w.Header().Set("Retry-After", "120")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if scenario == "revoked_during_retry" {
						_, err := f.Store.Integrations().DisconnectProjectIntegration(ctx,
							integrationstore.DisconnectProjectIntegrationInput{ProjectID: toolsTestProjectID, IntegrationID: f.Install.ID})
						if err != nil {
							t.Error(err)
						}
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if scenario == "identity_exhausted" || (scenario == "identity_transient" && attempt == 1) {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					serveSlackToolIdentity(w, r)
					return
				}
				switch r.URL.Path {
				case "/chat.postMessage":
					posts.Add(1)
					if scenario == "send_5xx_ratelimited" {
						w.WriteHeader(http.StatusServiceUnavailable)
						writeToolTestJSON(w, map[string]any{"ok": false, "error": "ratelimited"})
						return
					}
					writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
				case "/conversations.replies":
					writeToolTestJSON(w, map[string]any{"ok": true, "messages": []any{}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			presenter := integrationruntime.InteractionPresenter{
				Store:      f.Store,
				HTTPClient: integrationProviderTestClient(server),
			}
			err := presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
			success := scenario == "identity_transient" || scenario == "identity_short_throttle"
			if success {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			wantIdentities := int32(1)
			if success {
				wantIdentities = 2
			}
			if scenario == "identity_exhausted" {
				wantIdentities = 3
			}
			require.Equal(t, wantIdentities, identities.Load())
			wantPosts := int32(0)
			if success || scenario == "send_5xx_ratelimited" {
				wantPosts = 1
			}
			require.Equal(t, wantPosts, posts.Load())
			if success {
				require.NoError(t, presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID))
				require.Equal(t, wantIdentities, identities.Load(), "confirmed receipt skips preflight")
				require.Equal(t, wantPosts, posts.Load(), "confirmed receipt prevents reposting")
			}
			current, found, err := f.Store.Execution().GetAgentInteraction(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.Equal(t, success, len(current.PresentationReceipt) > 0)
		})
	}
}

type presentationReadKeyWrapper struct {
	secrets.KeyWrapper
	attempts int
}

func (w *presentationReadKeyWrapper) UnwrapDataKey(
	ctx context.Context, key secrets.WrappedDataKey, associatedData []byte,
) ([]byte, error) {
	w.attempts++
	if w.attempts == 1 {
		return nil, io.EOF
	}
	return w.KeyWrapper.UnwrapDataKey(ctx, key, associatedData)
}

func TestInteractionDeliveryRetriesCredentialRead(t *testing.T) {
	ctx := t.Context()
	f := newIntegrationToolFixture(t, ctx, "credential-preflight")
	interaction := pendingPermissionPresentation(t, f, nil)
	wrapper := &presentationReadKeyWrapper{KeyWrapper: integrationToolKeyWrapper(t)}
	store := storage.NewStore(f.Pool, storage.WithSecretKeyWrapper(wrapper))
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveSlackToolIdentity(w, r) {
			return
		}
		if r.URL.Path != "/chat.postMessage" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		posts.Add(1)
		writeToolTestJSON(w, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
	}))
	defer server.Close()
	presenter := integrationruntime.InteractionPresenter{Store: store, HTTPClient: integrationProviderTestClient(server)}
	require.NoError(t, presenter.Present(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID))
	require.Equal(t, 2, wrapper.attempts)
	require.EqualValues(t, 1, posts.Load())
	current, found, err := store.Execution().GetAgentInteraction(ctx, toolsTestProjectID, f.Agent.ID, interaction.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, current.PresentationReceipt)
}
