//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func setOrgResourceLimitOverrides(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	overrides map[string]int64,
) {
	t.Helper()
	if len(overrides) == 0 {
		if _, err := pool.Exec(
			ctx,
			`DELETE FROM org_resource_limit_overrides WHERE org_id = $1`,
			testOrgID,
		); err != nil {
			t.Fatalf("clear resource limit overrides: %v", err)
		}
		return
	}
	value := func(key string) *int64 {
		limit, ok := overrides[key]
		if !ok {
			return nil
		}
		return &limit
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO org_resource_limit_overrides (
    org_id,
    max_active_projects_per_org,
    max_pending_org_invitations_per_org,
    max_active_org_api_keys_per_org,
    max_active_tenant_model_provider_configs_per_org,
    max_active_configured_models_per_provider,
    max_agent_configs_per_project,
    max_active_agent_profiles_per_project,
    max_active_agents_per_project,
    max_active_tenant_secrets_per_owner,
    max_active_skills_per_owner,
    max_active_tenant_machine_pools_per_org,
    max_live_machines_per_org,
    max_active_byo_daemon_tokens_per_machine,
    max_non_terminal_processes_per_agent
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
)
ON CONFLICT (org_id) DO UPDATE SET
    max_active_projects_per_org = EXCLUDED.max_active_projects_per_org,
    max_pending_org_invitations_per_org = EXCLUDED.max_pending_org_invitations_per_org,
    max_active_org_api_keys_per_org = EXCLUDED.max_active_org_api_keys_per_org,
    max_active_tenant_model_provider_configs_per_org = EXCLUDED.max_active_tenant_model_provider_configs_per_org,
    max_active_configured_models_per_provider = EXCLUDED.max_active_configured_models_per_provider,
    max_agent_configs_per_project = EXCLUDED.max_agent_configs_per_project,
    max_active_agent_profiles_per_project = EXCLUDED.max_active_agent_profiles_per_project,
    max_active_agents_per_project = EXCLUDED.max_active_agents_per_project,
    max_active_tenant_secrets_per_owner = EXCLUDED.max_active_tenant_secrets_per_owner,
    max_active_skills_per_owner = EXCLUDED.max_active_skills_per_owner,
    max_active_tenant_machine_pools_per_org = EXCLUDED.max_active_tenant_machine_pools_per_org,
    max_live_machines_per_org = EXCLUDED.max_live_machines_per_org,
    max_active_byo_daemon_tokens_per_machine = EXCLUDED.max_active_byo_daemon_tokens_per_machine,
    max_non_terminal_processes_per_agent = EXCLUDED.max_non_terminal_processes_per_agent`,
		testOrgID,
		value("max_active_projects_per_org"),
		value("max_pending_org_invitations_per_org"),
		value("max_active_org_api_keys_per_org"),
		value("max_active_tenant_model_provider_configs_per_org"),
		value("max_active_configured_models_per_provider"),
		value("max_agent_configs_per_project"),
		value("max_active_agent_profiles_per_project"),
		value("max_active_agents_per_project"),
		value("max_active_tenant_secrets_per_owner"),
		value("max_active_skills_per_owner"),
		value("max_active_tenant_machine_pools_per_org"),
		value("max_live_machines_per_org"),
		value("max_active_byo_daemon_tokens_per_machine"),
		value("max_non_terminal_processes_per_agent"),
	); err != nil {
		t.Fatalf("set resource limit overrides: %v", err)
	}
}

func TestOrgResourceLimitOverridesResolveAndValidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	q := dbsqlc.New(pool)

	limits, err := resourceguard.ResolveLimits(ctx, q, testOrgID)
	if err != nil {
		t.Fatalf("resolve default resource limits: %v", err)
	}
	wantDefaults := dbsqlc.EffectiveResourceLimit{
		OrgID:                                     testOrgID,
		MaxActiveProjectsPerOrg:                   1_000,
		MaxPendingOrgInvitationsPerOrg:            10_000,
		MaxActiveOrgApiKeysPerOrg:                 10_000,
		MaxActiveTenantModelProviderConfigsPerOrg: 10_000,
		MaxActiveConfiguredModelsPerProvider:      10_000,
		MaxAgentConfigsPerProject:                 10_000,
		MaxActiveAgentProfilesPerProject:          10_000,
		MaxActiveAgentsPerProject:                 10_000,
		MaxActiveTenantSecretsPerOwner:            10_000,
		MaxActiveSkillsPerOwner:                   10_000,
		MaxActiveTenantMachinePoolsPerOrg:         10_000,
		MaxLiveMachinesPerOrg:                     10_000,
		MaxActiveByoDaemonTokensPerMachine:        20,
		MaxNonTerminalProcessesPerAgent:           32,
	}
	if limits != wantDefaults {
		t.Fatalf("default resource limits = %+v, want %+v", limits, wantDefaults)
	}

	overrides := map[string]int64{
		"max_active_projects_per_org":                      0,
		"max_pending_org_invitations_per_org":              2,
		"max_active_org_api_keys_per_org":                  3,
		"max_active_tenant_model_provider_configs_per_org": 4,
		"max_active_configured_models_per_provider":        5,
		"max_agent_configs_per_project":                    6,
		"max_active_agent_profiles_per_project":            7,
		"max_active_agents_per_project":                    8,
		"max_active_tenant_secrets_per_owner":              9,
		"max_active_skills_per_owner":                      10,
		"max_active_tenant_machine_pools_per_org":          11,
		"max_live_machines_per_org":                        12,
		"max_active_byo_daemon_tokens_per_machine":         13,
		"max_non_terminal_processes_per_agent":             42,
	}
	setOrgResourceLimitOverrides(t, ctx, pool, overrides)
	limits, err = resourceguard.ResolveLimits(ctx, q, testOrgID)
	if err != nil {
		t.Fatalf("resolve overridden resource limits: %v", err)
	}
	wantOverrides := dbsqlc.EffectiveResourceLimit{
		OrgID:                                     testOrgID,
		MaxActiveProjectsPerOrg:                   0,
		MaxPendingOrgInvitationsPerOrg:            2,
		MaxActiveOrgApiKeysPerOrg:                 3,
		MaxActiveTenantModelProviderConfigsPerOrg: 4,
		MaxActiveConfiguredModelsPerProvider:      5,
		MaxAgentConfigsPerProject:                 6,
		MaxActiveAgentProfilesPerProject:          7,
		MaxActiveAgentsPerProject:                 8,
		MaxActiveTenantSecretsPerOwner:            9,
		MaxActiveSkillsPerOwner:                   10,
		MaxActiveTenantMachinePoolsPerOrg:         11,
		MaxLiveMachinesPerOrg:                     12,
		MaxActiveByoDaemonTokensPerMachine:        13,
		MaxNonTerminalProcessesPerAgent:           42,
	}
	if limits != wantOverrides {
		t.Fatalf("overridden resource limits = %+v, want %+v", limits, wantOverrides)
	}

	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{})
	limits, err = resourceguard.ResolveLimits(ctx, q, testOrgID)
	if err != nil {
		t.Fatalf("resolve resource limits after removing overrides: %v", err)
	}
	if limits != wantDefaults {
		t.Fatalf("resource limits after removing overrides = %+v, want %+v", limits, wantDefaults)
	}

	for name, value := range map[string]string{
		"negative":  "-1",
		"too large": "9223372036854775808",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pool.Exec(
				ctx,
				`INSERT INTO org_resource_limit_overrides (
    org_id,
    max_active_projects_per_org
) VALUES ($1, $2::numeric)`,
				testOrgID,
				value,
			); err == nil {
				t.Fatalf("invalid resource limit override %s was accepted", value)
			}
		})
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE orgs SET deleted_at = now() WHERE id = $1`,
		testOrgID,
	); err != nil {
		t.Fatalf("soft-delete organization: %v", err)
	}
	if _, err := resourceguard.ResolveLimits(ctx, q, testOrgID); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("resolve deleted organization resource limits error = %v, want ErrNotFound", err)
	}
}

