//go:build integration

package integrationstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { integrationdb.RunTestMain(m) }

type inboxFixture struct {
	ctx                               context.Context //nolint:containedctx // Fixture shares test cancellation.
	pool                              *pgxpool.Pool
	store                             *integrationstore.Store
	org, project, integrationID, user uuid.UUID
}

func newInboxFixture(t *testing.T) inboxFixture {
	t.Helper()
	ctx := t.Context()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	wrapper, err := secrets.NewLocalKeyWrapper("inbox-fixture", map[string][]byte{
		"inbox-fixture": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	identity := identitystore.New(pool, wrapper, nil)
	_, err = identity.AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: ids.OrgID, UserID: ids.ProviderAdminUserID, Role: "owner",
	})
	require.NoError(t, err)
	execution := executionstore.New(pool, executionstore.Config{})
	config := storagefixture.SeedAgentConfig(t, ctx, modelstore.New(pool), execution, ids.OrgID, ids.ProjectID,
		"instruction: inbox test\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	_, err = execution.CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "inbox-profile", CurrentConfigID: config.ID,
	})
	require.NoError(t, err)
	credential, version, err := secretstore.New(pool, wrapper, identity).CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: ids.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: ids.ProjectID,
		Name: "inbox-fixture", Actor: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-inbox-test", ClientID: "inbox-client",
			ClientSecret: "inbox-secret", SigningSecret: "inbox-signing",
		},
	})
	require.NoError(t, err)
	store := integrationstore.New(pool, executionstore.IntegrationAccess{})
	integration, err := store.CreateIntegration(ctx, integrationstore.SaveIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "inbox-integration",
		IntegrationKind: integrationdefinition.SlackThread,
	})
	require.NoError(t, err)
	integration, err = store.ConfigureIntegration(ctx, integrationstore.ConfigureIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, IntegrationID: integration.ID,
		InstalledByUserID: ids.ProviderAdminUserID, Provider: integrationstore.IntegrationProviderSlack,
		ProviderTenantID: "T123", ProviderAccountRef: "inbox-integration",
		CredentialSecretID: credential.ID, CredentialVersionID: version.ID,
		ExpectedSetupRevision: integration.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
	})
	require.NoError(t, err)
	return inboxFixture{
		ctx: ctx, pool: pool, store: store, org: ids.OrgID,
		project: ids.ProjectID, integrationID: integration.ID, user: ids.ProviderAdminUserID,
	}
}

func (f inboxFixture) accept(t *testing.T, key string) integrationstore.IntegrationInboxRecord {
	t.Helper()
	r, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, IntegrationID: f.integrationID, ReceiptKey: key, Payload: []byte("  {\"verified\": true}\n"),
	})
	require.NoError(t, err)
	require.True(t, created)
	return r
}

func (f inboxFixture) addIntegration(
	t *testing.T,
	name string,
	settings integrationstore.IntegrationSettings,
) integrationstore.IntegrationRecord {
	t.Helper()
	template, err := f.store.GetIntegration(f.ctx, f.project, f.integrationID)
	require.NoError(t, err)
	credential, err := secretstore.New(f.pool, nil, nil).GetSecret(f.ctx, f.org, template.CredentialSecretID)
	require.NoError(t, err)
	integration, err := f.store.CreateIntegration(f.ctx, integrationstore.SaveIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: name, IntegrationKind: template.IntegrationKind, Settings: settings,
	})
	require.NoError(t, err)
	integration, err = f.store.ConfigureIntegration(f.ctx, integrationstore.ConfigureIntegrationInput{
		OrgID: f.org, ProjectID: f.project, IntegrationID: integration.ID, InstalledByUserID: f.user,
		Provider: template.Provider, ProviderTenantID: template.ProviderTenantID,
		ProviderAccountRef: template.ProviderAccountRef,
		CredentialSecretID: credential.ID, CredentialVersionID: credential.CurrentVersionID,
		ExpectedSetupRevision: integration.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
	})
	require.NoError(t, err)
	return integration
}
func (f inboxFixture) claim(t *testing.T) integrationstore.IntegrationInboxRecord {
	t.Helper()
	r, ok, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, ok)
	return r
}
func (f inboxFixture) mutate(
	t *testing.T, r integrationstore.IntegrationInboxRecord, apply func(*integrationstore.IntegrationInboxLeaseTx) error,
) {
	t.Helper()
	require.NoError(t, f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), apply))
}
func (f inboxFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, sql, args...)
	require.NoError(t, err)
}
func (f inboxFixture) read(t *testing.T, id uuid.UUID) integrationstore.IntegrationInboxRecord {
	t.Helper()
	r, err := f.store.GetIntegrationInbox(f.ctx, f.project, id)
	require.NoError(t, err)
	return r
}

