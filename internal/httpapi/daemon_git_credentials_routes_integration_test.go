//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

type gitCredentialsTestTransport func(*http.Request) (*http.Response, error)

func (f gitCredentialsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type daemonGitCredentialsFixture struct {
	githubSetupJourney
	disabled, process  daemonProcessFixture
	base, enabled      executionstore.AgentConfigRecord
	minted             atomic.Int32
	duringIssuance     func()
	contentsPermission string
	providerStatus     int
}

func newDaemonGitCredentialsFixture(t *testing.T) *daemonGitCredentialsFixture {
	t.Helper()
	f := &daemonGitCredentialsFixture{contentsPermission: "read"}
	provider := githubSetupTestConfig(t)
	transport := provider.HTTPClient.Transport
	provider.HTTPClient.Transport = gitCredentialsTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodDelete && r.URL.Path == "/installation/token" &&
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer git-installation-token-") {
			return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{},
				Body: http.NoBody, Request: r}, nil
		}
		if strings.HasSuffix(r.URL.Path, "/access_tokens") && r.ContentLength == 0 {
			count := f.minted.Add(1)
			if f.providerStatus != 0 {
				return &http.Response{
					StatusCode: f.providerStatus, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"message":"git-installation-token PRIVATE KEY"}`)), Request: r,
				}, nil
			}
			if f.duringIssuance != nil {
				f.duringIssuance()
			}
			body := fmt.Sprintf(`{"token":"git-installation-token-%d","expires_at":%q,"permissions":{"contents":%q}}`,
				count, time.Now().Add(time.Hour).UTC().Format(time.RFC3339), f.contentsPermission)
			return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		return transport.RoundTrip(r)
	})
	f.githubSetupJourney = newGitHubSetupJourney(t, "daemon-git", WithGitHubClientConfig(provider))
	f.disabled = createDaemonProcessFixture(t, t.Context(), integrationPoolForHandler(t, f.handler),
		f.project.Store, f.project, time.Now(), "git-disabled", "run_command")
	agent, err := f.project.Store.Execution().GetAgentInProject(t.Context(), f.project.ProjectUUID, f.disabled.AgentUUID)
	require.NoError(t, err)
	var found bool
	f.base, found, err = f.project.Store.Execution().GetAgentConfig(
		t.Context(), f.project.ProjectUUID, agent.CurrentConfigID,
	)
	require.NoError(t, err)
	require.True(t, found)
	f.enabled = f.config(t, &agentconfig.GitCredentialsCompiled{
		Integration: f.integration.Name, IntegrationID: f.integration.ID,
	}, "")
	f.process = f.launchProcess(t, f.enabled)
	return f
}

func (f *daemonGitCredentialsFixture) config(
	t *testing.T, credentials *agentconfig.GitCredentialsCompiled, instructionSuffix string,
) executionstore.AgentConfigRecord {
	t.Helper()
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(f.base.CompiledDefinition, &compiled))
	compiled.GitCredentials = credentials
	compiled.Instruction += instructionSuffix
	encoded, err := agentconfig.EncodeCompiled(compiled)
	require.NoError(t, err)
	config, err := f.project.Store.Execution().CreateAgentConfig(t.Context(), executionstore.CreateAgentConfigInput{
		ProjectID: f.project.ProjectUUID, ConfiguredModelID: f.base.ConfiguredModelID,
		CompiledDefinition: encoded.CanonicalJSON, EffectiveDefinitionHash: encoded.Hash,
	})
	require.NoError(t, err)
	return config
}

func (f *daemonGitCredentialsFixture) launchProcess(
	t *testing.T, config executionstore.AgentConfigRecord,
) daemonProcessFixture {
	t.Helper()
	ctx, store := t.Context(), f.project.Store
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: f.project.ProjectUUID, AgentConfigID: config.ID, LaunchedBy: httpUserPrincipal(f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	require.Len(t, launch.MachineBindings, 1)
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: f.project.ProjectUUID, AgentID: launch.Agent.ID,
		Actor:         httpOmnaraActorParams(t, f.project.OrgUUID, f.project.AdminUserUUID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"clone"}]`), IdempotencyKey: "git-input",
	})
	require.NoError(t, err)
	claim, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
	require.Equal(t, launch.Agent.ID, claim.RuntimeLock.AgentID)
	modelCall := claimNormalModelCallForHTTPTest(t, ctx, store, f.project.ProjectUUID, launch.Agent.ID,
		claim.RuntimeLock, []uuid.UUID{input.ID}, config.ID, claim.Model.AdmittedInputTurn.Events[0].Sequence)
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, f.project.ProjectUUID, modelCall.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "git-model-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{
				{ID: "git-command", Name: "run_command", Input: json.RawMessage(`{"command":"git clone"}`)},
			})})
	require.NoError(t, err)
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: f.project.ProjectUUID, AgentID: launch.Agent.ID, RuntimeLockID: claim.RuntimeLock.ID,
			ModelCallContextID: modelCall.Context.ID, ProviderResponse: response,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: "git-command", Type: toolcatalog.ToolTypeBuiltIn},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	call, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID: f.project.ProjectUUID, AgentID: launch.Agent.ID, ID: calls[0].ID, RuntimeLockID: claim.RuntimeLock.ID,
	})
	require.NoError(t, err)
	process, err := storagetest.StartProcessForToolCall(ctx, store, executionstore.ExecuteToolCallInput{
		ProjectID: f.project.ProjectUUID, AgentID: launch.Agent.ID, ToolCallID: call.ID, RuntimeLockID: claim.RuntimeLock.ID,
	}, executionstore.CreateProcessInput{AgentMachineBindingID: launch.MachineBindings[0].ID,
		Command: "git clone", ShellSelector: "sh"})
	require.NoError(t, err)
	fixture := f.disabled
	fixture.AgentUUID, fixture.BindingUUID = launch.Agent.ID, launch.MachineBindings[0].ID
	fixture.ProcessUUID, fixture.ProcessID = process.ID, testPublicID(t, publicid.KindProcess, process.ID)
	fixture.RuntimeLock, fixture.ToolCallUUID, fixture.ToolCall = claim.RuntimeLock, call.ID, call
	return fixture
}

