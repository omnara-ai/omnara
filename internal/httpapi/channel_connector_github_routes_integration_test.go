//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

const githubHTTPReviewCommit = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"

func TestChannelConnectorGitHubReviewLookupAndRecordJourney(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewHTTPFixture(t)
	before := f.creator(t)
	input := f.record(t)
	lookup := openapi.LookupChannelConnectorGitHubReviewsRequest{
		Scope: f.scope(t, 1), Observations: []openapi.GitHubReviewObservation{
			{ReviewId: "PRR_created", CreatingToolCallId: &input.Observation.CreatingToolCallId},
			{ReviewId: "PRR_unknown"},
		},
	}
	owned := map[string]any{
		"review_id": "PRR_created", "ownership": "owned",
		"creating_tool_call_id": input.Observation.CreatingToolCallId, "commit_id": strings.ToLower(githubHTTPReviewCommit),
	}
	response := f.post(t, f.path(t, "lookup"), workflowHTTPJSON(t, lookup), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"observations": []any{
		owned, map[string]any{"review_id": "PRR_unknown", "ownership": "unknown"},
	}}, response)
	require.Equal(t, before, f.creator(t), "read-only marker classification cannot record native identity")
	for range 2 {
		response = f.post(t, f.path(t, "record"), workflowHTTPJSON(t, input), f.token, http.StatusOK)
		require.Equal(t, map[string]any{"recorded": true, "continue": true}, response)
	}
	recordedID := input.Observation.ReviewId
	before.NativeID = &recordedID
	require.Equal(t, before, f.creator(t), "the HTTP acknowledgment commits only the native identity")
	otherCommit := strings.Repeat("1", 40)
	lookup.Observations = []openapi.GitHubReviewObservation{{ReviewId: "PRR_created", CommitId: &otherCommit}}
	response = f.post(t, f.path(t, "lookup"), workflowHTTPJSON(t, lookup), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"observations": []any{owned}}, response)
	input.Observation.ReviewId = "PRR_replacement"
	f.post(t, f.path(t, "record"), workflowHTTPJSON(t, input), f.token, http.StatusConflict)
	require.Equal(t, before, f.creator(t), "a conflicting acknowledgment cannot replace the first identity")
}

func TestChannelConnectorGitHubReviewReadLookupCannotRecord(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewHTTPFixture(t)
	before := f.creator(t)
	input := f.record(t)
	readScope := f.scope(t, 3)
	lookup := openapi.LookupChannelConnectorGitHubReviewsRequest{
		Scope: readScope, Observations: []openapi.GitHubReviewObservation{{
			ReviewId: input.Observation.ReviewId, CreatingToolCallId: &input.Observation.CreatingToolCallId,
		}},
	}
	owned := map[string]any{
		"review_id": input.Observation.ReviewId, "ownership": "owned",
		"creating_tool_call_id": input.Observation.CreatingToolCallId, "commit_id": strings.ToLower(githubHTTPReviewCommit),
	}
	response := f.post(t, f.path(t, "lookup"), workflowHTTPJSON(t, lookup), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"observations": []any{owned}}, response)
	for _, evidence := range []openapi.GitHubReviewIdentityEvidence{
		openapi.GitHubReviewMarker, openapi.GitHubReviewCreateResponse,
	} {
		readRecord := input
		readRecord.Scope, readRecord.Evidence = readScope, evidence
		f.post(t, f.path(t, "record"), workflowHTTPJSON(t, readRecord), f.token, http.StatusForbidden)
		require.Equal(t, before, f.creator(t), "read lookup cannot authorize recording an owned marker")
	}
	f.post(t, f.path(t, "record"), workflowHTTPJSON(t, input), f.token, http.StatusOK)
	before = f.creator(t)
	lookup.Observations = []openapi.GitHubReviewObservation{
		{ReviewId: input.Observation.ReviewId}, {ReviewId: "PRR_unknown"},
	}
	response = f.post(t, f.path(t, "lookup"), workflowHTTPJSON(t, lookup), f.token, http.StatusOK)
	require.Equal(t, map[string]any{"observations": []any{
		owned, map[string]any{"review_id": "PRR_unknown", "ownership": "unknown"},
	}}, response)
	for _, name := range []string{"channel", "agent", "install", "app", "capability"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invalid := lookup
			path, token, status := f.path(t, "lookup"), f.token, http.StatusNotFound
			switch name {
			case "channel":
				invalid.Scope.ChannelId = testPublicID(t, publicid.KindIntegrationTarget, f.otherTarget.ID)
				status = http.StatusForbidden
			case "agent":
				invalid.Scope.AgentId = testPublicID(t, publicid.KindAgent, f.otherAgentID)
			case "install":
				path = strings.Replace(path, testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
					testPublicID(t, publicid.KindIntegrationInstall, f.otherInstall.ID), 1)
			case "app":
				path = strings.Replace(path, testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
					testPublicID(t, publicid.KindIntegrationApp, f.otherApp.ID), 1)
			case "capability":
				token, status = f.otherToken, http.StatusForbidden
			}
			f.post(t, path, workflowHTTPJSON(t, invalid), token, status)
			require.Equal(t, before, f.creator(t))
		})
	}
}