func TestProjectResourceLimitSerializesConcurrentCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	const projectLimit = int64(3)
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_active_projects_per_org": projectLimit,
	})
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	creator := mustCreateIdentityUser(t, ctx, store, "limited-projects@example.com", "Limited Projects")
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID: testOrgID, UserID: creator.ID, Role: "member",
	}); err != nil {
		t.Fatalf("add project creator membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO projects(org_id, name, idempotency_key, created_at, updated_at)
SELECT $1, 'Limit seed project ' || n, 'limit-seed-project-' || n, $2, $2
FROM generate_series(1, $3::integer) AS n
`, testOrgID, now, projectLimit-2); err != nil {
		t.Fatalf("seed projects to limit: %v", err)
	}
	inputs := []identitystore.CreateProjectForPrincipalInput{
		{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Concurrent Project A",
			IdempotencyKey: "concurrent-project-a",
		},
		{
			OrgID:          testOrgID,
			Creator:        userPrincipal(creator.ID),
			Name:           "Concurrent Project B",
			IdempotencyKey: "concurrent-project-b",
		},
	}
	type result struct {
		index  int
		record identitystore.ProjectRecord
		err    error
	}
	results := make(chan result, len(inputs))
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(len(inputs))
	for index, input := range inputs {
		go func() {
			ready.Done()
			<-start
			record, err := store.Identity().CreateProjectForPrincipal(ctx, input)
			results <- result{index: index, record: record, err: err}
		}()
	}
	ready.Wait()
	close(start)

	var created result
	var createdCount, conflictCount int
	for range inputs {
		got := <-results
		switch {
		case got.err == nil:
			created = got
			createdCount++
		case errors.Is(got.err, storeerr.ErrConflict):
			conflictCount++
		default:
			t.Fatalf("concurrent project create error = %v", got.err)
		}
	}
	if createdCount != 1 || conflictCount != 1 {
		t.Fatalf("concurrent creates succeeded=%d conflicted=%d, want 1 and 1", createdCount, conflictCount)
	}
	count, err := testQueries(store).CountActiveProjectsForOrg(
		ctx,
		dbsqlc.CountActiveProjectsForOrgParams{OrgID: testOrgID},
	)
	if err != nil {
		t.Fatalf("count active projects: %v", err)
	}
	if count != projectLimit {
		t.Fatalf("active project count = %d, want %d", count, projectLimit)
	}
	replayed, err := store.Identity().CreateProjectForPrincipal(ctx, inputs[created.index])
	if err != nil {
		t.Fatalf("replay project at limit: %v", err)
	}
	if replayed.Created || replayed.ID != created.record.ID {
		t.Fatalf("unexpected project replay: %+v", replayed)
	}
}

func TestIdentityResourceLimitsRollBackAndReleaseCapacity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := newIntegrationStore(pool)
	seedDefaultProject(t, ctx, store)
	const invitationLimit = int64(2)
	const orgAPIKeyLimit = int64(2)
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_pending_org_invitations_per_org": invitationLimit,
		"max_active_org_api_keys_per_org":     orgAPIKeyLimit,
	})
	now := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)

	if _, err := pool.Exec(ctx, `
INSERT INTO org_invitations(org_id, email, normalized_email, org_role, created_at)
SELECT $1, 'limited-invite-' || n || '@example.com',
       'limited-invite-' || n || '@example.com', 'member', $2
FROM generate_series(1, $3::integer) AS n
`, testOrgID, now, invitationLimit); err != nil {
		t.Fatalf("seed invitations to limit: %v", err)
	}
	if _, err := store.Identity().CreateOrgInvitation(ctx, identitystore.CreateOrgInvitationInput{
		OrgID: testOrgID, Email: "limited-invite@example.com", Role: "member",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("invitation over limit error = %v, want ErrConflict", err)
	}
	var invitationCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM org_invitations WHERE org_id = $1`, testOrgID).
		Scan(&invitationCount); err != nil {
		t.Fatalf("count invitations: %v", err)
	}
	if invitationCount != invitationLimit {
		t.Fatalf(
			"invitation count after rollback = %d, want %d",
			invitationCount,
			invitationLimit,
		)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO personal_access_tokens(user_id, name, token_id, token_hash, created_at)
SELECT $1, 'Pre-canonical limit token ' || n, 'legacy-token-id-' || n,
       'legacy-token-hash-' || n, $2
FROM generate_series(1, $3::integer) AS n
`, testDefaultProviderAdminUserID, now, identitystore.MaxActivePersonalAccessTokensPerUser-1); err != nil {
		t.Fatalf("seed personal access tokens to below limit: %v", err)
	}
	first, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(
		ctx,
		identitystore.CreatePersonalAccessTokenInput{
			UserID: testDefaultProviderAdminUserID,
			Name:   "First limited token",
		},
	)
	if err != nil {
		t.Fatalf("create first personal access token: %v", err)
	}
	secondInput := identitystore.CreatePersonalAccessTokenInput{
		UserID: testDefaultProviderAdminUserID,
		Name:   "Second limited token",
	}
	if _, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, secondInput); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("personal access token over limit error = %v, want ErrConflict", err)
	}
	if _, err := store.Identity().RevokePersonalAccessToken(
		ctx,
		testDefaultProviderAdminUserID,
		first.Record.ID,
	); err != nil {
		t.Fatalf("revoke first personal access token: %v", err)
	}
	second, err := store.Identity().CreatePersonalAccessTokenWithPlaintext(ctx, secondInput)
	if err != nil {
		t.Fatalf("create personal access token after revoke: %v", err)
	}
	session, err := store.Identity().CreateBrowserSession(ctx, identitystore.CreateBrowserSessionInput{
		UserID:    testDefaultProviderAdminUserID,
		Token:     "resource-limit-device-session",
		CSRFToken: "resource-limit-device-csrf",
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	flow, err := store.Identity().StartDeviceAuthFlow(ctx, identitystore.StartDeviceAuthFlowInput{
		ClientName: "Resource limit test",
		TokenName:  "Limited device token",
	})
	if err != nil {
		t.Fatalf("start device auth flow: %v", err)
	}
	if err := store.Identity().ApproveDeviceAuthFlow(ctx, identitystore.ApproveDeviceAuthFlowInput{
		UserCode:                 flow.UserCode,
		UserID:                   testDefaultProviderAdminUserID,
		ApprovedBrowserSessionID: session.ID,
	}); err != nil {
		t.Fatalf("approve device auth flow: %v", err)
	}
	if _, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode},
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("device personal access token over limit error = %v, want ErrConflict", err)
	}
	if _, err := store.Identity().RevokePersonalAccessToken(
		ctx,
		testDefaultProviderAdminUserID,
		second.Record.ID,
	); err != nil {
		t.Fatalf("revoke second personal access token: %v", err)
	}
	approved, err := store.Identity().PollDeviceAuthFlow(
		ctx,
		identitystore.DeviceAuthFlowPollInput{DeviceCode: flow.DeviceCode},
	)
	if err != nil {
		t.Fatalf("poll device auth flow after revoke: %v", err)
	}
	if approved.Status != identitystore.DeviceAuthFlowStatusApproved || approved.Token == "" {
		t.Fatalf("device auth flow after revoke = %+v, want approved token", approved)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO org_api_keys(org_id, name, token_id, token_hash, created_by_user_id, created_at, updated_at)
SELECT $1, 'Pre-canonical limit key ' || n, 'legacy-key-id-' || n,
       'legacy-key-hash-' || n, $2, $3, $3
FROM generate_series(1, $4::integer) AS n
`, testOrgID, testDefaultProviderAdminUserID, now, orgAPIKeyLimit-1); err != nil {
		t.Fatalf("seed org api keys to below limit: %v", err)
	}
	firstKey, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: testDefaultProviderAdminUserID,
		Name:            "First limited key",
		OrgRole:         "member",
	})
	if err != nil {
		t.Fatalf("create first org api key: %v", err)
	}
	secondKeyInput := identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: testDefaultProviderAdminUserID,
		Name:            "Second limited key",
		OrgRole:         "member",
	}
	if _, err := store.Identity().CreateOrgAPIKeyWithPlaintext(
		ctx,
		secondKeyInput,
	); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("org api key over limit error = %v, want ErrConflict", err)
	}
	if _, err := store.Identity().RevokeOrgAPIKey(
		ctx,
		testOrgID,
		firstKey.Record.ID,
		identitystore.PrincipalRecord{},
	); err != nil {
		t.Fatalf("revoke first org api key: %v", err)
	}
	if _, err := store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, secondKeyInput); err != nil {
		t.Fatalf("create org api key after revoke: %v", err)
	}
}