func (f *daemonGitCredentialsFixture) accept(t *testing.T, process daemonProcessFixture) {
	t.Helper()
	_, found, err := acceptDaemonProcessOfferForTest(
		t.Context(), f.project.Store, process.authority(), process.ProcessUUID,
	)
	require.NoError(t, err)
	require.True(t, found)
}

func (f *daemonGitCredentialsFixture) request(
	t *testing.T, token, processID string, status int,
) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/daemon/processes/"+processID+"/git-credentials", nil)
	r = r.WithContext(t.Context())
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	require.Equal(t, status, w.Code, w.Body.String())
	if status != http.StatusOK {
		require.NotContains(t, w.Body.String(), "git-installation-token")
		require.NotContains(t, w.Body.String(), "PRIVATE KEY")
	}
	return w
}

func (f *daemonGitCredentialsFixture) changeConfig(
	t *testing.T, process daemonProcessFixture, config executionstore.AgentConfigRecord,
) {
	t.Helper()
	_, err := f.project.Store.Execution().ChangeAgentConfig(t.Context(), executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID: f.project.ProjectUUID, ConfiguredModelID: config.ConfiguredModelID,
			CompiledDefinition: config.CompiledDefinition, EffectiveDefinitionHash: config.EffectiveDefinitionHash,
		},
		AgentID: process.AgentUUID, ActorType: "user", ActorID: f.project.AdminUserUUID,
		Reason: "Git credential test", IdempotencyKey: uuid.NewString(),
	})
	require.NoError(t, err)
}

func TestDaemonGitCredentialsOriginalConfigOffersAndLiveAuthorization(t *testing.T) {
	t.Parallel()
	f := newDaemonGitCredentialsFixture(t)
	// Enabling credentials later must neither enable the old offer nor authorize the old shell.
	f.changeConfig(t, f.disabled, f.enabled)
	offers, err := f.project.Store.Execution().ListDaemonProcessOffers(t.Context(), executionstore.DaemonWorkInput{
		Authority: f.process.authority(), Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, offers, 2)
	for _, offer := range offers {
		want := offer.Process.ID == f.process.ProcessUUID
		require.Equal(t, want, offer.GitCredentials)
		require.Equal(t, want, daemonProcessOfferMessage("process", offer).ProcessOffer.GitCredentials)
	}
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusNotFound) // Queued, not execution-granted.
	f.accept(t, f.disabled)
	f.accept(t, f.process)
	f.request(t, f.disabled.Token, f.disabled.ProcessID, http.StatusNotFound)
	f.request(t, "", f.process.ProcessID, http.StatusUnauthorized)
	f.request(t, f.project.AdminToken, f.process.ProcessID, http.StatusForbidden)
	wrongMachine := createDaemonProcessFixture(t, t.Context(), integrationPoolForHandler(t, f.handler),
		f.project.Store, f.project, time.Now(), "git-wrong-machine", "run_command")
	f.request(t, wrongMachine.Token, f.process.ProcessID, http.StatusNotFound)
	otherProject := bootstrapPublicHTTPProject(t, f.handler, "git-other-org")
	otherOrg := createDaemonProcessFixture(t, t.Context(), integrationPoolForHandler(t, f.handler),
		f.project.Store, otherProject, time.Now(), "git-other-org-machine", "run_command")
	f.request(t, otherOrg.Token, f.process.ProcessID, http.StatusNotFound)

	first := f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	require.Equal(t, "no-store", first.Header().Get("Cache-Control"))
	var credential daemonprotocol.GitCredentials
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &credential))
	require.Equal(t, "git-installation-token-1", credential.Token)
	require.True(t, credential.ExpiresAt.After(time.Now().Add(45*time.Minute)))
	// A starting process can request credentials even after the original tool's runtime lease expires.
	pool := integrationPoolForHandler(t, f.handler)
	_, err = pool.Exec(t.Context(), `UPDATE agent_runtime_locks
 SET started_at=now()-interval '2 minutes', renewed_at=now()-interval '1 minute',
 lease_expires_at=now()-interval '1 second' WHERE id=$1`,
		f.process.RuntimeLock.ID)
	require.NoError(t, err)
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	require.EqualValues(t, 1, f.minted.Load(), "successful requests reuse installation credentials")
	_, err = pool.Exec(t.Context(), `UPDATE processes SET state='running',source_started_at=now() WHERE id=$1`,
		f.process.ProcessUUID)
	require.NoError(t, err)
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	f.changeConfig(t, f.process, f.base)
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusNotFound)
	require.EqualValues(t, 1, f.minted.Load(), "current config revokes a cached credential")
	other := githubHTTPJourneyIntegration(t, f.handler, f.project, f.secretID, "457")
	otherConfig := f.config(t, &agentconfig.GitCredentialsCompiled{
		Integration: other.Name, IntegrationID: other.ID,
	}, "")
	f.changeConfig(t, f.process, otherConfig)
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusNotFound)
	require.EqualValues(t, 1, f.minted.Load(), "both enabled configs must pin the same integration")
}

