//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

const githubReviewCommit = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
const githubReviewNativeID = "PRR_owned_review"

// A registered file-finding/summary contract for these storage tests. Provider
// schema generation and native GitHub requests are tested by the gateway owner.
const githubReviewPreparationSchema = `{"oneOf":[
  {"type":"object","additionalProperties":false},
  {"type":"object","properties":{
    "review_comment":{"const":true},"review_id":{"type":"string","minLength":1},
    "commit_id":{"type":"string","pattern":"^[0-9a-fA-F]{40}$"},
    "path":{"type":"string","minLength":1},"subject_type":{"const":"file"}},
   "required":["review_comment","commit_id","path","subject_type"],"additionalProperties":false},
  {"type":"object","properties":{
    "publish_review":{"const":true},"review_id":{"type":"string","minLength":1}},
   "required":["publish_review"],"additionalProperties":false}
]}`

func TestGitHubReviewPreparationRecordsDurableCreatorAndOriginalBinding(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewPreparationFixture(t)
	operation := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
	prepared := prepareGitHubReviewOperation(t, operation)
	before, err := f.Store.Execution().GetToolCall(t.Context(), testProjectID, f.AgentID, operation.input.ToolCallID)
	require.NoError(t, err)
	for range 2 {
		access, params, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepared)
		require.NoError(t, err)
		require.Equal(t, f.target.ID, access.ChannelID)
		var returned struct {
			CommitID string `json:"commit_id"`
		}
		require.NoError(t, json.Unmarshal(params, &returned))
		require.Equal(t, githubReviewCommit, returned.CommitID, "provider params retain the original commit spelling")
	}
	f.requireCount(t, 1)
	creator := f.creator(t, operation.input.ToolCallID)
	require.Equal(t, testProjectID, creator.ProjectID)
	require.Equal(t, f.AgentID, creator.AgentID)
	require.Equal(t, operation.input.ToolCallID, creator.ToolCallID)
	require.Equal(t, f.install.ID, creator.InstallID)
	require.Equal(t, f.target.ID, creator.ChannelID)
	require.Equal(t, prepared.Binding().ID, creator.BindingID)
	require.Equal(t, strings.ToLower(githubReviewCommit), creator.CommitID)
	require.Nil(t, creator.NativeID, "preparation cannot fabricate a provider acknowledgment")
	require.False(t, creator.CreatedAt.IsZero())
	after, err := f.Store.Execution().GetToolCall(t.Context(), testProjectID, f.AgentID, operation.input.ToolCallID)
	require.NoError(t, err)
	require.Equal(t, before.Input, after.Input, "creator preparation never rewrites durable tool arguments")
}

func TestGitHubReviewPreparationValidatesCurrentSchemaAndActualChannelBeforeInsert(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"current_schema_changed", "invalid_params", "wrong_actual_channel"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubReviewPreparationFixture(t)
			params := githubReviewFinding(githubReviewCommit, "")
			if name == "invalid_params" {
				params = `{"review_comment":true,"commit_id":42}`
			}
			operation := f.tools(t, githubReviewSend{params: params})[0]
			if name == "wrong_actual_channel" {
				// Both PRs are send-authorized. The durable call still addresses the
				// first one, so a caller cannot prepare against the second instead.
				other := f.channel(t, "PR_second")
				operation.input.ChannelID = other.ID
			}
			prepared := prepareGitHubReviewOperation(t, operation)
			if name == "current_schema_changed" {
				updated := f.definition
				updated.SendParamsSchema = json.RawMessage(`{"type":"object","additionalProperties":false}`)
				_, err := f.Store.Integrations().PublishConnectorChannelDefinition(t.Context(), updated)
				require.NoError(t, err)
			}
			_, paramsOut, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepared)
			if name == "wrong_actual_channel" {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			} else {
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			}
			require.Nil(t, paramsOut)
			f.requireCount(t, 0)
		})
	}
}