func TestInboxVerifiedReceiptDeduplicationAndIsolation(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	first := f.accept(t, "same-event")
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			r, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.project, IntegrationID: f.integrationID,
				ReceiptKey: "same-event", Payload: []byte(`{"changed":"retry metadata"}`),
			})
			if err != nil || created || r.ID != first.ID || string(r.Payload) != string(first.Payload) {
				t.Errorf("dedup failed: created=%v err=%v", created, err)
			}
		})
	}
	wg.Wait()
	require.Equal(t, []byte("  {\"verified\": true}\n"), f.read(t, first.ID).Payload)
	payload := bytes.Repeat([]byte{0, 255}, integrationstore.IntegrationInboxMaxPayloadBytes/2)
	binary, created, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project, IntegrationID: f.integrationID, ReceiptKey: "binary", Payload: payload,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, payload, f.read(t, binary.ID).Payload)

	_, err = f.store.GetIntegrationInbox(f.ctx, uuid.New(), first.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, _, err = f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: uuid.New(), IntegrationID: f.integrationID, ReceiptKey: "cross-project", Payload: []byte{1},
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	for _, sql := range []string{
		`UPDATE integration_inbox SET payload=decode(repeat('00',1048577),'hex') WHERE id=$1`,
		`UPDATE integration_inbox SET payload=''::bytea WHERE id=$1`,
		`UPDATE integration_inbox SET receipt_key=repeat('x',513) WHERE id=$1`,
		`UPDATE integration_inbox SET plan=jsonb_build_object('x',repeat('x',12615681)) WHERE id=$1`,
		`UPDATE integration_inbox SET last_error=repeat('x',4097) WHERE id=$1`,
		`UPDATE integration_inbox SET attempt_count=-1 WHERE id=$1`,
		`UPDATE integration_inbox SET state='failed' WHERE id=$1`,
		`UPDATE integration_inbox SET completed_at=now() WHERE id=$1`,
	} {
		_, err := f.pool.Exec(f.ctx, sql, first.ID)
		require.Error(t, err, sql)
	}
}

