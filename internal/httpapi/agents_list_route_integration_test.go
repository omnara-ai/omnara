//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestListAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)

	project := bootstrapPublicHTTPProject(t, handler, "list-agents")
	configResp := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"list-agents",
		"yaml",
		"instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken,
		http.StatusCreated,
	)
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, configResp["id"]))

	const agentCount = 5
	wantOrder := make([]string, 0, agentCount)
	agents := make([]executionstore.AgentRecord, 0, agentCount)
	var base time.Time
	for i := range agentCount {
		name := "Agent " + string(rune('A'+i))
		idempotencyKey := "list-agents-" + string(rune('a'+i))
		var agent executionstore.AgentRecord
		if i == 0 {
			var err error
			agent, err = store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
				ProjectID:       project.ProjectUUID,
				Name:            name,
				CurrentConfigID: configID,
			})
			if err != nil {
				t.Fatalf("seed agent %d: %v", i, err)
			}
			base = agent.CreatedAt
		} else {
			createdAt := base.Add(time.Duration(i) * time.Minute)
			if i == 3 {
				createdAt = base.Add(2 * time.Minute)
			}
			agent = seedListAgentAt(
				t,
				ctx,
				pool,
				store,
				project,
				configID,
				name,
				idempotencyKey,
				createdAt,
			)
		}
		agents = append(agents, agent)
	}
	sortedAgents := append([]executionstore.AgentRecord(nil), agents...)
	sort.Slice(sortedAgents, func(i, j int) bool {
		if !sortedAgents[i].CreatedAt.Equal(sortedAgents[j].CreatedAt) {
			return sortedAgents[i].CreatedAt.After(sortedAgents[j].CreatedAt)
		}
		return bytes.Compare(sortedAgents[i].ID[:], sortedAgents[j].ID[:]) > 0
	})
	for _, agent := range sortedAgents {
		publicAgentID, err := publicid.Encode(publicid.KindAgent, agent.ID)
		if err != nil {
			t.Fatalf("encode agent id: %v", err)
		}
		wantOrder = append(wantOrder, publicAgentID)
	}

	slackTargetAgentID := seedListAgentsSlackTarget(
		t,
		ctx,
		handler,
		store,
		pool,
		project,
		agents[1],
		base.Add(2*time.Hour),
	)

	agentsPath := project.ProjectPath + "/agents"

	got := make([]string, 0, agentCount)
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := agentsPath + "?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			path,
			"",
			"",
			http.StatusOK,
			authHeaders(project.AdminToken),
		)
		data := testutil.RequireType[[]any](t, page["data"])
		if len(data) > 2 {
			t.Fatalf("page returned %d items, want <= limit 2", len(data))
		}
		for _, raw := range data {
			row := testutil.RequireType[map[string]any](t, raw)
			id := testutil.RequireType[string](t, row["id"])
			if _, ok := row["name"].(string); !ok {
				t.Fatalf("list agent missing required name: %+v", row)
			}
			model, ok := row["model"].(map[string]any)
			if !ok {
				t.Fatalf("list agent missing model: %+v", row)
			}
			if model["provider_config"] != "openai-prod" || model["name"] != "gpt-test" {
				t.Fatalf("list agent model = %+v, want openai-prod/gpt-test", model)
			}
			if seen[id] {
				t.Fatalf("cursor paging returned duplicate agent %s", id)
			}
			if id == slackTargetAgentID {
				assertListAgentsIntegrationTarget(
					t,
					row,
					"slack",
					"C0BAK8REEGY:1783382417.000100",
					"thread",
					"agent-testing",
					"https://slack.com/app_redirect?channel=C0BAK8REEGY&team=TLISTAGENTS",
				)
			}
			seen[id] = true
			got = append(got, id)
		}
		pages++
		if pages > agentCount+2 {
			t.Fatalf("pagination did not terminate; got=%v", got)
		}
		next, ok := page["next_cursor"]
		if !ok {
			t.Fatalf("response missing next_cursor: %+v", page)
		}
		if next == nil {
			break
		}
		cursor = testutil.RequireType[string](t, next)
	}

	if len(got) != agentCount {
		t.Fatalf("paged %d agents, want %d: %v", len(got), agentCount, got)
	}
	for i := range wantOrder {
		if got[i] != wantOrder[i] {
			t.Fatalf(
				"order mismatch at %d: got %s want %s (full got=%v want=%v)",
				i,
				got[i],
				wantOrder[i],
				got,
				wantOrder,
			)
		}
	}

	full := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		agentsPath+"?limit=100",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if full["next_cursor"] != nil {
		t.Fatalf("single full page should have null next_cursor, got %v", full["next_cursor"])
	}
	if data := testutil.RequireType[[]any](t, full["data"]); len(data) != agentCount {
		t.Fatalf("full page returned %d agents, want %d", len(data), agentCount)
	}

	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		agentsPath+"?cursor=not-a-valid-cursor",
		"",
		"",
		http.StatusBadRequest,
		authHeaders(project.AdminToken),
	)

	firstPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		agentsPath+"?limit=1",
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	topLevelCursor := testutil.RequireType[string](t, firstPage["next_cursor"])
	parentAgentID, err := publicid.Encode(publicid.KindAgent, agents[0].ID)
	if err != nil {
		t.Fatalf("encode parent agent id: %v", err)
	}
	for _, query := range []string{
		"?include_subagents=true&cursor=",
		"?parent_agent_id=" + parentAgentID + "&cursor=",
		"?agent_profile_id=aprf_abcdefghijklmnopqrstuvwxyz&cursor=",
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			agentsPath+query+topLevelCursor,
			"",
			"",
			http.StatusBadRequest,
			authHeaders(project.AdminToken),
		)
	}

	otherOrg := bootstrapPublicHTTPProject(t, handler, "list-agents-other-org")
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		agentsPath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(otherOrg.AdminToken),
	)
}

func TestListAgentsChildrenWithActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool)
	store := integrationStoreForHandler(t, handler)
	project := bootstrapPublicHTTPProject(t, handler, "list-agent-children")
	parentLaunch := createHTTPRuntimeAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, "list-agent-children",
	)
	parent := parentLaunch.Agent
	active := spawnHTTPSubagentForTest(t, ctx, store, parent, parentLaunch.AgentConfig.ID, "active-child", "worker")
	archived := spawnHTTPSubagentForTest(t, ctx, store, parent, parentLaunch.AgentConfig.ID, "archived-child", "worker")
	if _, _, err := store.Execution().ArchiveAgent(
		ctx, project.ProjectUUID, archived.ID, httpUserPrincipal(project.AdminUserUUID),
	); err != nil {
		t.Fatalf("archive child: %v", err)
	}
	parentPublicID := testPublicID(t, publicid.KindAgent, parent.ID)
	activePublicID := testPublicID(t, publicid.KindAgent, active.ID)
	archivedPublicID := testPublicID(t, publicid.KindAgent, archived.ID)
	childrenPath := project.ProjectPath + "/agents?sort=created_at&parent_agent_id=" + parentPublicID

	listed := requestJSONWithHeaders(
		t, handler, http.MethodGet, childrenPath, "", "", http.StatusOK, authHeaders(project.AdminToken),
	)
	rows := testutil.RequireType[[]any](t, listed["data"])
	if len(rows) != 1 {
		t.Fatalf("children without archived = %+v, want the active child only", rows)
	}
	activeRow := testutil.RequireType[map[string]any](t, rows[0])
	activity := testutil.RequireType[map[string]any](t, activeRow["activity"])
	if activeRow["id"] != activePublicID || activeRow["subagent_key"] != "worker" ||
		activeRow["parent_agent_id"] != parentPublicID || activity["state"] != "idle" {
		t.Fatalf("active child row = %+v, want an idle worker under the parent", activeRow)
	}
	if _, ok := activity["last_activity_at"].(string); !ok {
		t.Fatalf("active child activity = %+v, want last_activity_at", activity)
	}

	withArchived := requestJSONWithHeaders(
		t, handler, http.MethodGet, childrenPath+"&include_archived=true", "", "", http.StatusOK,
		authHeaders(project.AdminToken),
	)
	rows = testutil.RequireType[[]any](t, withArchived["data"])
	if len(rows) != 2 {
		t.Fatalf("children with archived = %+v, want both children", rows)
	}
	archivedRow := testutil.RequireType[map[string]any](t, rows[1])
	archivedActivity := testutil.RequireType[map[string]any](t, archivedRow["activity"])
	if archivedRow["id"] != archivedPublicID || archivedRow["state"] != "archived" ||
		archivedActivity["state"] != "archived" {
		t.Fatalf("archived child row = %+v, want an archived worker", archivedRow)
	}

	detail := requestJSONWithHeaders(
		t, handler, http.MethodGet, project.ProjectPath+"/agents/"+parentPublicID, "", "", http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if _, ok := detail["subagents"]; ok {
		t.Fatalf("get agent still embeds subagents: %+v", detail)
	}
	if _, ok := testutil.RequireType[map[string]any](t, detail["agent"])["activity"]; ok {
		t.Fatalf("get agent reports activity: %+v", detail)
	}
}

func TestListAgentsByProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)

	handler := newIntegrationServer(pool)
	project := bootstrapPublicHTTPProject(t, handler, "list-agents-by-profile")
	config := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"list-agents-by-profile",
		"yaml",
		"instruction: Help.\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
		project.AdminToken,
		http.StatusCreated,
	)
	configID := testutil.RequireType[string](t, config["id"])
	firstProfile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"list-agents-by-profile-first",
		"First Profile",
		configID,
		project.AdminToken,
		http.StatusCreated,
	)
	secondProfile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"list-agents-by-profile-second",
		"Second Profile",
		configID,
		project.AdminToken,
		http.StatusCreated,
	)
	firstProfileID := testutil.RequireType[string](t, firstProfile["id"])
	secondProfileID := testutil.RequireType[string](t, secondProfile["id"])

	profileLaunch := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"profile":"`+firstProfileID+`","config":"`+configID+`"}`,
		"idem-list-agents-by-profile-first-launch",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	profileAgent := testutil.RequireType[map[string]any](t, profileLaunch["agent"])
	if got := profileAgent["agent_profile_id"]; got != firstProfileID {
		t.Fatalf("launched agent agent_profile_id = %v, want %s", got, firstProfileID)
	}

	configLaunch := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		`{"config":"`+configID+`"}`,
		"idem-list-agents-by-profile-config-launch",
		http.StatusCreated,
		authHeaders(project.AdminToken),
	)
	configAgent := testutil.RequireType[map[string]any](t, configLaunch["agent"])
	if _, ok := configAgent["agent_profile_id"]; ok {
		t.Fatalf("config-only agent unexpectedly has agent_profile_id: %+v", configAgent)
	}

	firstPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents?agent_profile_id="+firstProfileID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	firstData := testutil.RequireType[[]any](t, firstPage["data"])
	if len(firstData) != 1 {
		t.Fatalf("first profile returned %d agents, want 1", len(firstData))
	}
	if got := testutil.RequireType[map[string]any](t, firstData[0])["id"]; got != profileAgent["id"] {
		t.Fatalf("first profile agent = %v, want %v", got, profileAgent["id"])
	}

	secondPage := requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		project.ProjectPath+"/agents?agent_profile_id="+secondProfileID,
		"",
		"",
		http.StatusOK,
		authHeaders(project.AdminToken),
	)
	if data := testutil.RequireType[[]any](t, secondPage["data"]); len(data) != 0 {
		t.Fatalf("second profile returned %d agents, want 0", len(data))
	}
}

func seedListAgentAt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	store *storage.Store,
	project publicHTTPProject,
	configID uuid.UUID,
	name string,
	idempotencyKey string,
	createdAt time.Time,
) executionstore.AgentRecord {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO agents (
    org_id, project_id, state, name, current_config_id,
    idempotency_key, created_at, updated_at
)
VALUES ($1, $2, 'active', $3, $4, $5, $6, $6)
RETURNING id
`, project.OrgUUID, project.ProjectUUID, name, configID, idempotencyKey, createdAt).Scan(&id); err != nil {
		t.Fatalf("seed agent at creation time: %v", err)
	}
	record, err := store.Execution().GetAgentInProject(ctx, project.ProjectUUID, id)
	if err != nil {
		t.Fatalf("load agent seeded at creation time: %v", err)
	}
	return record
}