func TestGitHubReviewPreparationChecksExplicitReviewOwnershipAndCommit(t *testing.T) {
	t.Parallel()
	cases := []string{"own_finding", "own_summary", "other_agent", "other_pr", "unknown_id", "commit_mismatch"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newGitHubReviewPreparationFixture(t)
			attempt := githubReviewSend{params: githubReviewFinding(githubReviewCommit, githubReviewNativeID)}
			switch name {
			case "own_summary":
				attempt.params = `{"publish_review":true,"review_id":"` + githubReviewNativeID + `"}`
			case "other_pr":
				attempt.channel = f.channel(t, "PR_other").ID
			case "unknown_id":
				attempt.params = githubReviewFinding(githubReviewCommit, "PRR_unknown")
			case "commit_mismatch":
				attempt.params = githubReviewFinding(strings.Repeat("1", 40), githubReviewNativeID)
			}
			proposals := []githubReviewSend{{params: githubReviewFinding(githubReviewCommit, "")}}
			if name != "other_agent" {
				proposals = append(proposals, attempt)
			}
			operations := f.tools(t, proposals...)
			_, _, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepareGitHubReviewOperation(t, operations[0]))
			require.NoError(t, err)
			f.seedNativeID(t, operations[0].input.ToolCallID)
			before := f.creator(t, operations[0].input.ToolCallID)
			if name == "other_agent" {
				other := f
				other.processDaemonFixture = newProcessDaemonFixtureInStore(
					t, t.Context(), f.Store, f.UserID, "github-other-agent", f.Now)
				other.grant(t, f.target.ID)
				operations = append(operations, other.tools(t, attempt)[0])
			}
			_, _, err = f.Store.Execution().PrepareChannelSend(t.Context(), prepareGitHubReviewOperation(t, operations[1]))
			switch name {
			case "own_finding", "own_summary":
				require.NoError(t, err, "explicit owned identity remains usable even while its creator is running")
			case "commit_mismatch":
				requireGitHubReviewError(t, err, executionstore.GitHubReviewError{
					Code: "review_commit_mismatch", ReviewID: githubReviewNativeID, CommitID: strings.ToLower(githubReviewCommit),
				})
			default:
				requireGitHubReviewError(t, err, executionstore.GitHubReviewError{Code: "review_not_owned"})
			}
			f.requireCount(t, 1)
			require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID))
		})
	}
}

func TestGitHubReviewPreparationRunningCreatorBlocksOnlyOmittedReviewActions(t *testing.T) {
	t.Parallel()
	f := newGitHubReviewPreparationFixture(t)
	operations := f.tools(t,
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")},
		githubReviewSend{params: `{"publish_review":true}`}, githubReviewSend{params: `{}`})
	prepared := make([]executionstore.PreparedChannelOperation, len(operations))
	for index, operation := range operations {
		prepared[index] = prepareGitHubReviewOperation(t, operation)
	}
	_, _, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepared[0])
	require.NoError(t, err)
	before := f.creator(t, operations[0].input.ToolCallID)
	for _, index := range []int{1, 2} {
		_, _, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepared[index])
		requireGitHubReviewError(t, err, executionstore.GitHubReviewError{Code: "review_creation_in_progress"})
	}
	_, _, err = f.Store.Execution().PrepareChannelSend(t.Context(), prepared[3])
	require.NoError(t, err, "ordinary timeline comments do not acquire or wait for reviews")
	f.requireCount(t, 1)
	require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID))

	// A lost/failed creation can finish without ever learning a native ID. Its
	// historical row must not permanently block future explicit agent actions.
	_, err = f.Store.Execution().CompleteRuntimeToolCall(t.Context(), executionstore.CompleteRuntimeToolCallInput{
		ProjectID: testProjectID, AgentID: f.AgentID, ID: operations[0].input.ToolCallID, RuntimeLockID: f.Lock.ID,
		Outcome:            executionstore.ToolResultOutcomeFailed,
		ResultContentParts: json.RawMessage(`[{"type":"text","text":"provider outcome unknown"}]`),
	})
	require.NoError(t, err)
	_, _, err = f.Store.Execution().PrepareChannelSend(t.Context(), prepared[2])
	require.NoError(t, err, "summary-only submission acquires no creator row")
	f.requireCount(t, 1)
	_, _, err = f.Store.Execution().PrepareChannelSend(t.Context(), prepared[1])
	require.NoError(t, err, "a new explicit finding can prepare after the original tool terminates")
	f.requireCount(t, 2)
	require.Equal(t, before, f.creator(t, operations[0].input.ToolCallID))
}