func TestInboxClaimSkipsLockedAndHasSingleOwner(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	first := f.accept(t, "first")
	second := f.accept(t, "second")
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT id FROM integration_inbox WHERE id=$1 FOR UPDATE`, first.ID)
	require.NoError(t, err)
	claimed := f.claim(t)
	require.Equal(t, second.ID, claimed.ID)
	require.NoError(t, tx.Rollback(f.ctx))
	next := f.claim(t)
	require.Equal(t, first.ID, next.ID)
	require.NotEqual(t, claimed.ClaimToken, next.ClaimToken)
	_, ok, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, ok)
	for i := range 12 {
		f.accept(t, fmt.Sprintf("concurrent-%d", i))
	}
	var mu sync.Mutex
	seen := map[uuid.UUID]bool{}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			r, ok, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: f.project, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
			})
			if err != nil || !ok {
				t.Errorf("claim failed: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[r.ID] {
				t.Errorf("duplicate claim %s", r.ID)
			}
			seen[r.ID] = true
		})
	}
	wg.Wait()
	require.Len(t, seen, 12)
}

func TestInboxCrashRecoveryExhaustsBudgetAndPreservesPlan(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "crashing")
	r := f.claim(t)
	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		plan := json.RawMessage(`{"message":{},"recipients":{"slot":{"agent_id":"pinned","artifact_id":"pinned-blob"}}}`)
		return w.FreezePlan(f.ctx, plan)
	})
	original := f.read(t, r.ID)
	for attempt := 1; attempt <= integrationstore.IntegrationInboxMaxAttempts; attempt++ {
		require.Equal(t, attempt, r.AttemptCount)
		f.exec(t, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, r.ID)
		n, err := f.store.RecoverIntegrationInbox(f.ctx, 1)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		err = f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
			return w.Fail(f.ctx, "stale")
		})
		require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
		if attempt < integrationstore.IntegrationInboxMaxAttempts {
			r = f.claim(t)
		}
	}
	final := f.read(t, r.ID)
	require.Equal(t, integrationstore.IntegrationInboxFailed, final.State)
	require.Nil(t, final.ClaimExpiresAt)
	require.Equal(t, uuid.Nil, final.ClaimToken)
	require.JSONEq(t, string(original.Plan), string(final.Plan))
	_, ok, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, ok)
	require.NotNil(t, final.CompletedAt)
}

func TestInboxFrozenPlanRetryAndAtomicCompletion(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "slots")
	r := f.claim(t)
	plan := json.RawMessage(`{"message":{},"recipients":{"one":{"agent_id":"one"},"two":{"agent_id":"two"}}}`)
	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.FreezePlan(f.ctx, plan)
	})
	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		snapshot := w.Receipt()
		snapshot.Plan[0] = 'x'
		require.JSONEq(t, string(plan), string(w.Receipt().Plan))
		return w.FreezePlan(f.ctx, plan)
	})
	err := f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.FreezePlan(f.ctx, json.RawMessage(`{"message":{},"recipients":{"one":{"agent_id":"replacement"}}}`))
	})
	require.ErrorIs(t, err, storeerr.ErrConflict)
	original := f.read(t, r.ID)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	w, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, r.Lease())
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx, `UPDATE users SET display_name='admitted' WHERE id=$1`, f.user)
	require.NoError(t, err)
	require.NoError(t, w.Complete(f.ctx))
	require.NoError(t, tx.Rollback(f.ctx))
	var display string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT display_name FROM users WHERE id=$1`, f.user).Scan(&display))
	require.NotEqual(t, "admitted", display)
	require.Equal(t, original, f.read(t, r.ID), "completion and caller writes roll back together")

	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.Retry(f.ctx, time.Second, "temporarily unavailable")
	})
	pending := f.read(t, r.ID)
	require.Equal(t, integrationstore.IntegrationInboxQueued, pending.State)
	require.Equal(t, original.Plan, pending.Plan)
	f.exec(t, `UPDATE integration_inbox SET next_attempt_at=now() WHERE id=$1`, r.ID)
	reclaimed := f.claim(t)
	require.NotEqual(t, r.ClaimToken, reclaimed.ClaimToken)
	err = f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.Complete(f.ctx)
	})
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)

	tx, err = f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	w, err = f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, reclaimed.Lease())
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx, `UPDATE users SET display_name='admitted' WHERE id=$1`, f.user)
	require.NoError(t, err)
	require.NoError(t, w.Complete(f.ctx))
	require.NoError(t, tx.Commit(f.ctx))
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT display_name FROM users WHERE id=$1`, f.user).Scan(&display))
	require.Equal(t, "admitted", display)
	completed := f.read(t, r.ID)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	require.NotNil(t, completed.CompletedAt)
	require.Nil(t, completed.ClaimExpiresAt)
	require.Equal(t, uuid.Nil, completed.ClaimToken)
	require.Empty(t, completed.LastError)
	require.Equal(t, original.Plan, completed.Plan)
	err = f.store.WithIntegrationInboxLease(f.ctx, reclaimed.Lease(),
		func(w *integrationstore.IntegrationInboxLeaseTx) error {
			return w.Retry(f.ctx, time.Second, "late retry")
		})
	require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
}

func TestInboxCompleteRequiresValidFrozenPlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, plan string }{
		{"unplanned", ""},
		{"invalid slot", `{"slot":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			f.accept(t, "invalid-plan")
			r := f.claim(t)
			if tc.plan != "" {
				f.exec(t, `UPDATE integration_inbox SET plan=$2::jsonb WHERE id=$1`, r.ID, tc.plan)
			}
			before := f.read(t, r.ID)
			err := f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
				return w.Complete(f.ctx)
			})
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			require.Equal(t, before, f.read(t, r.ID))
		})
	}
}