func TestDaemonGitCredentialsCachedTokenRequiresLiveExecution(t *testing.T) {
	t.Parallel()
	mutations := []struct {
		name, query string
	}{
		{"binding", `UPDATE agent_machine_bindings SET state='released' WHERE id=$1`},
		{"grant", `DELETE FROM project_machine_grants WHERE machine_id=$1`},
		{"agent", `UPDATE agents SET state='archived',archived_at=now() WHERE id=$1`},
		{"machine", `UPDATE machines SET deleted_at=now(),lifecycle_state='deleted' WHERE id=$1`},
		{"project", `UPDATE projects SET deleted_at=now() WHERE id=$1`},
		{"integration", `UPDATE integrations SET deleted_at=now() WHERE id=$1`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			f := newDaemonGitCredentialsFixture(t)
			f.accept(t, f.process)
			f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
			ids := map[string]uuid.UUID{
				"binding": f.process.BindingUUID, "grant": f.process.MachineUUID, "agent": f.process.AgentUUID,
				"machine": f.process.MachineUUID, "project": f.project.ProjectUUID, "integration": f.integration.ID,
			}
			_, err := integrationPoolForHandler(t, f.handler).Exec(t.Context(), mutation.query, ids[mutation.name])
			require.NoError(t, err)
			status := http.StatusNotFound
			if mutation.name == "machine" {
				status = http.StatusUnauthorized
			}
			f.request(t, f.process.Token, f.process.ProcessID, status)
			require.EqualValues(t, 1, f.minted.Load())
		})
	}
}

func (f *daemonGitCredentialsFixture) rotateCredential(t *testing.T) {
	t.Helper()
	credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(t.Context(),
		secretstore.ReadProjectAvailableSecretPayloadInput{OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
			SecretID: f.integration.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials})
	require.NoError(t, err)
	_, _, err = f.project.Store.Secrets().CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
		OrgID: f.project.OrgUUID, SecretID: f.integration.CredentialSecretID,
		Actor: httpUserPrincipal(f.project.AdminUserUUID),
		Material: secrets.GitHubAppCredentialsMaterial{AppID: credential.Payload[secrets.KeyAppID],
			PrivateKey: credential.Payload[secrets.KeyPrivateKey], WebhookSecret: uuid.NewString()},
	})
	require.NoError(t, err)
}

func TestDaemonGitCredentialsRotationCannotFallBackToCachedTokenWithoutContentsPermission(t *testing.T) {
	t.Parallel()
	f := newDaemonGitCredentialsFixture(t)
	f.accept(t, f.process)
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	require.EqualValues(t, 1, f.minted.Load())
	f.rotateCredential(t)
	f.contentsPermission = ""
	denied := f.request(t, f.process.Token, f.process.ProcessID, http.StatusConflict)
	require.Contains(t, denied.Body.String(), "Contents read or write permission")
	require.EqualValues(t, 2, f.minted.Load(), "the old version's cached credential cannot satisfy this request")
	f.contentsPermission = "read"
	recovered := f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	require.Contains(t, recovered.Body.String(), "git-installation-token-3")
	f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
	require.EqualValues(t, 3, f.minted.Load(), "permission failures are not cached; restored access is cached")
}

