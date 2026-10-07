//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func integrationWorkerFixture(t *testing.T) (*pgxpool.Pool, *storage.Store, storagefixture.ProjectIDs, uuid.UUID) {
	t.Helper()
	return integrationProviderFixture(t, "slack", "T123", "A123")
}

func integrationProviderFixture(
	t *testing.T, provider integrationdefinition.Provider, tenant, account string,
) (*pgxpool.Pool, *storage.Store, storagefixture.ProjectIDs, uuid.UUID) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	pool := integrationdb.OpenMigratedPool(t, t.Context(), filepath.Join(filepath.Dir(file), "../../migrations"))
	ids := storagefixture.ProjectIDs{
		OrgID:                   uuid.New(),
		ProjectID:               uuid.New(),
		ProviderAdminUserID:     uuid.New(),
		ProviderSecretID:        uuid.New(),
		ProviderSecretVersionID: uuid.New(),
		ProviderConfigID:        uuid.New(),
	}
	storagefixture.SeedProject(t, t.Context(), pool, ids, time.Now())
	wrapper, err := secrets.NewLocalKeyWrapper("inbox-provider-test", map[string][]byte{
		"inbox-provider-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(wrapper))
	_, err = store.Identity().AddOrgMembership(t.Context(), identitystore.AddOrgMembershipInput{
		OrgID: ids.OrgID, UserID: ids.ProviderAdminUserID, Role: "owner",
	})
	require.NoError(t, err)
	integrationKinds := integrationdefinition.IntegrationKindsForProvider(provider)
	require.Len(t, integrationKinds, 1, "fixture requires an explicit registered type for this transport")
	integration, err := store.Integrations().CreateIntegration(t.Context(), integrationstore.SaveIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat",
		IntegrationKind: integrationdefinition.Kind(integrationKinds[0]),
	})
	require.NoError(t, err)
	setup := integrationstore.ConfigureIntegrationInput{
		OrgID: ids.OrgID, ProjectID: ids.ProjectID, IntegrationID: integration.ID,
		InstalledByUserID: ids.ProviderAdminUserID, Provider: provider,
		ProviderTenantID: tenant, ProviderAccountRef: account, ExpectedSetupRevision: integration.SetupRevision,
	}
	var material secrets.Material
	switch provider {
	case integrationdefinition.ProviderSlack:
		material = secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-inbox-test", ClientID: "client", ClientSecret: "client-secret", SigningSecret: "signing-secret",
		}
		setup.OAuthFlowID = uuid.Must(uuid.NewV7())
		setup.ProviderIdentity = json.RawMessage(`{"bot_user_id":"UBOT"}`)
	case integrationdefinition.ProviderDiscord:
		material = secrets.GenericMaterial{Value: "discord-inbox-test-token"}
	case integrationdefinition.ProviderGitHub:
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		material = secrets.GitHubAppCredentialsMaterial{
			AppID: tenant, WebhookSecret: "github-inbox-test-secret",
			PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		}
		setup.CredentialAppID, err = strconv.ParseInt(tenant, 10, 64)
		require.NoError(t, err)
	default:
		t.Fatalf("unsupported inbox fixture provider %q", provider)
	}
	credential, version, err := store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
		OrgID: ids.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: ids.ProjectID,
		Name: "inbox-credentials", Actor: identitystore.NewUserPrincipal(ids.ProviderAdminUserID), Material: material,
	})
	require.NoError(t, err)
	setup.CredentialSecretID, setup.CredentialVersionID = credential.ID, version.ID
	integration, err = store.Integrations().ConfigureIntegration(t.Context(), setup)
	require.NoError(t, err)
	return pool, store, ids, integration.ID
}

type integrationWorkerFailureConsumer struct {
	integrationWorkerConsumerFunc
	finalize func(context.Context, uuid.UUID, uuid.UUID) error
}

func (c integrationWorkerFailureConsumer) FinalizeFailure(
	ctx context.Context, project, receipt uuid.UUID, _ error,
) error {
	return c.finalize(ctx, project, receipt)
}