func TestGitHubReviewPreparationCannotReplaceRevokedOriginalBinding(t *testing.T) {
	t.Parallel()
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement_%t", replacement), func(t *testing.T) {
			t.Parallel()
			f := newGitHubReviewPreparationFixture(t)
			operation := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
			prepared := prepareGitHubReviewOperation(t, operation)
			_, _, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepared)
			require.NoError(t, err)
			before := f.creator(t, operation.input.ToolCallID)
			require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(
				t.Context(), testProjectID, prepared.Binding().ID))
			if replacement {
				binding := f.grant(t, f.target.ID)
				require.NotEqual(t, prepared.Binding().ID, binding.ID)
				access, err := f.Store.Integrations().GetAgentChannelAccess(t.Context(), testProjectID, f.AgentID, f.target.ID)
				require.NoError(t, err)
				require.True(t, access.Capabilities.Send, "isolate the original pin from aggregate channel access")
			}
			_, _, err = f.Store.Execution().PrepareChannelSend(t.Context(), prepared)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			f.requireCount(t, 1)
			require.Equal(t, before, f.creator(t, operation.input.ToolCallID))
		})
	}
}

func TestGitHubReviewPreparationCancelAndDisconnectPreserveCreatorFacts(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"cancel", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			f := newGitHubReviewPreparationFixture(t)
			operation := f.tools(t, githubReviewSend{params: githubReviewFinding(githubReviewCommit, "")})[0]
			prepared := prepareGitHubReviewOperation(t, operation)
			_, _, err := f.Store.Execution().PrepareChannelSend(t.Context(), prepared)
			require.NoError(t, err)
			f.seedNativeID(t, operation.input.ToolCallID)
			before := f.creator(t, operation.input.ToolCallID)
			if action == "cancel" {
				result, err := f.Store.Execution().CancelAgent(t.Context(), executionstore.CancelAgentInput{
					ProjectID: testProjectID, AgentID: f.AgentID, Actor: mustOmnaraActorParams(t, f.UserID),
				})
				require.NoError(t, err)
				require.True(t, result.Affected)
			} else {
				require.NoError(t, f.Store.Integrations().DeleteIntegrationInstall(t.Context(), testProjectID, f.install.ID))
			}
			_, _, err = f.Store.Execution().PrepareChannelSend(t.Context(), prepared)
			require.Error(t, err, "retained provider facts cannot authorize another dispatch")
			f.requireCount(t, 1)
			require.Equal(t, before, f.creator(t, operation.input.ToolCallID))
		})
	}
}

type githubReviewPreparationFixture struct {
	processDaemonFixture
	install    integrationstore.IntegrationInstallRecord
	definition integrationstore.PublishChannelDefinitionInput
	target     integrationstore.IntegrationTargetRecord
}

func newGitHubReviewPreparationFixture(t *testing.T) githubReviewPreparationFixture {
	t.Helper()
	ctx := t.Context()
	f := githubReviewPreparationFixture{processDaemonFixture: newProcessDaemonFixture(t, ctx, "github-review")}
	admin := createIntegrationProjectAdmin(t, ctx, f.Store, "github-review-installer@example.com")
	app, err := f.Store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, Provider: "github", ProviderAppRef: "github-review-app", DisplayName: "Review app",
		ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	f.install, err = f.Store.Integrations().UpsertIntegrationInstall(ctx, integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID,
		InstalledBy: identitystore.NewUserPrincipal(admin.ID), Provider: "github",
		IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
		State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: "42", ProviderAccountRef: "repository-17",
	})
	require.NoError(t, err)
	f.definition = integrationstore.PublishChannelDefinitionInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID,
		ImplementationKey: "github_pr", Kind: integrationstore.ChannelKindGitHubPR,
		SendParamsSchema:      json.RawMessage(githubReviewPreparationSchema),
		Capabilities:          integrationstore.ChannelCapabilities{Send: true, Text: true},
		ConnectorCapabilities: testChannelCapabilities("github"),
	}
	f.target = f.channel(t, "PR_first")
	return f
}

func (f githubReviewPreparationFixture) channel(t *testing.T, ref string) integrationstore.IntegrationTargetRecord {
	t.Helper()
	definition, err := f.Store.Integrations().PublishConnectorChannelDefinition(t.Context(), f.definition)
	require.NoError(t, err)
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: ref, ProviderRefKind: "pull_request",
	}
	target, err := f.Store.Integrations().CreateIntegrationTarget(t.Context(), input)
	require.NoError(t, err)
	f.grant(t, target.ID)
	return target
}

func (f githubReviewPreparationFixture) grant(
	t *testing.T, channelID uuid.UUID,
) integrationstore.IntegrationTargetBindingRecord {
	t.Helper()
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(t.Context(),
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: f.install.ID,
			IntegrationTargetID: channelID, SendAllowed: true, Source: "github-review-test",
		})
	require.NoError(t, err)
	return binding
}

type githubReviewSend struct {
	channel uuid.UUID
	params  string
}