func TestAgentResourceLimitsPreserveReplays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	const agentConfigLimit = int64(20)
	const agentProfileLimit = int64(2)
	const agentLimit = int64(2)
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_agent_configs_per_project":         agentConfigLimit,
		"max_active_agent_profiles_per_project": agentProfileLimit,
		"max_active_agents_per_project":         agentLimit,
	})
	now := time.Date(2026, 7, 24, 14, 0, 0, 0, time.UTC)
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID, "resource-limit", now)

	profileInput := executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            "Limited Profile",
		CurrentConfigID: configID,
		IdempotencyKey:  "limited-profile",
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, profileInput)
	if err != nil {
		t.Fatalf("create first profile: %v", err)
	}
	if _, err := pool.Exec(ctx, `
	WITH seed AS MATERIALIZED (
	  SELECT n, uuidv7() AS profile_id, uuidv7() AS version_id
	  FROM generate_series(1, $4::integer) AS n
	), profiles AS (
	  INSERT INTO agent_profiles(
	    id, project_id, name, current_version_id, idempotency_key, created_at, updated_at
	  )
	  SELECT profile_id, $1, 'Limit seed profile ' || n, version_id,
	         'limit-seed-profile-' || n, $3, $3
	  FROM seed
	  RETURNING id, project_id, current_version_id, created_at
	)
	INSERT INTO agent_profile_versions(
	  id, project_id, profile_id, generation, agent_config_id, reason, created_at
	)
	SELECT current_version_id, project_id, id, 1, $2, 'create', created_at
	FROM profiles
	`, testProjectID, configID, now, agentProfileLimit-1); err != nil {
		t.Fatalf("seed agent profiles to limit: %v", err)
	}
	replayedProfile, err := store.Execution().CreateAgentProfile(ctx, profileInput)
	if err != nil {
		t.Fatalf("replay profile at limit: %v", err)
	}
	if replayedProfile.Created || replayedProfile.ID != profile.ID {
		t.Fatalf("unexpected profile replay: %+v", replayedProfile)
	}
	if _, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            "Second Limited Profile",
		CurrentConfigID: configID,
		IdempotencyKey:  "second-limited-profile",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("profile over limit error = %v, want ErrConflict", err)
	}

	agentInput := executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  configID,
		LaunchedBy:     userPrincipal(testDefaultProviderAdminUserID),
		IdempotencyKey: "limited-agent",
	}
	agent, err := store.Execution().LaunchAgent(ctx, agentInput)
	if err != nil {
		t.Fatalf("create first agent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agents(
  id, org_id, project_id, state, current_config_id,
  idempotency_key, created_at, updated_at
)
SELECT md5('resource-limit-agent-' || n)::uuid, $1, $2, 'active', $3,
       'limit-seed-agent-' || n, $4, $4
FROM generate_series(1, $5::integer) AS n
`, testOrgID, testProjectID, configID, now, agentLimit-1); err != nil {
		t.Fatalf("seed agents to limit: %v", err)
	}
	replayedAgent, err := store.Execution().LaunchAgent(ctx, agentInput)
	if err != nil {
		t.Fatalf("replay agent at limit: %v", err)
	}
	if replayedAgent.Created || replayedAgent.Agent.ID != agent.Agent.ID {
		t.Fatalf("unexpected agent replay: %+v", replayedAgent)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  configID,
		LaunchedBy:     userPrincipal(testDefaultProviderAdminUserID),
		IdempotencyKey: "second-limited-agent",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("agent over limit error = %v, want ErrConflict", err)
	}

	configCount, err := testQueries(store).CountAgentConfigsForProject(
		ctx,
		dbsqlc.CountAgentConfigsForProjectParams{ProjectID: testProjectID},
	)
	if err != nil {
		t.Fatalf("count agent configs: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO agent_configs(
  org_id, project_id, configured_model_id, definition, source, source_format,
  source_hash, compiled_definition, compiler_version, effective_definition_hash,
  created_at
)
SELECT org_id, project_id, configured_model_id, definition,
       source || n, source_format, source_hash || n, compiled_definition,
       compiler_version, effective_definition_hash || n, $2
FROM agent_configs
CROSS JOIN generate_series(1, $3::bigint) AS n
WHERE id = $1
`, configID, now, agentConfigLimit-configCount); err != nil {
		t.Fatalf("seed agent configs to limit: %v", err)
	}
	sourceYAML := `
instruction: distinct config
model:
  provider_config: openai-prod
  name: test
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now.Add(4*time.Second))
	if _, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("agent config over limit error = %v, want ErrConflict", err)
	}
}

func TestSkillResourceLimitCountsIdentitiesNotRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	const skillLimit = int64(2)
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_active_skills_per_owner": skillLimit,
	})
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Resource Limit Skills Admin", "admin")
	input := skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "limited-skill",
		Description: "first revision", SkillMd: "# Limited skill",
		ArchiveBytes: []byte("first revision"), Actor: userPrincipal(admin.ID),
	}
	first, err := store.Skills().CreateSkillRevision(ctx, input)
	if err != nil {
		t.Fatalf("create first skill revision: %v", err)
	}
	skillCount, err := testQueries(store).CountActiveSkillsForOwner(
		ctx,
		dbsqlc.CountActiveSkillsForOwnerParams{
			OrgID:     testOrgID,
			OwnerKind: skillstore.SkillOwnerOrg,
		},
	)
	if err != nil {
		t.Fatalf("count active skills: %v", err)
	}
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin skill limit seed: %v", err)
	}
	defer func() { _ = seedTx.Rollback(ctx) }()
	if _, err := seedTx.Exec(ctx, `
