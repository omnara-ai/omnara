//go:build integration

package executionstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type runtimeParentContentionFixture struct {
	store        *Store
	parent       executionstore.AgentRecord
	child        executionstore.AgentRecord
	lock         executionstore.AgentRuntimeLockRecord
	claimInput   executionstore.ClaimNormalModelCallInput
	firstInputAt int64
}

func newRuntimeParentContentionFixture(t *testing.T, ctx context.Context) runtimeParentContentionFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "runtime-parent@example.com", "Runtime Parent")

	// Use a real cluster provider so closing admission reaches the terminal
	// publisher through the same claims used by the runtime.
	tx := integrationdb.BeginTx(t, ctx, pool)
	credential, _, err := store.Secrets().CreateTx(ctx, tx, secretstore.CreateSecretInput{
		OrgID: testOrgID, ManagementKind: management.Cluster,
		OwnerKind: secretstore.SecretOwnerOrg, Name: "runtime-parent-credential",
		Material: secrets.GenericMaterial{Value: "test-key"}, Actor: userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create managed credential: %v", err)
	}
	if err := store.Models().ProvisionDefaultTx(ctx, tx, testOrgID, testProjectID, user.ID, credential.ID,
		modelstore.DefaultModelProviderTemplate{
			Provisioner: "runtime-parent-test", Name: "managed-prod", CredentialSecretName: credential.Name,
			APIFormat: modelprotocol.APIFormatOpenAIResponses, BaseURL: "https://api.openai.com/v1",
			AuthKind: modelstore.ModelProviderAuthKindBearerToken,
			Models: []modelstore.DefaultConfiguredModelTemplate{{
				Name: "managed-model", ProviderModelSlug: "managed-model",
				ContextWindowTokens: 128000, MaxOutputTokens: new(8192),
			}},
		}); err != nil {
		t.Fatalf("provision managed model: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit managed provider: %v", err)
	}
	config := mustCreateAgentConfigFromYAML(t, ctx, store,
		strings.ReplaceAll(
			strings.ReplaceAll(subagentParentYAML, "openai-prod", "managed-prod"), "gpt-test", "managed-model",
		))
	parent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: testProjectID, CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	launch, err := spawnSubagentForTest(t, ctx, store, parent, config.ID, "child", "runtime-parent-child", nil,
		withoutLaunchMessage)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	child := launch.Agent
	// UUIDv7 creation order makes archival lock the parent first, exposing the
	// old child -> runtime -> parent inversion without depending on random IDs.
	if bytes.Compare(parent.ID[:], child.ID[:]) >= 0 {
		t.Fatalf("parent %s must sort before child %s", parent.ID, child.ID)
	}
	lock, err := store.Execution().AcquireAgentRuntimeLock(ctx, testProjectID, child.ID,
		testWorkerProcessID, 5*time.Minute)
	if err != nil {
		t.Fatalf("acquire child runtime: %v", err)
	}
	for index := range 2 {
		_, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID, AgentID: child.ID, Actor: mustOmnaraActorParams(t, user.ID),
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"opening input"}]`),
			DeliveryMode:  executionstore.DeliveryModeSteering, IdempotencyKey: fmt.Sprintf("opening-%d", index),
		})
		if err != nil {
			t.Fatalf("create opening input: %v", err)
		}
	}
	admitted, found := admitNextAgentInputAndOpenTurnForTest(t, ctx, store, testProjectID, child.ID, lock.ID)
	if !found || len(admitted.Inputs) != 2 || len(admitted.Events) != 2 {
		t.Fatalf("admit opening inputs: found=%v admitted=%+v", found, admitted)
	}
	return runtimeParentContentionFixture{
		store: store, parent: parent, child: child, lock: lock, firstInputAt: admitted.Events[0].Sequence,
		claimInput: executionstore.ClaimNormalModelCallInput{
			ProjectID: testProjectID, AgentID: child.ID, RuntimeLockID: lock.ID, AgentConfigID: config.ID,
			OpeningInputIDs:    []uuid.UUID{admitted.Inputs[0].ID, admitted.Inputs[1].ID},
			InputEventSequence: admitted.Events[1].Sequence,
		},
	}
}

func (f runtimeParentContentionFixture) claimNormal(t *testing.T, ctx context.Context) executionstore.ModelCallClaim {
	t.Helper()
	claim, err := f.store.Execution().ClaimNormalModelCall(ctx, f.claimInput)
	if err != nil || !claim.Claimed {
		t.Fatalf("claim normal context: claim=%+v err=%v", claim, err)
	}
	return claim
}

func (f runtimeParentContentionFixture) compactionHandoffInput(
	contextID uuid.UUID,
) executionstore.RecordModelCallFailureAndClaimCompactionInput {
	return executionstore.RecordModelCallFailureAndClaimCompactionInput{
		ParentContextID: contextID, SourceEventSequenceEnd: f.claimInput.InputEventSequence,
		Failure: executionstore.RecordRecoverableModelCallFailureInput{
			ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID, ModelCallContextID: contextID,
			RecoveryKind: executionstore.ModelCallRecoveryCompact,
			ErrorKind:    "context_window", ErrorCode: "context_window", ErrorMessage: "model context exceeded",
		},
	}
}

// Each operation is called exactly once. Archive also bypasses its production
// retry wrapper, so neither side can hide a PostgreSQL deadlock by retrying.
func TestParentNotifyingRuntimeMutationsContendWithArchive(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"initial_admission", "retry_admission", "terminal_compaction_failure",
		"compaction_handoff_admission", "compaction_replacement_admission", "direct_failure", "completion",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			f := newRuntimeParentContentionFixture(t, ctx)
			var normal executionstore.ModelCallClaim
			if name != "initial_admission" {
				normal = f.claimNormal(t, ctx)
			}
			var compaction executionstore.ModelCallClaim
			if name == "terminal_compaction_failure" || name == "compaction_replacement_admission" {
				handoff, err := f.store.Execution().RecordModelCallFailureAndClaimCompaction(
					ctx, f.compactionHandoffInput(normal.Context.ID),
				)
				if err != nil || !handoff.CompactionCall.Claimed {
					t.Fatalf("prepare compaction: handoff=%+v err=%v", handoff, err)
				}
				compaction = handoff.CompactionCall
			}
			if name == "retry_admission" {
				_, err := f.store.Execution().RecordRetryableModelCallFailure(ctx,
					executionstore.RecordRecoverableModelCallFailureInput{
						ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID,
						ModelCallContextID: normal.Context.ID, ErrorKind: "transient",
						ErrorCode: "provider_unavailable", ErrorMessage: "provider unavailable", RetryDelay: 0,
					})
				if err != nil {
					t.Fatalf("prepare retry: %v", err)
				}
			}
			wantErrorCode := "terminal_test_failure"
			if strings.HasSuffix(name, "admission") {
				setManagedWorkAdmissionForTest(t, ctx, f.store.pool, testOrgID, false)
				wantErrorCode = storeerr.ManagedWorkAdmissionDeniedCode
			}
			var completedContextID uuid.UUID
			operation := func() error {
				var claim executionstore.ModelCallClaim
				var err error
				switch name {
				case "initial_admission":
					claim, err = f.store.Execution().ClaimNormalModelCall(ctx, f.claimInput)
				case "retry_admission":
					claim, err = f.store.Execution().ClaimNextModelCallContext(ctx, executionstore.ClaimNextModelCallContextInput{
						ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID,
						PredecessorModelCallContextID: normal.Context.ID,
					})
				case "compaction_handoff_admission":
					var handoff executionstore.TriggeredCompactionHandoff
					handoff, err = f.store.Execution().RecordModelCallFailureAndClaimCompaction(
						ctx, f.compactionHandoffInput(normal.Context.ID),
					)
					claim = handoff.CompactionCall
				case "compaction_replacement_admission":
					var replacement executionstore.ReplaceCompactionSourceResult
					replacement, err = f.store.Execution().ReplaceCompactionSource(ctx, executionstore.ReplaceCompactionSourceInput{
						ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID,
						ModelCallContextID: compaction.Context.ID, NextSourceEventSequenceEnd: f.firstInputAt,
						ErrorKind: "context_window", ErrorCode: "candidate_projection_over_budget",
						ErrorMessage: "candidate checkpoint did not fit",
					})
					claim = replacement.CompactionCall
				case "terminal_compaction_failure":
					completedContextID = compaction.Context.ID
					return f.store.Execution().RecordTerminalCompactionFailure(ctx,
						executionstore.RecordTerminalCompactionFailureInput{
							ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID,
							ModelCallContextID: compaction.Context.ID, ErrorKind: modelprotocol.ErrorKindRuntime,
							ErrorCode: wantErrorCode, ErrorMessage: "terminal compaction failure",
						})
				case "direct_failure":
					completedContextID = normal.Context.ID
					_, err = f.store.Execution().RecordModelCallErrorAndCompleteContext(ctx,
						executionstore.RecordModelCallErrorAndCompleteContextInput{
							ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID,
							ModelCallContextID: normal.Context.ID, ErrorKind: modelprotocol.ErrorKindRuntime,
							ErrorCode: wantErrorCode, ErrorMessage: "terminal model failure",
						})
					return err
				case "completion":
					completedContextID = normal.Context.ID
					_, err = f.store.Execution().RecordModelOutputAndCompleteContext(ctx,
						executionstore.RecordModelOutputAndCompleteContextInput{
							ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID, ModelCallContextID: normal.Context.ID,
							ProviderResponse: modelenvelope.ResponseEnvelope{
								RequestedProviderModelSlug: "managed-model", ServedProviderModelSlug: "managed-model",
								APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
								Normalized: modelenvelope.ResponseNormalized{
									ID: "resp_runtime_parent", StopReason: modelenvelope.StopReasonEndTurn,
								},
							},
						})
					return err
				}
				if err != nil {
					return err
				}
				if !claim.Created || claim.Claimed ||
					claim.Context.State != executionstore.ModelCallContextFailed || claim.Context.ErrorCode != wantErrorCode {
					return fmt.Errorf("expected a terminal admission denial, got %+v", claim)
				}
				completedContextID = claim.Context.ID
				return nil
			}

			control := integrationdb.BeginTx(t, ctx, f.store.pool)
			if _, err := dbsqlc.New(control).LockAgentRuntimeLockForOwnedMutation(ctx,
				dbsqlc.LockAgentRuntimeLockForOwnedMutationParams{
					ProjectID: testProjectID, AgentID: f.child.ID, ID: f.lock.ID,
				}); err != nil {
				t.Fatalf("hold runtime barrier: %v", err)
			}
			mutationDone := integrationdb.RunAsyncError(operation)
			integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.store.pool,
				"-- name: LockAgentRuntimeLockForOwnedMutation ", int32(control.Conn().PgConn().PID()))
			archiveDone := integrationdb.RunAsyncError(func() error {
				_, _, err := f.store.Execution().IntegrationArchiveAgentOnce(ctx, testOrgID, testProjectID, f.child.ID, nil)
				return err
			})
			integrationdb.WaitForNamedLockWaitersBlockedByChain(t, ctx, f.store.pool,
				"LockAgentInProject", int32(control.Conn().PgConn().PID()), 1)
			if err := control.Commit(ctx); err != nil {
				t.Fatalf("release runtime barrier: %v", err)
			}
			if err := integrationdb.Await(t, mutationDone, "runtime mutation"); err != nil {
				t.Fatalf("runtime mutation: %v", err)
			}
			if err := integrationdb.Await(t, archiveDone, "child archive"); err != nil {
				t.Fatalf("archive child: %v", err)
			}

			contextRow, found, err := f.store.Execution().GetModelCallContext(ctx, testProjectID, f.child.ID, completedContextID)
			if err != nil || !found {
				t.Fatalf("load completed context: found=%v err=%v", found, err)
			}
			wantState, wantMessage := executionstore.ModelCallContextFailed, "failed"
			if name == "completion" {
				wantState = executionstore.ModelCallContextSucceeded
				wantMessage, wantErrorCode = executionstore.SubagentMessageKindResult, ""
			}
			if contextRow.State != wantState || contextRow.ErrorCode != wantErrorCode || contextRow.RecoveryKind != "" {
				t.Fatalf("completed context = %+v", contextRow)
			}
			child, err := f.store.Execution().GetAgentInProject(ctx, testProjectID, f.child.ID)
			if err != nil || child.State != executionstore.AgentStateArchived {
				t.Fatalf("archived child: state=%s err=%v", child.State, err)
			}
			childPublicID, err := publicid.Encode(publicid.KindAgent, f.child.ID)
			if err != nil {
				t.Fatalf("encode child ID: %v", err)
			}
			var terminalInputs, archivedInputs, outputs int
			if err := f.store.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE metadata->'subagent_message'->>'kind' = $3),
       count(*) FILTER (WHERE metadata->'subagent_message'->>'kind' = 'archived')
FROM agent_inputs
WHERE agent_id = $1 AND metadata->'subagent_message'->>'agent_id' = $2
`, f.parent.ID, childPublicID, wantMessage).Scan(&terminalInputs, &archivedInputs); err != nil {
				t.Fatalf("count parent inputs: %v", err)
			}
			if err := f.store.pool.QueryRow(ctx,
				`SELECT count(*) FROM model_outputs WHERE agent_id = $1 AND model_call_context_id = $2`,
				f.child.ID, completedContextID).Scan(&outputs); err != nil {
				t.Fatalf("count terminal outputs: %v", err)
			}
			if terminalInputs != 1 || archivedInputs != 1 || outputs != 1 {
				t.Fatalf("terminal parent inputs/archive parent inputs/model outputs = %d/%d/%d, want 1/1/1",
					terminalInputs, archivedInputs, outputs)
			}
		})
	}
}

