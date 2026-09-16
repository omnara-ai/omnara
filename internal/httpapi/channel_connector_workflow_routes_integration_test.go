//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type channelWorkflowHTTPFixture struct {
	channelReceiptHTTPFixture
	blobs        *channelWorkflowHTTPBlobs
	routeID      string
	definitionID string
}

func newChannelWorkflowHTTPFixture(t *testing.T) channelWorkflowHTTPFixture {
	t.Helper()
	f := channelWorkflowHTTPFixture{
		channelReceiptHTTPFixture: newChannelReceiptHTTPFixture(t), blobs: &channelWorkflowHTTPBlobs{},
	}
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{ID: "workflow-gateway", Token: f.token, Capabilities: connectorTestCapabilities("discord")},
		{ID: "other-workflow-gateway", Token: f.otherToken, Capabilities: connectorTestCapabilities("telegram")},
	})
	require.NoError(t, err)
	f.handler = newIntegrationServerWithStoreOptions(f.pool, []storage.Option{storage.WithBlobStore(f.blobs)},
		WithChannelConnectorAuthenticator(auth), WithInternalAPIOrigins([]string{"http://api:8080"}),
		WithPublicURL("https://omnara.example.test"))
	f.project.Store = integrationStoreForHandler(t, f.handler)
	profile := createPublicHTTPAgent(t, f.handler, f.project, "workflow-profile", f.project.AdminToken)
	profileID := mustPublicHTTPID(t, publicid.KindAgentProfile, channelReceiptString(t, profile, "id"))
	routeInput := integrationstore.CreateIntegrationRouteInput{
		ProjectID: f.install.ProjectID, IntegrationInstallID: f.install.ID, AgentProfileID: profileID,
		DeploymentKey: "conversation", BehaviorKey: "conversation", State: integrationstore.IntegrationRouteStateActive,
	}
	route, err := f.project.Store.Integrations().CreateIntegrationRoute(t.Context(), routeInput)
	require.NoError(t, err)
	f.routeID = testPublicID(t, publicid.KindIntegrationRoute, route.ID)
	definition := f.post(t, f.path(t, "channel-definitions/publish"),
		workflowHTTPJSON(t, workflowDefinitionRequest()), f.token, http.StatusOK)
	f.definitionID = channelReceiptString(t, definition, "id")
	return f
}

func workflowDefinitionRequest() openapi.PublishChannelConnectorDefinitionRequest {
	return openapi.PublishChannelConnectorDefinitionRequest{
		ImplementationKey: "conversation", Kind: openapi.ChannelKindDiscordThread, Description: "Current conversation",
		SendParamsSchema: json.RawMessage(`{"type":"object","properties":{"sequence":{"minimum":9007199254740993}}}`),
		Capabilities:     openapi.ChannelCapabilities{Read: true, Send: true, Text: true, Artifacts: true},
	}
}

func (f channelWorkflowHTTPFixture) path(t *testing.T, operation string) string {
	t.Helper()
	return "/api/v1/channel-connector/apps/" + testPublicID(t, publicid.KindIntegrationApp, f.app.ID) +
		"/installations/" + testPublicID(t, publicid.KindIntegrationInstall, f.install.ID) + "/" + operation
}

func workflowHTTPContent(t *testing.T, text, filename string) []openapi.CreateAgentInputContentBlock {
	t.Helper()
	raw := fmt.Sprintf(`[{"type":"text","text":%q},{"type":"media","media_type":"image/png",`+
		`"filename":%q,"data":%q,"metadata":{"caption":"retained"}}]`, text, filename,
		base64.StdEncoding.EncodeToString(testPNGBytes))
	var blocks []openapi.CreateAgentInputContentBlock
	require.NoError(t, json.Unmarshal([]byte(raw), &blocks))
	return blocks
}