INSERT INTO skills(org_id, owner_kind, name, created_at)
SELECT $1, 'org', 'limit-seed-skill-' || n, $2
FROM generate_series(1, $3::bigint) AS n
`, testOrgID, now, skillLimit-skillCount); err != nil {
		t.Fatalf("seed skills to limit: %v", err)
	}
	if _, err := seedTx.Exec(ctx, `
INSERT INTO skill_revisions(skill_id, revision, description, skill_md, archive_digest, created_at)
SELECT skill.id, 1, 'limit seed', '# Limit seed', 'limit-seed-' || skill.name, $2
FROM skills skill
WHERE skill.org_id = $1
  AND skill.owner_kind = 'org'
  AND skill.name LIKE 'limit-seed-skill-%'
`, testOrgID, now); err != nil {
		t.Fatalf("seed skill revisions to limit: %v", err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit skill limit seed: %v", err)
	}
	input.Description = "second revision"
	input.ArchiveBytes = []byte("second revision")
	second, err := store.Skills().CreateSkillRevision(ctx, input)
	if err != nil {
		t.Fatalf("create revision at identity limit: %v", err)
	}
	if second.ID != first.ID || second.Revision != 2 {
		t.Fatalf("unexpected second skill revision: %+v", second)
	}
	input.Name = "second-limited-skill"
	input.ArchiveBytes = []byte("new identity")
	if _, err := store.Skills().CreateSkillRevision(ctx, input); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("skill identity over limit error = %v, want ErrConflict", err)
	}
}

func TestResourceLimitLocksDoNotBlockUnrelatedCreates(t *testing.T) {
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Resource Limit Lock Admin", "admin")
	machine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Resource limit lock machine",
		IdempotencyKey: "resource-limit-lock-machine",
	})
	if err != nil {
		t.Fatalf("create daemon machine: %v", err)
	}

	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker transaction: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if err := resourceguard.Lock(
		ctx,
		dbsqlc.New(blocker),
		"skills",
		resourceguard.OwnerScope(testOrgID, skillstore.SkillOwnerOrg, NilID, NilID),
	); err != nil {
		t.Fatalf("lock skill creation: %v", err)
	}

	skillResult := make(chan error, 1)
	go func() {
		_, err := store.Skills().CreateSkillRevision(ctx, skillstore.CreateSkillInput{
			OrgID:        testOrgID,
			OwnerKind:    skillstore.SkillOwnerOrg,
			Name:         "resource-limit-lock-skill",
			Description:  "resource limit lock regression",
			SkillMd:      "# Resource limit lock skill",
			ArchiveBytes: []byte("resource limit lock skill"),
			Actor:        userPrincipal(admin.ID),
		})
		skillResult <- err
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockResourceCreation", 1)

	tokenCtx, cancelToken := context.WithTimeout(ctx, 5*time.Second)
	defer cancelToken()
	if _, err := store.Execution().CreateBYOMachineDaemonToken(tokenCtx, executionstore.CreateBYOMachineDaemonTokenInput{
		OrgID:     testOrgID,
		MachineID: machine.ID,
		Name:      "Resource limit lock token",
	}); err != nil {
		_ = blocker.Rollback(ctx)
		<-skillResult
		t.Fatalf("create daemon token while skill creation is blocked: %v", err)
	}

	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release skill creation lock: %v", err)
	}
	select {
	case err := <-skillResult:
		if err != nil {
			t.Fatalf("create skill after releasing its resource lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("skill creation did not finish after its resource lock was released")
	}
}

func TestMachineResourceLimitsPreserveReplaysAndReleaseTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	const machineLimit = int64(2)
	const daemonTokenLimit = int64(2)
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_live_machines_per_org":                machineLimit,
		"max_active_byo_daemon_tokens_per_machine": daemonTokenLimit,
	})
	now := time.Date(2026, 7, 24, 16, 0, 0, 0, time.UTC)
	machineInput := executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Limited Machine",
		IdempotencyKey: "limited-machine",
	}
	machine, err := store.Execution().CreateDaemonMachine(ctx, machineInput)
	if err != nil {
		t.Fatalf("create first machine: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO machines(
  org_id, source_kind, display_name, provider, lifecycle_state,
  lifecycle_changed_at, created_at, updated_at
)
SELECT $1, 'byo', 'Limit seed machine ' || n, 'byo', 'active', $2, $2, $2
FROM generate_series(1, $3::integer) AS n
`, testOrgID, now, machineLimit-1); err != nil {
		t.Fatalf("seed machines to limit: %v", err)
	}
	replayed, err := store.Execution().CreateDaemonMachine(ctx, machineInput)
	if err != nil {
		t.Fatalf("replay machine at limit: %v", err)
	}
	if replayed.Created || replayed.ID != machine.ID {
		t.Fatalf("unexpected machine replay: %+v", replayed)
	}
	if _, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Second Limited Machine",
		IdempotencyKey: "second-limited-machine",
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("machine over limit error = %v, want ErrConflict", err)
	}

	firstToken, err := store.Execution().CreateBYOMachineDaemonToken(ctx, executionstore.CreateBYOMachineDaemonTokenInput{
		OrgID: testOrgID, MachineID: machine.ID, Name: "First daemon token",
	})
	if err != nil {
		t.Fatalf("create first daemon token: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO machine_daemon_tokens(
  org_id, machine_id, name, token_hash, created_at
)
SELECT $1, $2, 'Limit seed daemon token ' || n,
       'legacy-daemon-token-hash-' || n, $3
FROM generate_series(1, $4::integer) AS n
`, testOrgID, machine.ID, now,
		daemonTokenLimit-1); err != nil {
		t.Fatalf("seed daemon tokens to limit: %v", err)
	}
	secondTokenInput := executionstore.CreateBYOMachineDaemonTokenInput{
		OrgID: testOrgID, MachineID: machine.ID, Name: "Second daemon token",
	}
	if _, err := store.Execution().CreateBYOMachineDaemonToken(ctx, secondTokenInput); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("daemon token over limit error = %v, want ErrConflict", err)
	}
	if _, err := store.Execution().RevokeBYOMachineDaemonToken(
		ctx,
		testOrgID,
		machine.ID,
		firstToken.Record.ID,
		"test",
	); err != nil {
		t.Fatalf("revoke first daemon token: %v", err)
	}
	if _, err := store.Execution().CreateBYOMachineDaemonToken(ctx, secondTokenInput); err != nil {
		t.Fatalf("create daemon token after revoke: %v", err)
	}
}

func TestTenantResourceLimitsUseTheirIntendedScopes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	const secretLimit = int64(2)
	const modelProviderLimit = int64(2)
	const configuredModelLimit = int64(2)
	const machinePoolLimit = int64(2)
	setOrgResourceLimitOverrides(t, ctx, pool, map[string]int64{
		"max_active_tenant_secrets_per_owner":              secretLimit,
		"max_active_tenant_model_provider_configs_per_org": modelProviderLimit,
		"max_active_configured_models_per_provider":        configuredModelLimit,
		"max_active_tenant_machine_pools_per_org":          machinePoolLimit,
	})
	now := time.Date(2026, 7, 24, 17, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Resource Limit Tenant Admin", "admin")

	for i := range secretLimit {
		if _, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerUser, OwnerUserID: admin.ID,
			Name:     fmt.Sprintf("limit-seed-secret-%d", i),
			Material: secrets.GenericMaterial{Value: "secret"},
			Actor:    userPrincipal(admin.ID),
		}); err != nil {
			t.Fatalf("seed secrets to limit: %v", err)
		}
	}
	if _, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerUser, OwnerUserID: admin.ID,
		Name:     "limited-secret",
		Material: secrets.GenericMaterial{Value: "secret"},
		Actor:    userPrincipal(admin.ID),
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("secret over limit error = %v, want ErrConflict", err)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO model_provider_configs(
  org_id, management_kind, name, api_format, api_variant, base_url,
  endpoint_path, request_timeout_ms, auth_kind, auth_options,
  credential_secret_id, created_at, updated_at
)
SELECT config.org_id, config.management_kind, 'limit-seed-provider-' || n,
       config.api_format, config.api_variant, config.base_url, config.endpoint_path,
       config.request_timeout_ms, config.auth_kind, config.auth_options,
       config.credential_secret_id, $2, $2
FROM model_provider_configs AS config
CROSS JOIN generate_series(1, $3::integer) AS n
WHERE config.id = $1
`, testDefaultProviderConfigID(), now, modelProviderLimit-1); err != nil {
		t.Fatalf("seed model provider configs to limit: %v", err)
	}
	if _, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              testOrgID,
		Name:               "limited-provider",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://api.openai.com/v1",
		CredentialSecretID: testDefaultProviderCredentialSecretID,
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("model provider config over limit error = %v, want ErrConflict", err)
	}

	for i := range configuredModelLimit {
		if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
			OrgID:                  testOrgID,
			ModelProviderConfigID:  testDefaultProviderConfigID(),
			Name:                   fmt.Sprintf("limit-seed-model-%d", i),
			ProviderModelSlug:      fmt.Sprintf("limit-seed-model-%d", i),
			ContextWindowTokens:    128_000,
			MaxOutputTokens:        8_192,
			DefaultMaxOutputTokens: intPtr(4_096),
		}); err != nil {
			t.Fatalf("seed configured models to limit: %v", err)
		}
	}
	if _, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  testOrgID,
		ModelProviderConfigID:  testDefaultProviderConfigID(),
		Name:                   "limited-model",
		ProviderModelSlug:      "limited-model",
		ContextWindowTokens:    128_000,
		MaxOutputTokens:        8_192,
		DefaultMaxOutputTokens: intPtr(4_096),
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("configured model over limit error = %v, want ErrConflict", err)
	}

	poolInput := completeMachinePoolCreateInputForTest(
		t,
		ctx,
		store,
		machinePoolInputWithDefaultMachineForTest(
			executionstore.CreateMachinePoolInput{
				OrgID: testOrgID, Name: "Limited Pool", Provider: "test.provider",
				MaxTotalMachines: 1,
			},
			defaultMachineFieldsForTest{
				DefaultMachineCPU:             1,
				DefaultMachineMemoryMB:        1_024,
				DefaultMachineEnv:             json.RawMessage(`{}`),
				DefaultMachineProviderOptions: json.RawMessage(`{"image":"test"}`),
			},
		),
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO machine_pools(
  org_id, name, management_kind, provider, default_machine_cpu,
  default_machine_memory_mb, default_machine_provider_options,
  provider_auth_secret_id, max_total_machines, max_total_cpu,
  max_total_memory_mb, max_machine_cpu, max_machine_memory_mb,
  created_at, updated_at
)
SELECT $1, 'Limit seed pool ' || n, 'tenant', 'test.provider', 1,
       1024, '{"image":"test"}'::jsonb, $2, 1, 1, 1024, 1, 1024, $3, $3
FROM generate_series(1, $4::integer) AS n
`, testOrgID, testDefaultProviderCredentialSecretID, now,
		machinePoolLimit); err != nil {
		t.Fatalf("seed machine pools to limit: %v", err)
	}
	if _, err := store.Execution().CreateMachinePool(ctx, poolInput); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("machine pool over limit error = %v, want ErrConflict", err)
	}
}