func TestParentNotifyingRuntimeMutationRechecksOwnershipAfterWaiting(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := newRuntimeParentContentionFixture(t, ctx)
	claim := f.claimNormal(t, ctx)
	control := integrationdb.BeginTx(t, ctx, f.store.pool)
	q := dbsqlc.New(control)
	if _, err := q.LockAgentRuntimeLockForOwnedMutation(ctx, dbsqlc.LockAgentRuntimeLockForOwnedMutationParams{
		ProjectID: testProjectID, AgentID: f.child.ID, ID: f.lock.ID,
	}); err != nil {
		t.Fatalf("hold runtime barrier: %v", err)
	}
	done := integrationdb.RunAsyncError(func() error {
		_, err := f.store.Execution().RecordModelCallErrorAndCompleteContext(ctx,
			executionstore.RecordModelCallErrorAndCompleteContextInput{
				ProjectID: testProjectID, AgentID: f.child.ID, RuntimeLockID: f.lock.ID,
				ModelCallContextID: claim.Context.ID, ErrorKind: modelprotocol.ErrorKindRuntime,
				ErrorCode: "terminal_test_failure", ErrorMessage: "terminal model failure",
			})
		return err
	})
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, f.store.pool,
		"-- name: LockAgentRuntimeLockForOwnedMutation ", int32(control.Conn().PgConn().PID()))
	if _, err := q.RequestAgentRuntimeCancel(ctx, dbsqlc.RequestAgentRuntimeCancelParams{
		ProjectID: testProjectID, AgentID: f.child.ID,
	}); err != nil {
		t.Fatalf("cancel ownership while mutation waits: %v", err)
	}
	if err := control.Commit(ctx); err != nil {
		t.Fatalf("release canceled runtime: %v", err)
	}
	if err := integrationdb.Await(t, done, "canceled runtime mutation"); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("mutation after cancellation = %v, want runtime inactive", err)
	}
	contextRow, found, err := f.store.Execution().GetModelCallContext(ctx, testProjectID, f.child.ID, claim.Context.ID)
	if err != nil || !found || contextRow.State != executionstore.ModelCallContextStarted {
		t.Fatalf("canceled mutation changed context: context=%+v found=%v err=%v", contextRow, found, err)
	}
	var outputs, parentInputs int
	if err := f.store.pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM model_outputs WHERE agent_id = $1),
       (SELECT count(*) FROM agent_inputs WHERE agent_id = $2
        AND metadata->'subagent_message'->>'kind' = 'failed')
`, f.child.ID, f.parent.ID).Scan(&outputs, &parentInputs); err != nil {
		t.Fatalf("count canceled mutation effects: %v", err)
	}
	if outputs != 0 || parentInputs != 0 {
		t.Fatalf("canceled mutation outputs/parent inputs = %d/%d, want 0/0", outputs, parentInputs)
	}
}