func TestDaemonGitCredentialsProviderFailureStatusAndRedaction(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name             string
		provider, status int
	}{
		{"invalid App credentials", http.StatusUnauthorized, http.StatusConflict},
		{"permission denied", http.StatusForbidden, http.StatusConflict},
		{"installation missing", http.StatusNotFound, http.StatusConflict},
		{"rate limited", http.StatusTooManyRequests, http.StatusServiceUnavailable},
		{"provider unavailable", http.StatusBadGateway, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newDaemonGitCredentialsFixture(t)
			f.accept(t, f.process)
			f.providerStatus = test.provider
			f.request(t, f.process.Token, f.process.ProcessID, test.status)
			require.EqualValues(t, 1, f.minted.Load(), "token issuance must not retry within the HTTP request")
		})
	}
}

func TestDaemonGitCredentialsRechecksAuthorityDuringIssuance(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{
		"disconnect", "rotation", "secret", "reconfigure", "config", "changed pin", "same integration new config", "closed process",
	} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			f := newDaemonGitCredentialsFixture(t)
			f.accept(t, f.process)
			f.duringIssuance = func() {
				switch mutation {
				case "disconnect":
					_, err := f.project.Store.Integrations().DisconnectIntegration(t.Context(),
						integrationstore.DisconnectIntegrationInput{ProjectID: f.project.ProjectUUID, IntegrationID: f.integration.ID})
					require.NoError(t, err)
				case "rotation":
					f.rotateCredential(t)
				case "reconfigure", "secret":
					body := integrationSetupHTTPBody("123", "456")
					body["credential_secret_id"] = f.secretID
					if mutation == "secret" {
						credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(t.Context(),
							secretstore.ReadProjectAvailableSecretPayloadInput{
								OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
								SecretID: f.integration.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
							})
						require.NoError(t, err)
						body["credential_secret_id"] = createIntegrationSetupHTTPSecret(t, f.handler, f.project,
							"replacement-github-credentials", map[string]any{
								"kind": "github_app_credentials", "app_id": credential.Payload[secrets.KeyAppID],
								"private_key":    credential.Payload[secrets.KeyPrivateKey],
								"webhook_secret": credential.Payload[secrets.KeyWebhookSecret],
							})
					}
					body["expected_setup_revision"] = f.integration.SetupRevision
					requestJSONWithHeaders(t, f.handler, http.MethodPost, integrationSetupPath(t, f.project, f.integration),
						integrationHTTPJSON(t, body), "", http.StatusOK, authHeaders(f.project.AdminToken))
				case "config":
					f.changeConfig(t, f.process, f.base)
				case "changed pin":
					other := githubHTTPJourneyIntegration(t, f.handler, f.project, f.secretID, "457")
					config := f.config(t, &agentconfig.GitCredentialsCompiled{
						Integration: other.Name, IntegrationID: other.ID,
					}, "")
					f.changeConfig(t, f.process, config)
				case "same integration new config":
					config := f.config(t, &agentconfig.GitCredentialsCompiled{
						Integration: f.integration.Name, IntegrationID: f.integration.ID,
					}, " Updated instruction.")
					f.changeConfig(t, f.process, config)
				case "closed process":
					_, err := integrationPoolForHandler(t, f.handler).Exec(t.Context(),
						`UPDATE processes SET state='unknown',state_reason_code='daemon_unreachable' WHERE id=$1`,
						f.process.ProcessUUID)
					require.NoError(t, err)
				}
			}
			unchangedCredentials := mutation == "same integration new config" || mutation == "reconfigure"
			status := http.StatusNotFound
			if unchangedCredentials {
				status = http.StatusOK
			}
			response := f.request(t, f.process.Token, f.process.ProcessID, status)
			if unchangedCredentials {
				require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
				require.Contains(t, response.Body.String(), "git-installation-token-1")
			}
			require.EqualValues(t, 1, f.minted.Load())
			f.duringIssuance = nil
			if mutation == "rotation" || mutation == "secret" {
				response := f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
				require.Contains(t, response.Body.String(), "git-installation-token-2")
				require.EqualValues(t, 2, f.minted.Load(), "a changed credential identity cannot reuse the old cached client")
			} else if unchangedCredentials {
				f.request(t, f.process.Token, f.process.ProcessID, http.StatusOK)
				require.EqualValues(t, 1, f.minted.Load(), "unrelated config and setup edits preserve cached credentials")
			} else {
				f.request(t, f.process.Token, f.process.ProcessID, http.StatusNotFound)
				require.EqualValues(t, 1, f.minted.Load(), "revocation also rejects cached credentials")
			}
		})
	}
}