func (f channelWorkflowHTTPFixture) delivery(
	t *testing.T, eventID string,
) openapi.DeliverChannelConnectorWorkflowRequest {
	t.Helper()
	f.post(t, f.eventPath(t, f.app), f.event(t, f.install, eventID, `{"text":"incoming"}`), f.token, http.StatusAccepted)
	claim := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, eventID, claim["event_id"])
	return openapi.DeliverChannelConnectorWorkflowRequest{
		RouteId: f.routeID, InstanceKey: "thread-one",
		InputKey: eventID,
		Receipt: openapi.ChannelEventLease{
			ReceiptId:       channelReceiptString(t, claim, "receipt_id"),
			LeaseToken:      uuid.MustParse(channelReceiptString(t, claim, "lease_token")),
			LeaseGeneration: int64(testutil.RequireType[float64](t, claim["lease_generation"])),
		},
		Target: openapi.ChannelRegistrationTarget{
			DefinitionId: f.definitionID, ProviderRef: "thread-one", ProviderRefKind: "thread",
			ProviderMetadata: json.RawMessage(`{"private_address":"connector only"}`),
		},
		Grants:        openapi.ChannelWorkflowGrants{Read: true, Send: true},
		Author:        openapi.ChannelWorkflowAuthor{Ref: "real-author", DisplayName: "Author"},
		ContentBlocks: workflowHTTPContent(t, "Original input", "original.png"),
		Metadata:      json.RawMessage(`{"sequence":9007199254740993}`),
	}
}

func (f channelWorkflowHTTPFixture) startDelivery(
	t *testing.T,
	body openapi.DeliverChannelConnectorWorkflowRequest,
) <-chan *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://api:8080"+f.path(t, "workflows/deliver"),
		strings.NewReader(workflowHTTPJSON(t, body)))
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		done <- response
	}()
	return done
}

func requireWorkflowHTTPResult(t *testing.T, done <-chan *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	select {
	case response := <-done:
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var result map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("workflow response did not complete")
		return nil
	}
}

func TestChannelConnectorDefinitionPublishCurrentContractAndScope(t *testing.T) {
	t.Parallel()
	f := newChannelWorkflowHTTPFixture(t)
	path := f.path(t, "channel-definitions/publish")
	body := workflowDefinitionRequest()
	body.Description = "Changed current contract"
	body.Capabilities.Read = false
	updated := f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusOK)
	require.Equal(t, f.definitionID, updated["id"])
	id := mustPublicHTTPID(t, publicid.KindChannelDefinition, f.definitionID)
	stored, err := f.project.Store.Integrations().GetChannelDefinition(t.Context(), f.install.ProjectID, f.install.ID, id)
	require.NoError(t, err)
	require.JSONEq(t, string(body.SendParamsSchema), string(stored.SendParamsSchema), "large integers must remain exact")
	require.False(t, stored.Capabilities.Read)
	require.Equal(t, body.Description, stored.Description)
	f.post(t, path, workflowHTTPJSON(t, body), "", http.StatusUnauthorized)
	f.post(t, path, workflowHTTPJSON(t, body), f.project.AdminToken, http.StatusForbidden)
	f.post(t, path, workflowHTTPJSON(t, body), f.otherToken, http.StatusNotFound)
	wrongApp := strings.Replace(path, testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
		testPublicID(t, publicid.KindIntegrationApp, f.otherApp.ID), 1)
	f.post(t, wrongApp, workflowHTTPJSON(t, body), f.token, http.StatusNotFound)
	body.Kind = openapi.ChannelKindSlackThread
	f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusBadRequest)
	body.Kind = openapi.ChannelKindDiscordThread
	body.SendParamsSchema = json.RawMessage(`{"type":"object","type":"string"}`)
	f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusBadRequest)
	body.SendParamsSchema = json.RawMessage(`{"type":"unsupported_schema_type"}`)
	f.post(t, path, workflowHTTPJSON(t, body), f.token, http.StatusBadRequest)
	stored, err = f.project.Store.Integrations().GetChannelDefinition(t.Context(), f.install.ProjectID, f.install.ID, id)
	require.NoError(t, err)
	require.Equal(t, "Changed current contract", stored.Description,
		"rejected publication cannot replace the current definition")
}

func TestChannelConnectorWorkflowUploadsBeforeLaunchAndReplaysCanonicalInput(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowHTTPFixture(t)
	body := f.delivery(t, "first")
	entered, release := make(chan struct{}), make(chan struct{})
	f.blobs.putHook = func(ctx context.Context, _ string, _ []byte) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := f.startDelivery(t, body)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("upload never began")
	}
	var agents, artifacts, workflows int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT
(SELECT count(*) FROM agents), (SELECT count(*) FROM artifacts), (SELECT count(*) FROM integration_workflows)`).
		Scan(&agents, &artifacts, &workflows))
	require.Zero(t, agents, "preparation accepts an agent identity that has not been persisted")
	require.Zero(t, artifacts)
	require.Zero(t, workflows)
	select {
	case <-done:
		t.Fatal("HTTP must not acknowledge before preparation and commit")
	default:
	}
	close(release)
	first := requireWorkflowHTTPResult(t, done)
	f.blobs.putHook = nil
	require.Equal(t, true, first["created_agent"])
	require.Equal(t, true, first["created_input"])
	inputID := mustPublicHTTPID(t, publicid.KindAgentInput, channelReceiptString(t, first, "agent_input_id"))
	agentID := mustPublicHTTPID(t, publicid.KindAgent, channelReceiptString(t, first, "agent_id"))
	var metadata string
	require.NoError(t, f.pool.QueryRow(ctx,
		`SELECT metadata::text FROM agent_inputs WHERE id = $1`, inputID).Scan(&metadata))
	require.JSONEq(t, string(body.Metadata), metadata)
	var artifactAgent uuid.UUID
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT agent_id FROM artifacts`).Scan(&artifactAgent))
	require.Equal(t, agentID, artifactAgent)
	require.Contains(t, workflowHTTPJSON(t, first["content_blocks"]), `"artifact_id":"art_`)
	require.NotContains(t, workflowHTTPJSON(t, first), "private_address")

	// Current replacement routing/content must not replace an immutable accepted
	// event. Fresh replay uploads may be prepared but never persisted.
	bindingID := mustPublicHTTPID(t, publicid.KindIntegrationBinding, channelReceiptString(t, first, "binding_id"))
	_, err := f.pool.Exec(ctx,
		`UPDATE integration_target_bindings SET revoked_at = statement_timestamp() WHERE id = $1`, bindingID)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx, `CREATE FUNCTION reject_workflow_replay_artifact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'replay must not persist prepared artifacts'; END $$;
CREATE TRIGGER reject_workflow_replay_artifact BEFORE INSERT ON artifacts
FOR EACH ROW EXECUTE FUNCTION reject_workflow_replay_artifact()`)
	require.NoError(t, err)
	body.Target.ProviderRef = "replacement-destination"
	body.Grants = openapi.ChannelWorkflowGrants{}
	body.ContentBlocks = workflowHTTPContent(t, "Changed incoming content", "changed.png")
	replayed := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, http.StatusOK)
	for _, field := range []string{"agent_id", "agent_input_id", "channel_id", "binding_id", "content_blocks"} {
		require.Equal(t, first[field], replayed[field], field)
	}
	require.Equal(t, false, replayed["created_agent"])
	require.Equal(t, false, replayed["created_input"])
	uploads, deletions, retained := f.blobs.snapshot()
	require.Len(t, uploads, 2)
	require.Equal(t, []string{uploads[1]}, deletions)
	require.Equal(t, 1, retained)
}