func TestInboxLeaseRevalidatedAfterLockWaitAndInsideTransaction(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "expiring")
	r := f.claim(t)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT id FROM integration_inbox WHERE id=$1 FOR UPDATE`, r.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
			return w.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`))
		})
	}()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationInboxReceipt", 1)
	_, err = tx.Exec(f.ctx, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, r.ID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(t, <-done, integrationstore.ErrIntegrationInboxLeaseLost)
	_, err = f.store.RecoverIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	r = f.claim(t)
	tx, err = f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	w, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, r.Lease())
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx, `UPDATE users SET display_name='expired admission' WHERE id=$1`, f.user)
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx,
		`UPDATE integration_inbox SET claim_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, r.ID)
	require.NoError(t, err)
	require.ErrorIs(t, w.CheckLease(f.ctx), integrationstore.ErrIntegrationInboxLeaseLost)
	require.ErrorIs(t, w.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`)), integrationstore.ErrIntegrationInboxLeaseLost)
	require.ErrorIs(t, w.Complete(f.ctx), integrationstore.ErrIntegrationInboxLeaseLost)
	require.NoError(t, tx.Rollback(f.ctx))
	var display string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT display_name FROM users WHERE id=$1`, f.user).Scan(&display))
	require.NotEqual(t, "expired admission", display)
}

func TestInboxRetrySchedulingFailureAndBoundedCleanup(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "retry")
	r := f.claim(t)
	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.Retry(f.ctx, time.Hour, strings.Repeat("🙂", 4096))
	})
	read := f.read(t, r.ID)
	require.Equal(t, integrationstore.IntegrationInboxQueued, read.State)
	require.Nil(t, read.CompletedAt)
	require.LessOrEqual(t, len(read.LastError), 4096)
	ready, err := f.store.ListReadyIntegrationInboxIntegrations(f.ctx, integrationstore.IntegrationInboxIntegration{}, 100)
	require.NoError(t, err)
	require.Empty(t, ready.Integrations)
	_, ok, err := f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.False(t, ok)
	f.exec(t, `UPDATE integration_inbox SET next_attempt_at=now(),attempt_count=7 WHERE id=$1`, r.ID)
	r = f.claim(t)
	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.Retry(f.ctx, time.Second, "last failure")
	})
	require.Equal(t, integrationstore.IntegrationInboxFailed, f.read(t, r.ID).State)
	require.NotNil(t, f.read(t, r.ID).CompletedAt)
	for i := range 3 {
		f.accept(t, fmt.Sprintf("done-%d", i))
		claimed := f.claim(t)
		f.mutate(t, claimed, func(w *integrationstore.IntegrationInboxLeaseTx) error {
			if err := w.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`)); err != nil {
				return err
			}
			return w.Complete(f.ctx)
		})
	}
	f.exec(t, `UPDATE integration_inbox SET completed_at=now()-interval '1 day' WHERE state='completed'`)
	n, err := f.store.CleanupTerminalIntegrationInbox(f.ctx, time.Hour, 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	n, err = f.store.CleanupTerminalIntegrationInbox(f.ctx, time.Hour, 2)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, integrationstore.IntegrationInboxFailed, f.read(t, r.ID).State)
	require.NotNil(t, f.read(t, r.ID).CompletedAt)
}