func TestChannelConnectorGitHubReviewRejectsWrongScopeAndAuthentication(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewHTTPFixture(t)
	before := f.creator(t)
	for _, operation := range []string{"lookup", "record"} {
		for _, name := range []string{
			"no_bearer", "account_bearer", "wrong_capability_pair", "app", "installation",
			"agent", "request", "actual_call_target", "timeline_call", "unconfigured_origin",
		} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				t.Parallel()
				input := f.record(t)
				path, token, status := f.path(t, operation), f.token, http.StatusNotFound
				switch name {
				case "no_bearer":
					token, status = "", http.StatusUnauthorized
				case "account_bearer":
					token, status = f.project.AdminToken, http.StatusForbidden
				case "wrong_capability_pair":
					token, status = f.otherToken, http.StatusForbidden
				case "app":
					path = strings.Replace(path, testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
						testPublicID(t, publicid.KindIntegrationApp, f.otherApp.ID), 1)
				case "installation":
					path = strings.Replace(path, testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
						testPublicID(t, publicid.KindIntegrationInstall, f.otherInstall.ID), 1)
				case "agent":
					input.Scope.AgentId = testPublicID(t, publicid.KindAgent, f.otherAgentID)
				case "request":
					input.Scope.RequestId = testPublicID(t, publicid.KindToolCall, uuid.New())
				case "actual_call_target":
					input.Scope.ChannelId = testPublicID(t, publicid.KindIntegrationTarget, f.otherTarget.ID)
					status = http.StatusForbidden
				case "timeline_call":
					input.Scope = f.scope(t, 2)
					status = http.StatusForbidden
				case "unconfigured_origin":
					path = "https://untrusted.example.test" + path
				}
				body := workflowHTTPJSON(t, input)
				if operation == "lookup" {
					body = workflowHTTPJSON(t, openapi.LookupChannelConnectorGitHubReviewsRequest{
						Scope: input.Scope, Observations: []openapi.GitHubReviewObservation{{
							ReviewId: input.Observation.ReviewId, CommitId: input.Observation.CommitId,
							CreatingToolCallId: &input.Observation.CreatingToolCallId,
						}},
					})
				}
				f.post(t, path, body, token, status)
				require.Equal(t, before, f.creator(t), "rejected requests cannot change the creator")
			})
		}
	}
}

func TestChannelConnectorGitHubReviewDecodingAndStrictCreationEvidence(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewHTTPFixture(t)
	before := f.creator(t)
	input := f.record(t)
	recordBody := workflowHTTPJSON(t, input)
	lookupBody := workflowHTTPJSON(t, openapi.LookupChannelConnectorGitHubReviewsRequest{
		Scope: input.Scope, Observations: []openapi.GitHubReviewObservation{{ReviewId: "PRR_created"}},
	})
	for _, operation := range []string{"lookup", "record"} {
		body := recordBody
		if operation == "lookup" {
			body = lookupBody
		}
		for _, invalid := range []string{"", "null", "{", body + `{}`, strings.TrimSuffix(body, "}") + `,"project_id":"x"}`} {
			f.post(t, f.path(t, operation), invalid, f.token, http.StatusBadRequest)
		}
		for _, field := range []string{"agent_id", "channel_id", "request_id"} {
			var invalid map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &invalid))
			testutil.RequireType[map[string]any](t, invalid["scope"])[field] = uuid.NewString()
			f.post(t, f.path(t, operation), workflowHTTPJSON(t, invalid), f.token, http.StatusBadRequest)
		}
	}
	for _, name := range []string{
		"missing_creator", "empty_creator", "raw_creator_uuid", "wrong_creator_kind", "null_creator",
		"bad_commit", "short_commit", "missing_commit", "different_commit", "another_admitted_request", "bad_evidence",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var invalid map[string]any
			require.NoError(t, json.Unmarshal([]byte(recordBody), &invalid))
			observation := testutil.RequireType[map[string]any](t, invalid["observation"])
			status := http.StatusBadRequest
			switch name {
			case "missing_creator":
				delete(observation, "creating_tool_call_id")
			case "empty_creator":
				observation["creating_tool_call_id"] = ""
			case "raw_creator_uuid":
				observation["creating_tool_call_id"] = f.calls[0].ID.String()
			case "wrong_creator_kind":
				observation["creating_tool_call_id"] = input.Scope.AgentId
			case "null_creator":
				observation["creating_tool_call_id"] = nil
			case "bad_commit":
				observation["commit_id"] = strings.Repeat("z", 40)
			case "short_commit":
				observation["commit_id"] = "abcd"
			case "missing_commit":
				delete(observation, "commit_id")
				status = http.StatusForbidden
			case "different_commit":
				observation["commit_id"] = strings.Repeat("2", 40)
				status = http.StatusForbidden
			case "another_admitted_request":
				invalid["scope"] = f.scope(t, 1)
				status = http.StatusForbidden
			case "bad_evidence":
				invalid["evidence"] = "unverified"
			}
			f.post(t, f.path(t, "record"), workflowHTTPJSON(t, invalid), f.token, status)
			require.Equal(t, before, f.creator(t))
		})
	}
	batch := make([]openapi.GitHubReviewObservation, executionstore.MaxGitHubReviewObservations+1)
	for i := range batch {
		batch[i].ReviewId = fmt.Sprintf("PRR_%d", i)
	}
	f.post(t, f.path(t, "lookup"), workflowHTTPJSON(t, openapi.LookupChannelConnectorGitHubReviewsRequest{
		Scope: input.Scope, Observations: batch,
	}), f.token, http.StatusBadRequest)
	require.Equal(t, before, f.creator(t))
}