func seedListAgentsSlackTarget(
	t *testing.T,
	ctx context.Context,
	handler http.Handler,
	store *storage.Store,
	pool *pgxpool.Pool,
	project publicHTTPProject,
	agent executionstore.AgentRecord,
	now time.Time,
) string {
	t.Helper()
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"list-agents-target",
		project.AdminToken,
	)
	profileID := mustPublicHTTPID(t, publicid.KindAgentProfile, testutil.RequireType[string](t, profile["id"]))
	install := createSlackHTTPInstall(
		t,
		ctx,
		project,
		profileID,
		"ALISTAGENTS",
		"TLISTAGENTS",
		"ULISTAGENTSBOT",
		"signing-secret-list-agents",
	)
	require.Equal(t, integrationdefinition.SlackThread, install.IntegrationType)
	base, found, err := store.Execution().GetAgentConfig(ctx, project.ProjectUUID, agent.CurrentConfigID)
	require.NoError(t, err)
	require.True(t, found)
	derived, err := agentconfigcompile.DeriveIntegrationConfig(ctx, store, project.OrgUUID, project.ProjectUUID,
		agentconfig.CompileOptions{}, base, agentconfig.IntegrationCapabilitiesSource{
			InteractionHandlers: map[string]agentconfig.AgentConfigIntegrationCapabilitySource{
				install.Name: {},
			},
		})
	require.NoError(t, err)
	_, err = store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: derived.CreateInput(project.ProjectUUID),
		AgentID:                agent.ID, ExpectedCurrentConfigID: base.ID, ActorType: "user", ActorID: project.AdminUserUUID,
		IdempotencyKey: "list-agents-handler",
	})
	require.NoError(t, err)
	_, _, err = store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: project.ProjectUUID, IntegrationID: install.ID,
		ReceiptKey: "list-origin", Payload: []byte(`{"verified":true}`),
	})
	require.NoError(t, err)
	receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: project.ProjectUUID, IntegrationID: install.ID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	actor, err := executionstore.IntegrationActorParams(install.ID, "ULISTINPUT", nil)
	require.NoError(t, err)
	plan, err := json.Marshal(map[string]executionstore.InboxInputSlot{"recipient": {
		AgentID: agent.ID, Input: executionstore.CreateAgentContentInputInput{
			Origin: &executionstore.AgentInputOrigin{
				IntegrationID: install.ID,
				Address: integrationstore.ConversationAddress{
					Kind: "thread",
					Ref:  "C0BAK8REEGY:1783382417.000100",
				},
			},
			Actor:         &actor,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"list origin"}]`), IdempotencyKey: "list-origin",
		},
	}})
	require.NoError(t, err)
	require.NoError(t, store.Integrations().WithIntegrationInboxLease(ctx, receipt.Lease(),
		func(w *integrationstore.IntegrationInboxLeaseTx) error { return w.FreezePlan(ctx, plan) }))
	_, err = store.Execution().AdmitInboxInputSlot(ctx, receipt.Lease(), "recipient", nil)
	require.NoError(t, err)
	if err := store.Integrations().UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
		ctx,
		project.ProjectUUID,
		install.ID,
		"C0BAK8REEGY",
		"agent-testing",
	); err != nil {
		t.Fatalf("seed Slack conversation display name: %v", err)
	}
	publicAgentID, err := publicid.Encode(publicid.KindAgent, agent.ID)
	if err != nil {
		t.Fatalf("encode Slack target agent id: %v", err)
	}
	return publicAgentID
}

func assertListAgentsIntegrationTarget(
	t *testing.T,
	row map[string]any,
	provider string,
	providerRef string,
	refKind string,
	displayName string,
	providerURI string,
) {
	t.Helper()
	raw, ok := row["integration_target"]
	if !ok {
		t.Fatalf("list agent missing integration target: %+v", row)
	}
	target, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("integration target has unexpected shape: %+v", raw)
	}
	// Released clients still interpret this field as a transport, not an integration type.
	if got := target["provider"]; got != provider {
		t.Fatalf("integration target provider = %v, want %q", got, provider)
	}
	if got := target["provider_ref"]; got != providerRef {
		t.Fatalf("integration target provider_ref = %v, want %q", got, providerRef)
	}
	if got := target["provider_ref_kind"]; got != refKind {
		t.Fatalf("integration target provider_ref_kind = %v, want %q", got, refKind)
	}
	if got := target["display_name"]; got != displayName {
		t.Fatalf("integration target display_name = %v, want %q", got, displayName)
	}
	if got := target["provider_uri"]; got != providerURI {
		t.Fatalf("integration target provider_uri = %v, want %q", got, providerURI)
	}
}