func TestInboxScopeLifecycleFencesAdmissionAndPurgesDeletedPayloads(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "processing")
	r := f.claim(t)
	pending := f.accept(t, "queued")
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	require.NoError(t, lifecyclelock.EnterActiveProject(f.ctx, tx, f.org, f.project))
	require.NoError(t, dbsqlc.New(tx).LockIntegrationLifecycleExclusive(f.ctx,
		dbsqlc.LockIntegrationLifecycleExclusiveParams{IntegrationID: f.integrationID}))
	done := make(chan error, 1)
	go func() {
		_, _, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
			ProjectID: f.project, IntegrationID: f.integrationID, ReceiptKey: "late", Payload: []byte{1},
		})
		done <- err
	}()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationLifecycleShared", 1)
	_, err = tx.Exec(f.ctx, `UPDATE integrations SET state='disconnected' WHERE id=$1`, f.integrationID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(t, <-done, storeerr.ErrUnauthorized)
	err = f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`))
	})
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	ready, err := f.store.ListReadyIntegrationInboxIntegrations(f.ctx, integrationstore.IntegrationInboxIntegration{}, 100)
	require.NoError(t, err)
	require.Empty(t, ready.Integrations)
	n, err := f.store.RecoverIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, integrationstore.IntegrationInboxFailed, f.read(t, pending.ID).State)
	require.NotNil(t, f.read(t, pending.ID).CompletedAt)
	require.Equal(t, integrationstore.IntegrationInboxProcessing, f.read(t, r.ID).State)
	n, err = f.store.RecoverIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.Zero(t, n)
	f.exec(t, `UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`, r.ID)
	n, err = f.store.RecoverIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, integrationstore.IntegrationInboxFailed, f.read(t, r.ID).State)
	require.NotNil(t, f.read(t, r.ID).CompletedAt)
	n, err = f.store.CleanupDeletedIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, f.store.DeleteIntegration(f.ctx, f.org, f.project, f.integrationID))
	n, err = f.store.CleanupDeletedIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	n, err = f.store.CleanupDeletedIntegrationInbox(f.ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func TestInboxRejectsDeletedProjectAndOrganization(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"project", "organization"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			f := newInboxFixture(t)
			r := f.accept(t, "queued")
			if scope == "project" {
				f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
			} else {
				f.exec(t, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.org)
			}
			_, _, err := f.store.AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.project, IntegrationID: f.integrationID, ReceiptKey: "late", Payload: []byte{1},
			})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			_, _, err = f.store.ClaimIntegrationInbox(f.ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: f.project, IntegrationID: f.integrationID, LeaseDuration: time.Minute,
			})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			n, err := f.store.CleanupDeletedIntegrationInbox(f.ctx, 1)
			require.NoError(t, err)
			require.EqualValues(t, 1, n)
			_, err = f.store.GetIntegrationInbox(f.ctx, f.project, r.ID)
			require.True(t, errors.Is(err, storeerr.ErrNotFound))
		})
	}
}

func TestInboxMultipleHandlesObserveFrozenPlanAndTerminalState(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "shared-transaction")
	r := f.claim(t)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	first, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, r.Lease())
	require.NoError(t, err)
	second, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, r.Lease())
	require.NoError(t, err)
	plan := json.RawMessage(`{"message":{},"recipients":{"one":{},"two":{}}}`)
	require.NoError(t, first.FreezePlan(f.ctx, plan))
	require.ErrorIs(t, second.FreezePlan(f.ctx, json.RawMessage(`{"message":{},"recipients":{"replacement":{}}}`)), storeerr.ErrConflict)
	require.NoError(t, second.Complete(f.ctx))
	require.ErrorIs(t, first.Complete(f.ctx), integrationstore.ErrIntegrationInboxLeaseLost)
	require.NoError(t, tx.Commit(f.ctx))
	completed := f.read(t, r.ID)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	require.JSONEq(t, string(plan), string(completed.Plan))
}

func TestInboxDisableWaitsForAtomicAdmission(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "disable-race")
	r := f.claim(t)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	w, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, r.Lease())
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
			ProjectID: f.project, IntegrationID: f.integrationID,
		})
		done <- err
	}()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationLifecycleExclusive", 1)
	require.NoError(t, w.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`)))
	require.NoError(t, w.Complete(f.ctx))
	require.NoError(t, tx.Commit(f.ctx))
	require.NoError(t, <-done)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, f.read(t, r.ID).State)
}