func TestChannelConnectorGitHubReviewMarkerAllowsOptionalNativeCommit(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"absent", "different"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubReviewHTTPFixture(t)
			before := f.creator(t)
			input := f.record(t)
			input.Scope, input.Evidence = f.scope(t, 1), openapi.GitHubReviewMarker
			input.Observation.CommitId = nil
			if name == "different" {
				commit := strings.Repeat("3", 40)
				input.Observation.CommitId = &commit
			}
			response := f.post(t, f.path(t, "record"), workflowHTTPJSON(t, input), f.token, http.StatusOK)
			require.Equal(t, map[string]any{"recorded": true, "continue": false}, response)
			before.NativeID = &input.Observation.ReviewId
			require.Equal(t, before, f.creator(t), "marker evidence never replaces the creator's original commit")
		})
	}
}

func TestChannelConnectorGitHubReviewRecordsAfterStopWithoutContinuing(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewHTTPFixture(t)
	before := f.creator(t)
	stopped, err := f.project.Store.Execution().CancelAgent(t.Context(), executionstore.CancelAgentInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.agentID,
		Actor: httpOmnaraActorParams(t, f.project.OrgUUID, f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	require.True(t, stopped.Affected)
	callBefore, err := f.project.Store.Execution().GetToolCall(
		t.Context(), f.project.ProjectUUID, f.agentID, f.calls[0].ID)
	require.NoError(t, err)
	input := f.record(t)
	for range 2 {
		response := f.post(t, f.path(t, "record"), workflowHTTPJSON(t, input), f.token, http.StatusOK)
		require.Equal(t, map[string]any{"recorded": true, "continue": false}, response)
	}
	before.NativeID = &input.Observation.ReviewId
	require.Equal(t, before, f.creator(t))
	callAfter, err := f.project.Store.Execution().GetToolCall(t.Context(), f.project.ProjectUUID, f.agentID, f.calls[0].ID)
	require.NoError(t, err)
	require.Equal(t, callBefore, callAfter, "late acknowledgment cannot reopen or complete the stopped tool")
}

type githubReviewHTTPFixture struct {
	channelReceiptHTTPFixture
	target, otherTarget   integrationstore.IntegrationTargetRecord
	agentID, otherAgentID uuid.UUID
	calls                 []executionstore.ToolCallRecord
}

func newGitHubReviewHTTPFixture(t *testing.T) *githubReviewHTTPFixture {
	t.Helper()
	ctx := t.Context()
	f := &githubReviewHTTPFixture{channelReceiptHTTPFixture: channelReceiptHTTPFixture{pool: openIntegrationDB(t, ctx)}}
	var err error
	f.token, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	f.otherToken, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	capabilities := []channelconnector.Capability{{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "github"}}
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{ID: "github-review-http", Token: f.token, Capabilities: capabilities},
		{ID: "github-review-other", Token: f.otherToken, Capabilities: []channelconnector.Capability{
			{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "discord"},
			{ConnectorKey: "other_connector", Provider: "github"},
		}},
	})
	require.NoError(t, err)
	f.handler = newIntegrationServer(f.pool, WithChannelConnectorAuthenticator(auth),
		WithInternalAPIOrigins([]string{"http://api:8080"}), WithPublicURL("https://omnara.example.test"))
	f.project = bootstrapPublicHTTPProject(t, f.handler, "github-review-http")
	for _, app := range []*integrationstore.IntegrationAppRecord{&f.app, &f.otherApp} {
		*app, err = f.project.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
			OrgID: f.project.OrgUUID, Provider: "github", ProviderAppRef: uuid.NewString(), DisplayName: "GitHub test",
			ConnectorKey: channelconnector.BuiltInConnectorKey, State: integrationstore.IntegrationAppStateActive,
		})
		require.NoError(t, err)
	}
	f.install = f.createInstall(t, f.app, f.project.ProjectUUID, "github-primary")
	f.otherInstall = f.createInstall(t, f.app, f.project.ProjectUUID, "github-other")
	definition, err := f.project.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: f.project.ProjectUUID, IntegrationInstallID: f.install.ID,
			ConnectorCapabilities: capabilities, ImplementationKey: "github_pr", Kind: integrationstore.ChannelKindGitHubPR,
			Description: "HTTP review fixture", SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities: integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
		})
	require.NoError(t, err)
	for i, target := range []*integrationstore.IntegrationTargetRecord{&f.target, &f.otherTarget} {
		*target, err = f.project.Store.Integrations().CreateIntegrationTarget(ctx,
			integrationstore.CreateIntegrationTargetInput{
				ProjectID: f.project.ProjectUUID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: definition.ID,
				ProviderRef: fmt.Sprintf("PR_%d", i), ProviderRefKind: "pull_request",
			})
		require.NoError(t, err)
	}
	f.admitTools(t)
	other := createHTTPRuntimeAgent(t, ctx, f.project.Store, f.project.OrgUUID,
		f.project.ProjectUUID, f.project.AdminUserUUID, "github-other-agent")
	f.otherAgentID = other.Agent.ID
	return f
}