func TestIntegrationInboxWorkerDurablePartialRetryAndExhaustion(t *testing.T) {
	pool, store, ids, integrationSetup := integrationWorkerFixture(t)
	inbox := store.Integrations()
	ctx := t.Context()
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	integration, err := inbox.GetIntegration(ctx, ids.ProjectID, integrationSetup)
	require.NoError(t, err)
	var agents []uuid.UUID
	for range 2 {
		launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID: ids.ProjectID, AgentConfigID: base.ID,
			LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
		})
		require.NoError(t, err)
		agents = append(agents, launched.Agent.ID)
		createTestIntegrationSubscription(t, store, integration, launched.Agent.ID, `{"channel_id":"C123"}`)
	}
	router := NewIntegrationRouter(store.Execution(), inbox)
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: integrationdefinition.EventMessage,
			Scope: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}}},
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"review"}]`),
		Actor:         integrationTestActor(t, integration, "U123"),
	}
	receipt, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "partial",
			Payload:       []byte(`{"event":"test"}`),
		},
	)
	require.NoError(t, err)
	transient := &discord.APIError{Code: discord.RateLimited, RetryAfter: time.Hour}
	var admissions []executionstore.InboxInputResult
	consumer := integrationWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationRecipientAdmission, error) {
			event.SemanticKey = lease.ReceiptID.String()
			plan, err := freezeTestIntegrationEvent(ctx, router, lease, &event)
			if err != nil {
				return nil, err
			}
			for key, recipient := range plan.Recipients {
				if recipient.AgentID == agents[0] {
					result, err := store.Execution().AdmitInboxInputRecipient(ctx, lease, key, nil)
					if err != nil {
						return nil, err
					}
					admissions = append(admissions, result)
					return []IntegrationRecipientAdmission{{Recipient: key, Input: &result}}, transient
				}
			}
			return nil, errors.New("fixture did not route the first recipient")
		},
	)
	finalizations := 0
	metricSet := metrics.New()
	assertOutcome := func(outcome string, count int) {
		t.Helper()
		response := httptest.NewRecorder()
		metricSet.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil))
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), fmt.Sprintf(
			`omnara_integration_inbox_processing_total{outcome="%s"} %d`, outcome, count))
	}
	worker := NewIntegrationInboxWorker(inbox, integrationWorkerFailureConsumer{integrationWorkerConsumerFunc: consumer,
		finalize: func(ctx context.Context, project, id uuid.UUID) error {
			stored, err := inbox.GetIntegrationInbox(ctx, project, id)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxFailed, stored.State)
			finalizations++
			return errors.New("notification unavailable")
		},
	}, IntegrationInboxWorkerOptions{Metrics: metrics.NewIntegrationInboxRecorder(metricSet)})
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	first, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxQueued, first.State)
	assertOutcome("retry_scheduled", 1)
	require.Contains(t, first.LastError, transient.Error())
	require.Len(t, admissions, 1)
	require.True(t, admissions[0].Created)
	outcomes, err := store.Execution().GetIntegrationInboxOutcomes(ctx, first)
	require.NoError(t, err)
	var plan IntegrationInboxPlan
	require.NoError(t, json.Unmarshal(first.Plan, &plan))
	require.Len(t, plan.Recipients, 2)
	for key, recipient := range plan.Recipients {
		if recipient.AgentID == agents[0] {
			require.Equal(t, executionstore.InboxRecipientDelivered, outcomes[key])
		} else {
			require.Equal(t, executionstore.InboxRecipientPending, outcomes[key])
		}
	}
	require.Zero(t, finalizations, "no failure notice during retry")
	require.WithinDuration(t, time.Now().Add(time.Hour), first.NextAttemptAt, 5*time.Second)
	_, err = pool.Exec(ctx, `UPDATE integration_inbox SET attempt_count=7,next_attempt_at=now() WHERE id=$1`, receipt.ID)
	require.NoError(t, err)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	failed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxFailed, failed.State)
	assertOutcome("failed", 1)
	require.Equal(t, 8, failed.AttemptCount)
	require.JSONEq(t, string(first.Plan), string(failed.Plan))
	require.Len(t, admissions, 2)
	require.False(t, admissions[1].Created)
	require.Equal(t, admissions[0].AgentInput.ID, admissions[1].AgentInput.ID)
	failedOutcomes, err := store.Execution().GetIntegrationInboxOutcomes(ctx, failed)
	require.NoError(t, err)
	require.Equal(t, outcomes, failedOutcomes, "exhaustion preserves the delivered input and pending sibling")
	require.Contains(t, failed.LastError, transient.Error())
	require.Equal(t, 1, finalizations)
	require.NotNil(t, failed.CompletedAt)
	duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationSetup, ReceiptKey: "partial", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, receipt.ID, duplicate.ID)
	worked, err = worker.RunOnce(ctx)
	require.False(t, worked)
	require.NoError(t, err)
	fresh, created, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationSetup, ReceiptKey: "fresh", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, receipt.ID, fresh.ID)
	worked, err = worker.RunOnce(ctx)
	require.True(t, worked)
	require.ErrorIs(t, err, transient)
	fresh, err = inbox.GetIntegrationInbox(ctx, ids.ProjectID, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, 1, fresh.AttemptCount)
	require.Equal(t, integrationstore.IntegrationInboxQueued, fresh.State)
	assertOutcome("retry_scheduled", 2)
}

func TestIntegrationInboxWorkerRecoversExpiredLeaseBeforeDiscovery(t *testing.T) {
	pool, store, ids, integrationSetup := integrationWorkerFixture(t)
	inbox := store.Integrations()
	ctx := t.Context()
	_, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "expired",
			Payload:       []byte(`{}`),
		},
	)
	require.NoError(t, err)
	old, claimed, err := inbox.ClaimIntegrationInbox(
		ctx,
		integrationstore.ClaimIntegrationInboxInput{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			LeaseDuration: time.Minute,
		},
	)
	require.True(t, claimed)
	require.NoError(t, err)
	_, err = pool.Exec(
		ctx,
		`UPDATE integration_inbox SET claim_expires_at=now()-interval '1 second' WHERE id=$1`,
		old.ID,
	)
	require.NoError(t, err)
	worker := NewIntegrationInboxWorker(
		inbox,
		integrationWorkerConsumerFunc(
			func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationRecipientAdmission, error) {
				require.Equal(t, old.ID, lease.ReceiptID)
				require.NotEqual(t, old.ClaimToken, lease.Token)
				err := inbox.WithIntegrationInboxLease(
					ctx,
					old.Lease(),
					func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.Fail(ctx, "stale") },
				)
				require.ErrorIs(t, err, integrationstore.ErrIntegrationInboxLeaseLost)
				err = inbox.WithIntegrationInboxLease(ctx, lease,
					func(work *integrationstore.IntegrationInboxLeaseTx) error {
						return work.FreezePlan(ctx, json.RawMessage(`{"recipients":{}}`))
					})
				if err != nil {
					return nil, err
				}
				return nil, store.Execution().CompleteIntegrationInbox(ctx, lease)
			},
		),
		IntegrationInboxWorkerOptions{},
	)
	worked, err := worker.RunOnce(ctx)
	require.True(t, worked)
	require.NoError(t, err)
}

func TestIntegrationInboxWorkerSlowReceiptDoesNotBlockSameIntegration(t *testing.T) {
	_, store, ids, integrationSetup := integrationWorkerFixture(t)
	inbox := store.Integrations()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slow, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "slow-file",
			Payload:       []byte(`{}`),
		},
	)
	require.NoError(t, err)
	fast, _, err := inbox.AcceptIntegrationReceipt(
		ctx,
		integrationstore.VerifiedIntegrationReceipt{
			ProjectID:     ids.ProjectID,
			IntegrationID: integrationSetup,
			ReceiptKey:    "other-conversation",
			Payload:       []byte(`{}`),
		},
	)
	require.NoError(t, err)
	slowStarted := make(chan struct{})
	fastCompleted := make(chan error, 1)
	consumer := integrationWorkerConsumerFunc(
		func(ctx context.Context, lease integrationstore.IntegrationInboxLease) ([]IntegrationRecipientAdmission, error) {
			if lease.ReceiptID == slow.ID {
				close(slowStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			<-slowStarted
			err := inbox.WithIntegrationInboxLease(
				ctx,
				lease,
				func(work *integrationstore.IntegrationInboxLeaseTx) error {
					return work.FreezePlan(ctx, json.RawMessage(`{"recipients":{}}`))
				},
			)
			if err == nil {
				err = store.Execution().CompleteIntegrationInbox(ctx, lease)
			}
			fastCompleted <- err
			return nil, err
		},
	)
	worker := NewIntegrationInboxWorker(inbox, consumer, IntegrationInboxWorkerOptions{Capacity: 2})
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case err := <-fastCompleted:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("slow receipt blocked another conversation on the same integration")
	}
	completed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, fast.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
	busy, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, slow.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxProcessing, busy.State)
	cancel()
	require.NoError(t, <-done)
}

func seedIndependentIntegration(
	t *testing.T, pool *pgxpool.Pool, template integrationstore.IntegrationRecord, name string,
) integrationstore.IntegrationRecord {
	t.Helper()
	store := storage.NewStore(pool)
	credential, err := store.Secrets().GetSecret(t.Context(), template.OrgID, template.CredentialSecretID)
	require.NoError(t, err)
	integration, err := store.Integrations().CreateIntegration(t.Context(), integrationstore.SaveIntegrationInput{
		OrgID: template.OrgID, ProjectID: template.ProjectID, Name: name,
		IntegrationKind: template.IntegrationKind, Settings: template.Settings,
	})
	require.NoError(t, err)
	integration, err = store.Integrations().ConfigureIntegration(t.Context(), integrationstore.ConfigureIntegrationInput{
		OrgID: template.OrgID, ProjectID: template.ProjectID, IntegrationID: integration.ID,
		InstalledByUserID: template.InstalledByUserID, Provider: template.Provider,
		ProviderTenantID: template.ProviderTenantID, ProviderAccountRef: template.ProviderAccountRef,
		CredentialSecretID: credential.ID, CredentialVersionID: credential.CurrentVersionID,
		ExpectedSetupRevision: integration.SetupRevision, OAuthFlowID: uuid.Must(uuid.NewV7()),
		ProviderConfig: template.ProviderConfig, ProviderIdentity: template.ProviderIdentity,
		ProviderMetadata: template.ProviderMetadata, ProviderAgentDisplayName: template.ProviderAgentDisplayName,
	})
	require.NoError(t, err)
	return integration
}

func TestIntegrationInboxWorkerOnlyMarkedInboundFailuresAreTerminal(t *testing.T) {
	for _, test := range []struct {
		name     string
		cause    error
		terminal bool
	}{
		{"inaccessible channel", fmt.Errorf("expand: %w: %w", ErrIntegrationInboundPermanent,
			&discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusForbidden}), true},
		{"missing channel", fmt.Errorf("expand: %w: %w", ErrIntegrationInboundPermanent,
			&discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusNotFound}), true},
		{"out of credits", fmt.Errorf("launch: %w", storeerr.ErrManagedWorkAdmissionDenied), true},
		{"revoked stored authority", fmt.Errorf("recipient: %w", storeerr.ErrUnauthorized), false},
		{"unmarked invalid request", storeerr.InvalidRequest(errors.New("admission rejected")), false},
		{"unmarked channel forbidden", &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusForbidden,
		}, false},
		{"unmarked channel missing", &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusNotFound,
		}, false},
		{"provider credential rejected", &discord.APIError{
			Code: discord.PermanentFailure, StatusCode: http.StatusUnauthorized,
		}, false},
		{"rate limited", &discord.APIError{Code: discord.RateLimited, RetryAfter: time.Minute}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
			ctx := t.Context()
			inbox := store.Integrations()
			input := integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "disposition", Payload: []byte(`{}`),
			}
			receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, input)
			require.NoError(t, err)
			attempts, finalized := 0, 0
			consumer := integrationWorkerFailureConsumer{
				integrationWorkerConsumerFunc: func(
					context.Context,
					integrationstore.IntegrationInboxLease,
				) ([]IntegrationRecipientAdmission, error) {
					attempts++
					return nil, test.cause
				},
				finalize: func(ctx context.Context, projectID, receiptID uuid.UUID) error {
					current, err := inbox.GetIntegrationInbox(ctx, projectID, receiptID)
					require.NoError(t, err)
					require.Equal(t, integrationstore.IntegrationInboxFailed, current.State,
						"finalization must run after the terminal commit")
					require.NotNil(t, current.CompletedAt)
					finalized++
					return nil
				},
			}
			worker := NewIntegrationInboxWorker(inbox, consumer, IntegrationInboxWorkerOptions{})
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			require.ErrorIs(t, err, test.cause)
			current, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, 1, current.AttemptCount)
			require.Equal(t, 1, attempts)
			if test.terminal {
				require.Equal(t, integrationstore.IntegrationInboxFailed, current.State)
				require.Equal(t, 1, finalized)
				duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, input)
				require.NoError(t, err)
				require.False(t, created)
				require.Equal(t, receipt.ID, duplicate.ID)
				worked, err = worker.RunOnce(ctx)
				require.NoError(t, err)
				require.False(t, worked)
				require.Equal(t, 1, finalized, "duplicate intake must not finalize again")
				require.Equal(t, 1, attempts, "terminal input must not retry")
			} else {
				require.Equal(t, integrationstore.IntegrationInboxQueued, current.State)
				require.Nil(t, current.CompletedAt)
				require.True(t, current.NextAttemptAt.After(time.Now()))
				require.Zero(t, finalized)
			}
		})
	}
}