func TestInboxJSONBNormalizedSizeBoundaryIsExplicitAndAtomic(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.accept(t, "json-size")
	r := f.claim(t)
	prefix, suffix := `{"message":{"data":"`, `"},"recipients":{}}`
	remaining := integrationstore.IntegrationInboxMaxPlanBytes - len(prefix) - len(suffix)
	oversized := json.RawMessage(prefix + strings.Repeat("x", remaining) + suffix)
	require.Len(t, oversized, integrationstore.IntegrationInboxMaxPlanBytes)
	err := f.store.WithIntegrationInboxLease(f.ctx, r.Lease(), func(w *integrationstore.IntegrationInboxLeaseTx) error {
		return w.FreezePlan(f.ctx, oversized)
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.Empty(t, f.read(t, r.ID).Plan)
	fits := json.RawMessage(prefix + strings.Repeat("x", remaining-32) + suffix)
	f.mutate(t, r, func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.FreezePlan(f.ctx, fits) })
	require.JSONEq(t, string(fits), string(f.read(t, r.ID).Plan))
}

func TestInboxSetupUpdateWaitsForAtomicAdmission(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.store = integrationstore.New(f.pool, executionstore.IntegrationAccess{})
	wrapper, err := secrets.NewLocalKeyWrapper("inbox-test", map[string][]byte{
		"inbox-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	secretStore := secretstore.New(f.pool, wrapper, identitystore.New(f.pool, wrapper, nil))
	credential, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
		Name: "inbox-update", Actor: identitystore.NewUserPrincipal(f.user),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-inbox-test", ClientID: "inbox-client",
			ClientSecret: "inbox-secret", SigningSecret: "inbox-signing",
		},
	})
	require.NoError(t, err)
	integration, err := f.store.GetIntegration(f.ctx, f.project, f.integrationID)
	require.NoError(t, err)
	input := integrationstore.ConfigureIntegrationInput{

		OrgID:             f.org,
		ProjectID:         f.project,
		IntegrationID:     f.integrationID,
		InstalledByUserID: f.user,

		Provider:           integration.Provider,
		ProviderTenantID:   integration.ProviderTenantID,
		ProviderAccountRef: integration.ProviderAccountRef,

		CredentialSecretID:  credential.ID,
		CredentialVersionID: version.ID,

		ExpectedSetupRevision: integration.SetupRevision,
		OAuthFlowID:           uuid.Must(uuid.NewV7()),
	}
	f.accept(t, "update-race")
	receipt := f.claim(t)
	tx, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	work, err := f.store.LockIntegrationInboxLeaseTx(f.ctx, tx, receipt.Lease())
	require.NoError(t, err)
	_, err = tx.Exec(f.ctx, `SELECT id FROM secrets WHERE id=$1 FOR UPDATE`, credential.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := f.store.ConfigureIntegration(f.ctx, input); done <- err }()
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationLifecycleExclusive", 1)
	during, err := f.store.GetIntegration(f.ctx, f.project, f.integrationID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationStateActive, during.State)
	require.NoError(t, work.FreezePlan(f.ctx, json.RawMessage(`{"recipients":{}}`)))
	require.NoError(t, work.Complete(f.ctx))
	require.NoError(t, tx.Commit(f.ctx))
	require.NoError(t, <-done)
	after, err := f.store.GetIntegration(f.ctx, f.project, f.integrationID)
	require.NoError(t, err)
	require.Equal(t, f.integrationID, after.ID)
	require.Equal(t, integrationstore.IntegrationStateActive, after.State)
	require.Equal(t, integration.SetupRevision+1, after.SetupRevision)
	require.Equal(t, credential.ID, after.CredentialSecretID)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, f.read(t, receipt.ID).State)
}

func TestInboxDiscoveryPagesIntegrationsIndependentlyOfBacklog(t *testing.T) {
	t.Parallel()
	f, _, setup := integrationSetupFixture(t)
	var integrations []integrationstore.IntegrationInboxIntegration
	for i := range 4 {
		integration, err := f.store.CreateIntegration(f.ctx, integrationstore.SaveIntegrationInput{

			OrgID:           f.org,
			ProjectID:       f.project,
			Name:            fmt.Sprintf("discovery-%d", i),
			IntegrationKind: integrationdefinition.SlackThread,
		})
		require.NoError(t, err)
		setup.IntegrationID, setup.ExpectedSetupRevision = integration.ID, integration.SetupRevision
		setup.OAuthFlowID = uuid.Must(uuid.NewV7())
		_, err = f.store.ConfigureIntegration(f.ctx, setup)
		require.NoError(t, err)
		integrations = append(
			integrations,
			integrationstore.IntegrationInboxIntegration{ProjectID: f.project, IntegrationID: integration.ID},
		)
		fixture := f
		fixture.integrationID = integration.ID
		for n := range 101 {
			fixture.accept(t, fmt.Sprintf("receipt-%d", n))
		}
	}
	f.exec(
		t,
		`UPDATE integration_inbox SET next_attempt_at=now()+interval '1 day' WHERE integration_id=$1`,
		integrations[0].IntegrationID,
	)
	_, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
		ProjectID: f.project, IntegrationID: integrations[1].IntegrationID,
	})
	require.NoError(t, err)
	var after integrationstore.IntegrationInboxIntegration
	var ready []integrationstore.IntegrationInboxIntegration
	for i := range integrations {
		page, err := f.store.ListReadyIntegrationInboxIntegrations(f.ctx, after, 1)
		require.NoError(t, err)
		if i < len(integrations)-1 {
			require.Equal(t, integrations[i], page.NextCursor)
		} else {
			require.Zero(t, page.NextCursor)
		}
		ready = append(ready, page.Integrations...)
		after = page.NextCursor
	}
	require.Equal(
		t,
		integrations[2:],
		ready,
		"unavailable integrations and a hot backlog cannot hide the next integration",
	)
	page, err := f.store.ListReadyIntegrationInboxIntegrations(f.ctx, after, 100)
	require.NoError(t, err)
	require.Equal(
		t,
		integrations[2:],
		page.Integrations,
		"the next round must revisit ready integrations without draining either backlog",
	)
	require.Zero(t, page.NextCursor)
}