func (f *githubReviewHTTPFixture) admitTools(t *testing.T) {
	t.Helper()
	ctx, store := t.Context(), f.project.Store
	launch := createHTTPRuntimeAgent(t, ctx, store, f.project.OrgUUID, f.project.ProjectUUID,
		f.project.AdminUserUUID, "github-review-agent")
	f.agentID = launch.Agent.ID
	for _, target := range []integrationstore.IntegrationTargetRecord{f.target, f.otherTarget} {
		_, err := store.Integrations().CreateIntegrationTargetBinding(ctx,
			integrationstore.CreateIntegrationTargetBindingInput{
				ProjectID: f.project.ProjectUUID, AgentID: f.agentID, IntegrationInstallID: f.install.ID,
				IntegrationTargetID: target.ID, ReadAllowed: true, SendAllowed: true, Source: "github-http-test",
			})
		require.NoError(t, err)
	}
	input, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.agentID,
		Actor:         httpOmnaraActorParams(t, f.project.OrgUUID, f.project.AdminUserUUID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Review this pull request"}]`), IdempotencyKey: "github-input",
	})
	require.NoError(t, err)
	work, found, err := store.Execution().ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, f.agentID, work.RuntimeLock.AgentID)
	claim := claimNormalModelCallForHTTPTest(t, ctx, store, f.project.ProjectUUID, f.agentID, work.RuntimeLock,
		[]uuid.UUID{input.ID}, launch.AgentConfig.ID, work.Model.AdmittedInputTurn.Events[0].Sequence)
	params := []string{
		`{"review_comment":true,"commit_id":"` + githubHTTPReviewCommit + `","path":"src/main.go","subject_type":"file"}`,
		`{"publish_review":true}`, `{}`,
	}
	proposals := make([]model.ToolCall, len(params)+1)
	bindings := make([]executionstore.ToolCallBindingInput, len(params)+1)
	for i, raw := range params {
		proposals[i] = model.ToolCall{ID: fmt.Sprintf("github-send-%d", i), Name: toolcatalog.ToolNameSendChannelMessage,
			Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q,"message":{"text":"Review finding"},"params":%s}`,
				testPublicID(t, publicid.KindIntegrationTarget, f.target.ID), raw))}
		bindings[i] = executionstore.ToolCallBindingInput{ProviderCallID: proposals[i].ID, Type: toolcatalog.ToolTypeBuiltIn}
	}
	proposals[len(params)] = model.ToolCall{ID: "github-read", Name: toolcatalog.ToolNameReadChannel,
		Input: json.RawMessage(fmt.Sprintf(`{"channel_id":%q,"limit":3}`,
			testPublicID(t, publicid.KindIntegrationTarget, f.target.ID)))}
	bindings[len(params)] = executionstore.ToolCallBindingInput{
		ProviderCallID: "github-read", Type: toolcatalog.ToolTypeBuiltIn,
	}
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, store, f.project.ProjectUUID, claim.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "github-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls(proposals)})
	require.NoError(t, err)
	_, f.calls, err = store.Execution().RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: f.project.ProjectUUID, AgentID: f.agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: response, ToolCallBindings: bindings,
		})
	require.NoError(t, err)
	require.Len(t, f.calls, len(proposals))
	for i, call := range f.calls {
		_, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID: f.project.ProjectUUID, AgentID: f.agentID, ID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
		})
		require.NoError(t, err)
		owner := executionstore.PrepareChannelOperationInput{
			ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
				ProjectID: f.project.ProjectUUID, AgentID: f.agentID, ToolCallID: call.ID, RuntimeLockID: work.RuntimeLock.ID,
			}, TurnID: call.TurnID, ChannelID: f.target.ID, Operation: integrationstore.ChannelBindingOperationSend,
		}
		if call.Name == toolcatalog.ToolNameReadChannel {
			owner.Operation = integrationstore.ChannelBindingOperationRead
		}
		started, err := store.Execution().ExecuteToolCall(ctx, owner.ExecuteToolCallInput,
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.StartToolCallAsync(), nil
			})
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallDispositionRunning, started.Disposition)
		if i == 0 {
			prepared, err := store.Execution().PrepareChannelOperation(ctx, owner)
			require.NoError(t, err)
			_, _, err = store.Execution().PrepareChannelSend(ctx, prepared)
			require.NoError(t, err)
		}
		if owner.Operation == integrationstore.ChannelBindingOperationRead {
			prepared, err := store.Execution().PrepareChannelOperation(ctx, owner)
			require.NoError(t, err)
			_, err = store.Execution().RecheckChannelOperation(ctx, prepared)
			require.NoError(t, err)
		}
	}
}

func (f *githubReviewHTTPFixture) path(t *testing.T, operation string) string {
	t.Helper()
	return "/api/v1/channel-connector/apps/" + testPublicID(t, publicid.KindIntegrationApp, f.app.ID) +
		"/installations/" + testPublicID(t, publicid.KindIntegrationInstall, f.install.ID) + "/github-reviews/" + operation
}

func (f *githubReviewHTTPFixture) scope(t *testing.T, call int) openapi.GitHubReviewOperationScope {
	t.Helper()
	return openapi.GitHubReviewOperationScope{
		AgentId:   testPublicID(t, publicid.KindAgent, f.agentID),
		ChannelId: testPublicID(t, publicid.KindIntegrationTarget, f.target.ID),
		RequestId: testPublicID(t, publicid.KindToolCall, f.calls[call].ID),
	}
}

func (f *githubReviewHTTPFixture) record(t *testing.T) openapi.RecordChannelConnectorGitHubReviewRequest {
	t.Helper()
	input := openapi.RecordChannelConnectorGitHubReviewRequest{
		Scope: f.scope(t, 0), Evidence: openapi.GitHubReviewCreateResponse,
	}
	commit := githubHTTPReviewCommit
	input.Observation.CommitId = &commit
	input.Observation.CreatingToolCallId = testPublicID(t, publicid.KindToolCall, f.calls[0].ID)
	input.Observation.ReviewId = "PRR_created"
	return input
}

type githubReviewHTTPCreator struct {
	BindingID uuid.UUID
	CommitID  string
	NativeID  *string
}

func (f *githubReviewHTTPFixture) creator(t *testing.T) githubReviewHTTPCreator {
	t.Helper()
	var row githubReviewHTTPCreator
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT creating_binding_id, commit_id, provider_review_id
FROM github_pr_reviews WHERE project_id=$1 AND agent_id=$2 AND creating_tool_call_id=$3`,
		f.project.ProjectUUID, f.agentID, f.calls[0].ID).Scan(&row.BindingID, &row.CommitID, &row.NativeID))
	return row
}

// Use the actual private origin for every request, except explicit origin-rejection cases.
func (f *githubReviewHTTPFixture) post(t *testing.T, path, body, token string, status int) map[string]any {
	t.Helper()
	if strings.HasPrefix(path, "/") {
		path = "http://api:8080" + path
	}
	return requestJSONWithHeaders(t, f.handler, http.MethodPost, path, body, "", status, authHeaders(token))
}