func TestChannelConnectorWorkflowRejectsUntrustedScopeAndStaleReceipt(t *testing.T) {
	f := newChannelWorkflowHTTPFixture(t)
	path := f.path(t, "workflows/deliver")
	body := f.delivery(t, "scoped")
	base := workflowHTTPJSON(t, body)
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"missing bearer", "", http.StatusUnauthorized},
		{"account token", f.project.AdminToken, http.StatusForbidden},
		{"wrong capability", f.otherToken, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) { f.post(t, path, base, tc.token, tc.status) })
	}
	for _, field := range []string{"project_id", "agent_id", "agent_profile_id"} {
		f.post(t, path, strings.TrimSuffix(base, "}")+`,"`+field+`":"untrusted"}`, f.token, http.StatusBadRequest)
	}
	wrongApp := strings.Replace(path, testPublicID(t, publicid.KindIntegrationApp, f.app.ID),
		testPublicID(t, publicid.KindIntegrationApp, f.otherApp.ID), 1)
	f.post(t, wrongApp, base, f.token, http.StatusNotFound)
	for _, tc := range []struct {
		name   string
		change func(*openapi.DeliverChannelConnectorWorkflowRequest)
		status int
	}{
		{"stale generation", func(b *openapi.DeliverChannelConnectorWorkflowRequest) { b.Receipt.LeaseGeneration++ }, 409},
		{"wrong lease token", func(b *openapi.DeliverChannelConnectorWorkflowRequest) {
			b.Receipt.LeaseToken = uuid.New()
		}, 409},
		{"oversized instance", func(b *openapi.DeliverChannelConnectorWorkflowRequest) {
			b.InstanceKey = strings.Repeat("x", 513)
		}, 400},
		{"empty author", func(b *openapi.DeliverChannelConnectorWorkflowRequest) { b.Author.Ref = " " }, 400},
		{"unknown route", func(b *openapi.DeliverChannelConnectorWorkflowRequest) {
			b.RouteId = testPublicID(t, publicid.KindIntegrationRoute, uuid.New())
		}, 404},
		{"raw definition UUID", func(b *openapi.DeliverChannelConnectorWorkflowRequest) {
			b.Target.DefinitionId = uuid.NewString()
		}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := body
			tc.change(&changed)
			f.post(t, path, workflowHTTPJSON(t, changed), f.token, tc.status)
		})
	}
	_, err := f.pool.Exec(t.Context(), `UPDATE integration_event_receipts
SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`,
		mustPublicHTTPID(t, publicid.KindIntegrationEventReceipt, body.Receipt.ReceiptId))
	require.NoError(t, err)
	f.post(t, path, base, f.token, http.StatusConflict)
	var agents, artifacts int
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT (SELECT count(*) FROM agents), (SELECT count(*) FROM artifacts)`).
		Scan(&agents, &artifacts))
	require.Zero(t, agents)
	require.Zero(t, artifacts)
	uploads, deletions, retained := f.blobs.snapshot()
	require.ElementsMatch(t, uploads, deletions)
	require.Zero(t, retained)
}

func TestChannelConnectorWorkflowCommitFailureRetainsUploadAndRollbackCleansIt(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"input insert", "commit"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChannelWorkflowHTTPFixture(t)
			body := f.delivery(t, "failed-input")
			_, err := f.pool.Exec(ctx, `CREATE FUNCTION reject_test_workflow_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'isolated workflow failure'; END $$`)
			require.NoError(t, err)
			trigger := `CREATE TRIGGER reject_test_workflow_input BEFORE INSERT ON agent_inputs
FOR EACH ROW EXECUTE FUNCTION reject_test_workflow_input()`
			if stage == "commit" {
				trigger = `CREATE CONSTRAINT TRIGGER reject_test_workflow_input AFTER INSERT ON agent_inputs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_test_workflow_input()`
			}
			_, err = f.pool.Exec(ctx, trigger)
			require.NoError(t, err)
			failed := f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, 500)
			require.NotContains(t, failed, "agent_input_id", "a failed commit cannot produce a successful delivery ACK")
			var agents, artifacts, workflows int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT
(SELECT count(*) FROM agents), (SELECT count(*) FROM artifacts), (SELECT count(*) FROM integration_workflows)`).
				Scan(&agents, &artifacts, &workflows))
			require.Zero(t, agents)
			require.Zero(t, artifacts)
			require.Zero(t, workflows)
			uploads, deletions, retained := f.blobs.snapshot()
			require.Len(t, uploads, 1)
			if stage == "commit" {
				require.Empty(t, deletions, "an ambiguous commit never permits compensation")
				require.Equal(t, 1, retained)
			} else {
				require.Equal(t, uploads, deletions)
				require.Zero(t, retained)
			}
		})
	}
}