func TestInboxRecoveryMakesProgressThroughMixedBacklogs(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	disabled := f.addIntegration(t, "inactive-backlog", integrationstore.IntegrationSettings(`{}`)).ID
	applied, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
		ProjectID: f.project, IntegrationID: disabled,
	})
	require.NoError(t, err)
	require.True(t, applied)
	f.exec(t, `INSERT INTO integration_inbox(project_id,integration_id,receipt_key,payload,next_attempt_at)
 SELECT $1,$2,'inactive:'||n,'x'::bytea,statement_timestamp()-interval '2 hours'
 FROM generate_series(1,250) n`, f.project, disabled)
	f.exec(t, `INSERT INTO integration_inbox
 (project_id,integration_id,receipt_key,payload,state,attempt_count,claim_token,claim_expires_at)
 SELECT $1,$2,'expired:'||n,'x'::bytea,'processing',1,uuidv7(),statement_timestamp()-interval '1 hour'
 FROM generate_series(1,150) n`, f.project, f.integrationID)
	ready, err := f.store.ListReadyIntegrationInboxIntegrations(f.ctx, integrationstore.IntegrationInboxIntegration{}, 100)
	require.NoError(t, err)
	require.Empty(t, ready.Integrations, "expired receipts need recovery before becoming eligible")
	count, err := f.store.RecoverIntegrationInbox(f.ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 200, count, "both backlogs get an independent allowance")
	var expiredLeft, inactiveLeft int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER (WHERE state='processing'),
 count(*) FILTER (WHERE integration_id=$1 AND state='queued') FROM integration_inbox`, disabled).
		Scan(&expiredLeft, &inactiveLeft))
	require.Equal(t, 50, expiredLeft, "inactive backlog cannot starve expired leases")
	require.Equal(t, 150, inactiveLeft, "expired backlog cannot starve the inactive frontier")
	count, err = f.store.RecoverIntegrationInbox(f.ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 150, count)
	count, err = f.store.RecoverIntegrationInbox(f.ctx, 100)
	require.NoError(t, err)
	require.EqualValues(t, 50, count)
	ready, err = f.store.ListReadyIntegrationInboxIntegrations(f.ctx, integrationstore.IntegrationInboxIntegration{}, 100)
	require.NoError(t, err)
	require.Equal(
		t,
		[]integrationstore.IntegrationInboxIntegration{{ProjectID: f.project, IntegrationID: f.integrationID}},
		ready.Integrations,
	)
	claimed := f.claim(t)
	require.Equal(t, 2, claimed.AttemptCount, "recovered work resumes its original attempt budget")
}

func TestInboxOldestReadyLagUsesAvailabilityAndDistinguishesFailure(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	f.exec(t, `INSERT INTO integration_inbox
 (project_id,integration_id,receipt_key,payload,created_at,next_attempt_at,state,completed_at)
 VALUES ($1,$2,'future','x'::bytea,now()-interval '3 days',now()+interval '1 hour','queued',NULL),
        ($1,$2,'history','x'::bytea,now()-interval '4 days',now()-interval '4 days','completed',now())`,
		f.project, f.integrationID)
	lag, err := f.store.OldestReadyIntegrationInboxLag(f.ctx)
	require.NoError(t, err)
	require.Zero(t, lag, "future retries and terminal history are not ready work")
	f.exec(t, `INSERT INTO integration_inbox(project_id,integration_id,receipt_key,payload,created_at,next_attempt_at)
 VALUES ($1,$2,'ready','x'::bytea,now()-interval '2 days',now()-interval '90 seconds')`, f.project, f.integrationID)
	applied, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
		ProjectID: f.project, IntegrationID: f.integrationID,
	})
	require.NoError(t, err)
	require.True(t, applied)
	lag, err = f.store.OldestReadyIntegrationInboxLag(f.ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, lag, 90*time.Second, "inactive ready receipts remain visible until recovery")
	require.Less(t, lag, 2*time.Minute, "lag measures next_attempt_at, not original receipt age")
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err = f.store.OldestReadyIntegrationInboxLag(ctx)
	require.Error(t, err, "database failure must not look like an empty queue")
}