func (f githubReviewPreparationFixture) tools(t *testing.T, sends ...githubReviewSend) []managedOperationFixture {
	t.Helper()
	items := make([]processToolCallBatchItem, len(sends))
	for index, send := range sends {
		if send.channel == uuid.Nil {
			sends[index].channel = f.target.ID
		}
		channelID, err := publicid.Encode(publicid.KindIntegrationTarget, sends[index].channel)
		require.NoError(t, err)
		items[index] = processToolCallBatchItem{
			TestName: fmt.Sprintf("review-%d", index), ToolName: toolcatalog.ToolNameSendChannelMessage,
			ToolType: toolcatalog.ToolTypeBuiltIn, Allowed: true,
			Input: json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"Review finding"},"params":` +
				send.params + `}`),
		}
	}
	ids := createToolCallBatchForProcessTest(t, t.Context(), f.processDaemonFixture, "github-review", items)
	operations := make([]managedOperationFixture, len(ids))
	for index, id := range ids {
		operations[index] = managedOperationFixture{processDaemonFixture: f.processDaemonFixture,
			input: executionstore.PrepareChannelOperationInput{
				ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
					ProjectID: testProjectID, AgentID: f.AgentID, ToolCallID: id, RuntimeLockID: f.Lock.ID,
				},
				TurnID:    turnIDForProcessToolCallTest(t, t.Context(), f.processDaemonFixture, id),
				ChannelID: sends[index].channel, Operation: integrationstore.ChannelBindingOperationSend,
			}}
		operations[index].start(t, t.Context())
	}
	return operations
}

func prepareGitHubReviewOperation(
	t *testing.T, operation managedOperationFixture,
) executionstore.PreparedChannelOperation {
	t.Helper()
	prepared, err := operation.Store.Execution().PrepareChannelOperation(t.Context(), operation.input)
	require.NoError(t, err)
	return prepared
}

func githubReviewFinding(commit, reviewID string) string {
	params := `{"review_comment":true,"commit_id":"` + commit + `","path":"src/main.go","subject_type":"file"`
	if reviewID != "" {
		params += `,"review_id":"` + reviewID + `"`
	}
	return params + `}`
}

func requireGitHubReviewError(t *testing.T, err error, expected executionstore.GitHubReviewError) {
	t.Helper()
	var reviewError *executionstore.GitHubReviewError
	require.ErrorAs(t, err, &reviewError)
	require.Equal(t, &expected, reviewError, "only recovery facts belonging to this agent and PR may be returned")
	require.EqualError(t, err, string(expected.Code))
}

func (f githubReviewPreparationFixture) seedNativeID(t *testing.T, callID uuid.UUID) {
	t.Helper()
	// Test-only acknowledgment until the owning Record RPC lands. This suite
	// proves core authority and retention, not native provider interoperability.
	tag, err := f.Store.pool.Exec(t.Context(), `UPDATE github_pr_reviews SET provider_review_id=$1
WHERE project_id=$2 AND agent_id=$3 AND creating_tool_call_id=$4`,
		githubReviewNativeID, testProjectID, f.AgentID, callID)
	require.NoError(t, err)
	require.EqualValues(t, 1, tag.RowsAffected())
}

func (f githubReviewPreparationFixture) requireCount(t *testing.T, want int) {
	t.Helper()
	var count int
	require.NoError(t, f.Store.pool.QueryRow(t.Context(), `SELECT count(*) FROM github_pr_reviews`).Scan(&count))
	require.Equal(t, want, count)
}

type githubReviewCreatorFacts struct {
	ProjectID, AgentID, ToolCallID, InstallID, ChannelID, BindingID uuid.UUID
	CommitID                                                        string
	NativeID                                                        *string
	CreatedAt                                                       time.Time
}

func (f githubReviewPreparationFixture) creator(t *testing.T, callID uuid.UUID) githubReviewCreatorFacts {
	t.Helper()
	var row githubReviewCreatorFacts
	require.NoError(t, f.Store.pool.QueryRow(t.Context(), `SELECT project_id, agent_id, creating_tool_call_id,
integration_install_id, pr_channel_id, creating_binding_id, commit_id, provider_review_id, created_at
FROM github_pr_reviews WHERE agent_id=$1 AND creating_tool_call_id=$2`, f.AgentID, callID).Scan(
		&row.ProjectID, &row.AgentID, &row.ToolCallID, &row.InstallID, &row.ChannelID, &row.BindingID,
		&row.CommitID, &row.NativeID, &row.CreatedAt))
	return row
}