func TestChannelConnectorWorkflowRejectsCrossInstallationResources(t *testing.T) {
	f := newChannelWorkflowHTTPFixture(t)
	ctx := t.Context()
	body := f.delivery(t, "own-event")
	definitionInput := integrationstore.PublishChannelDefinitionInput{
		ProjectID: f.otherInstall.ProjectID, IntegrationInstallID: f.otherInstall.ID,
		ImplementationKey: "other", Kind: integrationstore.ChannelKindDiscordThread,
		SendParamsSchema:      json.RawMessage(`{"type":"object"}`),
		ConnectorCapabilities: connectorTestCapabilities("discord"),
	}
	definition, err := f.project.Store.Integrations().PublishConnectorChannelDefinition(ctx, definitionInput)
	require.NoError(t, err)
	route, err := f.project.Store.Integrations().CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
		ProjectID: f.otherInstall.ProjectID, IntegrationInstallID: f.otherInstall.ID,
		DeploymentKey: "other", BehaviorKey: "conversation", State: integrationstore.IntegrationRouteStateActive,
	})
	require.NoError(t, err)
	f.post(t, f.eventPath(t, f.app), f.event(t, f.otherInstall, "foreign-event", `{}`), f.token, http.StatusAccepted)
	claim := f.post(t, channelReceiptClaimPath, channelReceiptClaimBody, f.token, http.StatusOK)
	require.Equal(t, "foreign-event", claim["event_id"])
	for _, field := range []string{"definition", "route", "receipt"} {
		t.Run(field, func(t *testing.T) {
			changed := body
			status := http.StatusNotFound
			switch field {
			case "definition":
				changed.Target.DefinitionId = testPublicID(t, publicid.KindChannelDefinition, definition.ID)
			case "route":
				changed.RouteId = testPublicID(t, publicid.KindIntegrationRoute, route.ID)
			case "receipt":
				changed.Receipt = openapi.ChannelEventLease{
					ReceiptId:       channelReceiptString(t, claim, "receipt_id"),
					LeaseToken:      uuid.MustParse(channelReceiptString(t, claim, "lease_token")),
					LeaseGeneration: int64(testutil.RequireType[float64](t, claim["lease_generation"])),
				}
				status = http.StatusConflict
			}
			f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, changed), f.token, status)
		})
	}
	for _, operation := range []string{"workflows/deliver", "channel-definitions/publish"} {
		request := httptest.NewRequest(http.MethodPost, "https://untrusted.example"+f.path(t, operation),
			strings.NewReader(workflowHTTPJSON(t, body)))
		request.Header.Set("Authorization", "Bearer "+f.token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		require.Equal(t, http.StatusNotFound, response.Code, "connector operations require the configured internal origin")
	}
	var agents, artifacts int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agents), (SELECT count(*) FROM artifacts)`).
		Scan(&agents, &artifacts))
	require.Zero(t, agents)
	require.Zero(t, artifacts)
	uploads, deletions, retained := f.blobs.snapshot()
	require.ElementsMatch(t, uploads, deletions)
	require.Zero(t, retained)
}

func TestChannelConnectorWorkflowCompetingFirstEventsRetryPreparationForWinner(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newChannelWorkflowHTTPFixture(t)
	first, second := f.delivery(t, "first"), f.delivery(t, "second")
	blocker := integrationdb.BeginTx(t, ctx, f.pool)
	_, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
$1::uuid::text || ':' || $2::uuid::text || ':' || $3::uuid::text || ':' || $4::text, 0))`,
		f.install.ProjectID, f.install.ID, mustPublicHTTPID(t, publicid.KindIntegrationRoute, f.routeID), first.InstanceKey)
	require.NoError(t, err)
	left, right := f.startDelivery(t, first), f.startDelivery(t, second)
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.pool, "LockIntegrationWorkflowIdentity", 2)
	uploads, _, retained := f.blobs.snapshot()
	require.Len(t, uploads, 2, "both requests must prepare uploads before contending on workflow admission")
	require.Equal(t, 2, retained)
	require.NoError(t, blocker.Rollback(ctx))
	leftResult, rightResult := requireWorkflowHTTPResult(t, left), requireWorkflowHTTPResult(t, right)
	require.Equal(t, leftResult["agent_id"], rightResult["agent_id"])
	require.NotEqual(t, leftResult["agent_input_id"], rightResult["agent_input_id"])
	require.NotEqual(t, leftResult["created_agent"], rightResult["created_agent"])
	uploads, deletions, retained := f.blobs.snapshot()
	require.Len(t, uploads, 3, "only the losing request prepares once more for the durable winner")
	require.Len(t, deletions, 1)
	require.Equal(t, 2, retained)
	var agents, artifacts int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agents), (SELECT count(*) FROM artifacts)`).
		Scan(&agents, &artifacts))
	require.Equal(t, 1, agents)
	require.Equal(t, 2, artifacts)
}

func TestChannelConnectorWorkflowPartialUploadFailureNeverLaunches(t *testing.T) {
	t.Parallel()
	f := newChannelWorkflowHTTPFixture(t)
	body := f.delivery(t, "upload-fails")
	body.ContentBlocks = append(body.ContentBlocks, body.ContentBlocks[1])
	puts := 0
	f.blobs.putHook = func(context.Context, string, []byte) error {
		puts++
		if puts == 2 {
			return errors.New("isolated upload failure")
		}
		return nil
	}
	f.post(t, f.path(t, "workflows/deliver"), workflowHTTPJSON(t, body), f.token, 500)
	var agents int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agents`).Scan(&agents))
	require.Zero(t, agents)
	uploads, deletions, retained := f.blobs.snapshot()
	require.Len(t, uploads, 1)
	require.Equal(t, uploads, deletions)
	require.Zero(t, retained)
}

func workflowHTTPJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}
