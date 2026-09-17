//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestSubagentConfigChangesAreRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "readonly-child@example.com", "Read-only child")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "readonly-child", "Read-only child", subagentParentYAML,
	)
	parent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: testProjectID, ProfileID: profile.ID, AgentConfigID: profile.CurrentConfigID,
		LaunchedBy: userPrincipal(user.ID), IdempotencyKey: "readonly-parent",
	})
	require.NoError(t, err)
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, subagentParentYAML)
	for _, source := range []string{subagentParentYAML, ""} {
		config := executionstore.CreateAgentConfigInput{
			ProjectID: testProjectID, Source: source,
			ConfiguredModelID:  parseConfiguredModelID(t, compiled),
			CompiledDefinition: compiled.CanonicalJSON, CompilerVersion: compiled.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		}
		child, err := spawnSubagentForTest(t, ctx, store, parent.Agent, uuid.Nil,
			"readonly-child", "readonly-child-"+fmt.Sprint(len(source)), nil,
			func(input *executionstore.LaunchAgentInput) { input.DerivedConfig = &config })
		require.NoError(t, err)
		_, err = store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: config, AgentID: child.Agent.ID,
			ActorType: identitystore.PrincipalTypeUser, ActorID: user.ID,
		})
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		require.ErrorContains(t, err, "subagent configurations are read-only")
	}
}

func systemPrincipalForTest(id uuid.UUID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: id}
}

func defaultAgentListingForTest() listing.Options {
	return listing.Options{SortField: "created_at", SortDesc: true}
}

const subagentParentYAML = `
instruction: Coordinate helpers.
model:
  provider_config: openai-prod
  name: gpt-test
subagents:
  fork:
    type: self
    max_instances: 1
max_subagents: 2
`

func intPtrForSubagentTest(value int) *int {
	return &value
}

func spawnSubagentForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	parent executionstore.AgentRecord,
	configID uuid.UUID,
	name, idempotencyKey string,
	maxInstances *int,
	options ...func(*executionstore.LaunchAgentInput),
) (executionstore.LaunchAgentResult, error) {
	t.Helper()
	actor, err := executionstore.SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		t.Fatalf("subagent actor params: %v", err)
	}
	input := executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  configID,
		LaunchedBy:     systemPrincipalForTest(parent.ID),
		Name:           &name,
		Message:        "Investigate the failing build.",
		MessageActor:   actor,
		IdempotencyKey: idempotencyKey,
		Subagent: &executionstore.SubagentLaunch{
			ParentAgentID: parent.ID,
			Key:           "fork",
			MaxInstances:  maxInstances,
			MaxDepth:      agentconfig.MaxSubagentDepth,
		},
	}
	for _, option := range options {
		option(&input)
	}
	return store.Execution().LaunchAgent(ctx, input)
}

func withoutLaunchMessage(input *executionstore.LaunchAgentInput) {
	input.Message = ""
}

func TestLaunchSubagentLinksParentAndEnforcesLimits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-launch@example.com", "Subagent Launch")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-launch", "Subagent Launch", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-launch-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent

	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "worker-1", "subagent-launch-child-1", intPtrForSubagentTest(1),
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if child.Agent.ParentAgentID != parent.ID || child.Agent.SubagentKey != "fork" {
		t.Fatalf("child linkage = parent %s key %q", child.Agent.ParentAgentID, child.Agent.SubagentKey)
	}
	if child.AgentInput.ID == uuid.Nil {
		t.Fatal("child launch did not queue the task input")
	}

	_, err = spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "worker-2", "subagent-launch-child-2", intPtrForSubagentTest(1),
	)
	if !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("second spawn beyond max_instances: err = %v, want conflict", err)
	}

	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].AgentID != child.Agent.ID ||
		subagents[0].State != executionstore.SubagentStateRunning {
		t.Fatalf("subagents = %+v", subagents)
	}

	topLevel, err := store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: testProjectID,
		Limit:     50,
		List:      defaultAgentListingForTest(),
	})
	if err != nil {
		t.Fatalf("list top-level agents: %v", err)
	}
	if containsAgentID(topLevel.Agents, child.Agent.ID) || !containsAgentID(topLevel.Agents, parent.ID) {
		t.Fatalf("top-level listing should hide subagents: %+v", agentIDsForTest(topLevel.Agents))
	}
	withChildren, err := store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: testProjectID,
		Limit:     50,
		List:      defaultAgentListingForTest(),
		Filters:   executionstore.AgentListFilters{IncludeSubagents: true},
	})
	if err != nil {
		t.Fatalf("list with subagents: %v", err)
	}
	if !containsAgentID(withChildren.Agents, child.Agent.ID) {
		t.Fatalf("include_subagents listing should contain the child")
	}
	parentID := parent.ID
	byParent, err := store.Execution().ListAgentsForProject(ctx, executionstore.ListAgentsForProjectInput{
		ProjectID: testProjectID,
		Limit:     50,
		List:      defaultAgentListingForTest(),
		Filters:   executionstore.AgentListFilters{ParentAgentID: &parentID},
	})
	if err != nil {
		t.Fatalf("list by parent: %v", err)
	}
	if len(byParent.Agents) != 1 || byParent.Agents[0].ID != child.Agent.ID {
		t.Fatalf("parent filter listing = %+v", agentIDsForTest(byParent.Agents))
	}

	descendants, err := store.Execution().ListAgentDescendantIDs(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list descendants: %v", err)
	}
	if len(descendants) != 1 || descendants[0] != child.Agent.ID {
		t.Fatalf("descendants = %v", descendants)
	}

	archived, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, parent.ID, userPrincipal(user.ID))
	if err != nil {
		t.Fatalf("archive parent: %v", err)
	}
	if archived.State != executionstore.AgentStateArchived {
		t.Fatalf("parent state = %s", archived.State)
	}
	childAfter, err := store.Execution().GetAgentInProject(ctx, testProjectID, child.Agent.ID)
	if err != nil {
		t.Fatalf("load child after parent archive: %v", err)
	}
	if childAfter.State != executionstore.AgentStateArchived {
		t.Fatalf("archiving the parent should archive the child, got %s", childAfter.State)
	}
}

func TestLaunchSubagentWithDerivedConfigKeepsProfileAttribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-derived@example.com", "Subagent Derived")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-derived", "Subagent Derived", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-derived-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	if parent.AgentProfileID != profile.ID {
		t.Fatalf("parent profile = %s, want %s", parent.AgentProfileID, profile.ID)
	}

	derivedYAML := strings.Replace(subagentParentYAML, "Coordinate helpers.", "Coordinate helpers carefully.", 1)
	compiled := storagefixture.SeedModelAndCompileAgentYAML(
		t, ctx, store.Models(), store.Execution(), testOrgID, testProjectID, derivedYAML,
	)
	derived := executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Source:                  derivedYAML,
		ConfiguredModelID:       compiled.Compiled.Model.ConfiguredModelID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	}
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, uuid.Nil, "worker", "subagent-derived-child", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.ProfileID = parent.AgentProfileID
			input.DerivedConfig = &derived
		},
	)
	if err != nil {
		t.Fatalf("spawn subagent with derived config: %v", err)
	}
	if child.Agent.AgentProfileID != profile.ID {
		t.Fatalf("child profile = %s, want %s", child.Agent.AgentProfileID, profile.ID)
	}
	if child.Agent.CurrentConfigID == profile.CurrentConfigID || child.Agent.CurrentConfigID == uuid.Nil {
		t.Fatalf(
			"child config = %s, want a new config distinct from %s",
			child.Agent.CurrentConfigID, profile.CurrentConfigID,
		)
	}

	unrelated := mustCreateAgentConfigFromYAML(
		t, ctx, store, strings.Replace(subagentParentYAML, "Coordinate helpers.", "Coordinate helpers alone.", 1),
	)
	_, err = spawnSubagentForTest(
		t, ctx, store, parent, unrelated.ID, "worker-2", "subagent-derived-child-2", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.ProfileID = profile.ID
		},
	)
	if !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("existing config outside the profile history: err = %v, want not found", err)
	}
}

func TestLaunchSubagentSharesParentMachinesForAnyBaseConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, storage.WithMachinePoolProviders(mergingMachinePoolProviders{}))
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-machines@example.com", "Subagent Machines")
	machine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Subagent Machine",
		IdempotencyKey: "idem-subagent-machine",
	})
	if err != nil {
		t.Fatalf("create machine: %v", err)
	}
	if _, _, err := store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:          testOrgID,
		ProjectID:      testProjectID,
		MachineID:      machine.ID,
		IdempotencyKey: "idem-subagent-machine-grant",
	}); err != nil {
		t.Fatalf("create project machine grant: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "subagent-machines", "Subagent Machines", `
instruction: Coordinate helpers on a machine.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_name: `+machine.DisplayName+`
    cwd: /workspace
tools:
  run_command: {}
subagents:
  helper:
    type: self
`)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-machines-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	if len(parentLaunch.MachineBindings) != 1 {
		t.Fatalf("parent bindings = %+v", parentLaunch.MachineBindings)
	}
	machineless := mustCreateAgentConfigFromYAML(t, ctx, store, `
instruction: Help without machines of your own.
model:
  provider_config: openai-prod
  name: gpt-test
`)
	child, err := spawnSubagentForTest(
		t, ctx, store, parentLaunch.Agent, machineless.ID, "helper", "subagent-machines-child", nil,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if len(child.MachineBindings) != 1 || child.MachineBindings[0].MachineID != machine.ID ||
		child.MachineBindings[0].Cwd != "/workspace" {
		t.Fatalf("child bindings = %+v, want the parent's machine", child.MachineBindings)
	}
	if len(child.ProvisionMachineIDs) != 0 {
		t.Fatalf("child provisioned machines %v, want none", child.ProvisionMachineIDs)
	}
}

func TestLaunchSubagentRejectsForeignParentAndDepthLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-depth@example.com", "Subagent Depth")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-depth", "Subagent Depth", subagentParentYAML,
	)
	topLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-depth-top",
	})
	if err != nil {
		t.Fatalf("launch top-level agent: %v", err)
	}

	otherProjectID := seedAdditionalProjectForTest(t, ctx, pool, "subagent_foreign_parent")
	otherConfig := storagefixture.SeedAgentConfig(
		t, ctx, store.Models(), store.Execution(), testOrgID, otherProjectID, subagentParentYAML,
	)
	foreignLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      otherProjectID,
		AgentConfigID:  otherConfig.ID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-depth-foreign",
	})
	if err != nil {
		t.Fatalf("launch agent in other project: %v", err)
	}
	_, err = spawnSubagentForTest(
		t, ctx, store, topLaunch.Agent, profile.CurrentConfigID, "foreign", "subagent-depth-foreign-child", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.Subagent.ParentAgentID = foreignLaunch.Agent.ID
		},
	)
	if !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("spawn with a parent from another project: err = %v, want not found", err)
	}

	_, err = spawnSubagentForTest(
		t, ctx, store, topLaunch.Agent, profile.CurrentConfigID, "unbounded", "subagent-depth-unbounded", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.Subagent.MaxDepth = 0
		},
	)
	if err == nil || !strings.Contains(err.Error(), "max depth") {
		t.Fatalf("spawn without a depth limit: err = %v, want max depth error", err)
	}
	shallow, err := spawnSubagentForTest(
		t, ctx, store, topLaunch.Agent, profile.CurrentConfigID, "shallow", "subagent-depth-shallow", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.Subagent.MaxDepth = 1
		},
	)
	if err != nil {
		t.Fatalf("spawn first level with max depth 1: %v", err)
	}
	_, err = spawnSubagentForTest(
		t, ctx, store, shallow.Agent, profile.CurrentConfigID, "shallow-child", "subagent-depth-shallow-child", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.Subagent.MaxDepth = 1
		},
	)
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("spawn second level with max depth 1: err = %v, want invalid request", err)
	}

	parent := topLaunch.Agent
	for level := 1; level <= agentconfig.MaxSubagentDepth; level++ {
		child, err := spawnSubagentForTest(
			t, ctx, store, parent, profile.CurrentConfigID,
			fmt.Sprintf("level-%d", level), fmt.Sprintf("subagent-depth-level-%d", level), nil,
		)
		if err != nil {
			t.Fatalf("spawn subagent at depth %d: %v", level, err)
		}
		parent = child.Agent
	}
	_, err = spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "too-deep", "subagent-depth-too-deep", nil,
	)
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("spawn at depth %d: err = %v, want invalid request", agentconfig.MaxSubagentDepth, err)
	}
}

func TestSubagentArchiveNotifiesParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-notify@example.com", "Subagent Notify")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-notify", "Subagent Notify", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-notify-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "notify", "subagent-notify-child", nil,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	if _, _, err := store.Execution().ArchiveAgent(
		ctx, testProjectID, child.Agent.ID, userPrincipal(user.ID),
	); err != nil {
		t.Fatalf("archive subagent: %v", err)
	}
	var metadata json.RawMessage
	var deliveryMode string
	if err := pool.QueryRow(
		ctx,
		`SELECT metadata, delivery_mode FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message'`,
		testProjectID,
		parent.ID,
	).Scan(&metadata, &deliveryMode); err != nil {
		t.Fatalf("load parent notification input: %v", err)
	}
	if deliveryMode != string(executionstore.DeliveryModeSteering) {
		t.Fatalf("parent notification delivery mode = %q, want steering", deliveryMode)
	}
	var decodedMetadata struct {
		SubagentMessage struct {
			Kind    string `json:"kind"`
			AgentID string `json:"agent_id"`
		} `json:"subagent_message"`
	}
	if err := json.Unmarshal(metadata, &decodedMetadata); err != nil {
		t.Fatalf("decode parent notification metadata: %v", err)
	}
	if decodedMetadata.SubagentMessage.Kind != "archived" || decodedMetadata.SubagentMessage.AgentID == "" {
		t.Fatalf("parent notification metadata = %s", metadata)
	}
}

func TestArchiveIdleAgentsWaitsForBusyDescendants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-idle@example.com", "Subagent Idle")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-idle", "Subagent Idle", subagentParentYAML,
	)
	topLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-idle-top",
	})
	if err != nil {
		t.Fatalf("launch top-level agent: %v", err)
	}
	top := topLaunch.Agent
	middle, err := spawnSubagentForTest(
		t, ctx, store, top, profile.CurrentConfigID, "middle", "subagent-idle-middle", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.ArchiveAfterIdleMinutes = intPtrForSubagentTest(1)
		},
	)
	if err != nil {
		t.Fatalf("spawn middle subagent: %v", err)
	}
	leaf, err := spawnSubagentForTest(
		t, ctx, store, middle.Agent, profile.CurrentConfigID, "leaf", "subagent-idle-leaf", nil,
	)
	if err != nil {
		t.Fatalf("spawn leaf subagent: %v", err)
	}
	for _, agentID := range []uuid.UUID{middle.Agent.ID, leaf.Agent.ID} {
		if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, agentID); err != nil {
			t.Fatalf("clear wakeup: %v", err)
		}
	}
	leafLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		leaf.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire leaf runtime lock: %v", err)
	}
	asOf := time.Now().Add(2 * time.Hour)

	_, archived, err := store.Execution().ArchiveIdleAgentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents while leaf is running: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a grandchild held a runtime lock, want 0", archived)
	}

	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, leaf.Agent.ID, leafLock.ID); err != nil {
		t.Fatalf("release leaf runtime lock: %v", err)
	}
	if err := store.Execution().MarkAgentWakeup(
		ctx, testProjectID, leaf.Agent.ID, []byte(`{"reason":"test"}`),
	); err != nil {
		t.Fatalf("mark leaf wakeup: %v", err)
	}
	_, archived, err = store.Execution().ArchiveIdleAgentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents while leaf has a wakeup: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a grandchild had a pending wakeup, want 0", archived)
	}

	if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, leaf.Agent.ID); err != nil {
		t.Fatalf("clear leaf wakeup: %v", err)
	}
	_, archived, err = store.Execution().ArchiveIdleAgents(ctx, 10)
	if err != nil {
		t.Fatalf("archive idle subagents before the idle window: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents before the idle window elapsed, want 0", archived)
	}
	_, archived, err = store.Execution().ArchiveIdleAgentsAsOf(ctx, asOf, 10)
	if err != nil {
		t.Fatalf("archive idle subagents: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived %d subagents, want 1", archived)
	}
	for _, agentID := range []uuid.UUID{middle.Agent.ID, leaf.Agent.ID} {
		record, err := store.Execution().GetAgentInProject(ctx, testProjectID, agentID)
		if err != nil {
			t.Fatalf("load agent: %v", err)
		}
		if record.State != executionstore.AgentStateArchived {
			t.Fatalf("agent %s state = %s, want archived", agentID, record.State)
		}
	}
	topRecord, err := store.Execution().GetAgentInProject(ctx, testProjectID, top.ID)
	if err != nil {
		t.Fatalf("load top-level agent: %v", err)
	}
	if topRecord.State != executionstore.AgentStateActive {
		t.Fatalf("top-level agent state = %s, want active", topRecord.State)
	}
}

func containsAgentID(agents []executionstore.AgentRecord, id uuid.UUID) bool {
	for _, agent := range agents {
		if agent.ID == id {
			return true
		}
	}
	return false
}

func agentIDsForTest(agents []executionstore.AgentRecord) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.ID.String())
	}
	return out
}

func TestIdleArchiveAndStatusTreatPendingToolCallsAsRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-pending@example.com", "Subagent Pending")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-pending", "Subagent Pending", subagentParentYAML+"tools:\n  run_command: {}\n",
	)
	topLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-pending-top",
	})
	if err != nil {
		t.Fatalf("launch top-level agent: %v", err)
	}
	top := topLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, top, profile.CurrentConfigID, "child", "subagent-pending-child", nil, withoutLaunchMessage,
		func(input *executionstore.LaunchAgentInput) {
			input.ArchiveAfterIdleMinutes = intPtrForSubagentTest(1)
		},
	)
	if err != nil {
		t.Fatalf("spawn child subagent: %v", err)
	}
	childLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, child.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire child runtime lock: %v", err)
	}
	createReadyToolCallsForTest(
		t, ctx, store, child.Agent.ID, user.ID, child.AgentConfig.ID, childLock, "subagent-pending",
		[]toolCallSpecForTest{{Label: "run", Name: "run_command", Input: json.RawMessage(`{"command":"sleep 1"}`)}},
	)
	if err := store.Execution().ReleaseAgentRuntimeLock(ctx, testProjectID, child.Agent.ID, childLock.ID); err != nil {
		t.Fatalf("release child runtime lock: %v", err)
	}
	if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, child.Agent.ID); err != nil {
		t.Fatalf("clear child wakeup: %v", err)
	}

	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, top.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].State != executionstore.SubagentStateRunning {
		t.Fatalf("subagents with a pending tool call = %+v, want one running subagent", subagents)
	}
	_, archived, err := store.Execution().ArchiveIdleAgentsAsOf(ctx, time.Now().Add(2*time.Hour), 10)
	if err != nil {
		t.Fatalf("archive idle subagents with a pending tool call: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d subagents while a tool call awaited its result, want 0", archived)
	}
}

func TestArchiveIdleAgentRechecksEligibilityUnderLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-recheck@example.com", "Subagent Recheck")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-recheck", "Subagent Recheck", subagentParentYAML,
	)
	topLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-recheck-top",
	})
	if err != nil {
		t.Fatalf("launch top-level agent: %v", err)
	}
	child, err := spawnSubagentForTest(
		t, ctx, store, topLaunch.Agent, profile.CurrentConfigID, "child", "subagent-recheck-child", nil,
		func(input *executionstore.LaunchAgentInput) {
			input.ArchiveAfterIdleMinutes = intPtrForSubagentTest(1)
		},
	)
	if err != nil {
		t.Fatalf("spawn child subagent: %v", err)
	}
	asOf := time.Now().Add(2 * time.Hour)

	archived, err := store.Execution().ArchiveIdleAgentCandidateAsOf(ctx, testProjectID, child.Agent.ID, asOf)
	if err != nil {
		t.Fatalf("archive candidate with a pending wakeup: %v", err)
	}
	if archived != 0 {
		t.Fatalf("archived %d agents while the candidate had a pending wakeup, want 0", archived)
	}
	current, err := store.Execution().GetAgentInProject(ctx, testProjectID, child.Agent.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if current.State != executionstore.AgentStateActive {
		t.Fatalf("child state = %s, want active after a skipped archive", current.State)
	}

	if err := store.Execution().DeleteAgentWakeup(ctx, testProjectID, child.Agent.ID); err != nil {
		t.Fatalf("clear child wakeup: %v", err)
	}
	archived, err = store.Execution().ArchiveIdleAgentCandidateAsOf(ctx, testProjectID, child.Agent.ID, asOf)
	if err != nil {
		t.Fatalf("archive idle candidate: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived %d agents, want 1", archived)
	}
	current, err = store.Execution().GetAgentInProject(ctx, testProjectID, child.Agent.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if current.State != executionstore.AgentStateArchived {
		t.Fatalf("child state = %s, want archived", current.State)
	}
}

func TestSubagentQuestionSurfacesOnParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-question@example.com", "Subagent Question")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-question", "Subagent Question", subagentParentYAML+"tools:\n  ask_question: {}\n",
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-question-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "asker", "subagent-question-child", nil, withoutLaunchMessage,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	parentLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, parent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire parent runtime lock: %v", err)
	}
	parentToolCallIDs := createReadyToolCallsForTest(
		t, ctx, store, parent.ID, user.ID, parentLaunch.AgentConfig.ID, parentLock, "subagent-question-steer",
		[]toolCallSpecForTest{{
			Label: "send",
			Name:  "send_agent_message",
			Input: json.RawMessage(`{"agent_id":"x","message":"Skip the decision and continue."}`),
		}},
	)
	runtimeLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, child.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire child runtime lock: %v", err)
	}
	toolCallIDs := createReadyToolCallsForTest(
		t, ctx, store, child.Agent.ID, user.ID, child.AgentConfig.ID, runtimeLock, "subagent-question",
		[]toolCallSpecForTest{{
			Label: "question",
			Name:  "ask_question",
			Input: json.RawMessage(`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"}]}]}`),
		}},
	)
	form, err := interactionform.New(
		"Need a decision",
		nil,
		[]interactionform.Question{{Prompt: "Continue?", Options: []interactionform.Option{{Label: "Yes"}}}},
	)
	if err != nil {
		t.Fatalf("create question form: %v", err)
	}
	if _, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       child.Agent.ID,
			ToolCallID:    toolCallIDs["question"],
			RuntimeLockID: runtimeLock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{Form: form},
			), nil
		},
	); err != nil {
		t.Fatalf("create child question: %v", err)
	}

	descendants, err := store.Execution().ListAgentDescendantIDs(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list descendants: %v", err)
	}
	tree, err := store.Execution().ListAgentInteractions(ctx, executionstore.ListAgentInteractionsInput{
		ProjectID: testProjectID,
		AgentIDs:  append([]uuid.UUID{parent.ID}, descendants...),
		State:     executionstore.AgentInteractionStateOpen,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list tree interactions: %v", err)
	}
	if len(tree.Interactions) != 1 || tree.Interactions[0].AgentID != child.Agent.ID ||
		tree.Interactions[0].AgentName != "asker" || tree.Interactions[0].SubagentKey != "fork" {
		t.Fatalf("tree interactions = %+v", tree.Interactions)
	}
	var kind string
	if err := pool.QueryRow(
		ctx,
		`SELECT metadata->'subagent_message'->>'kind' FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message'`,
		testProjectID,
		parent.ID,
	).Scan(&kind); err != nil {
		t.Fatalf("load parent question notification: %v", err)
	}
	if kind != "question" {
		t.Fatalf("parent notification kind = %q", kind)
	}

	if _, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       parent.ID,
			ToolCallID:    parentToolCallIDs["send"],
			RuntimeLockID: parentLock.ID,
		},
		func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			childPublicID, err := publicid.Encode(publicid.KindAgent, child.Agent.ID)
			if err != nil {
				return nil, err
			}
			status, turns, err := reader.ReadSubagentTurns(ctx, childPublicID, 0, 10)
			if err != nil {
				return nil, fmt.Errorf("read subagent turns: %w", err)
			}
			if status.AgentID != child.Agent.ID || len(turns) == 0 || len(turns[0].OpeningEvents) == 0 {
				return nil, fmt.Errorf("subagent turns = %+v", turns)
			}
			_, events, err := reader.ReadSubagentTurnEvents(ctx, childPublicID, turns[0].ID, 0, 10)
			if err != nil {
				return nil, fmt.Errorf("read subagent turn events: %w", err)
			}
			if len(events) == 0 || events[0].TurnID != turns[0].ID {
				return nil, fmt.Errorf("subagent turn events = %+v", events)
			}
			_, _, err = reader.ReadSubagentTurnEvents(ctx, childPublicID, parentToolCallIDs["send"], 0, 10)
			if !errors.Is(err, storeerr.ErrInvalidRequest) {
				return nil, fmt.Errorf("read foreign turn: err = %w, want invalid request", err)
			}
			parentPublicID, err := publicid.Encode(publicid.KindAgent, parent.ID)
			if err != nil {
				return nil, err
			}
			if _, _, err := reader.ReadSubagentTurns(ctx, parentPublicID, 0, 10); !errors.Is(err, storeerr.ErrInvalidRequest) {
				return nil, fmt.Errorf("read a non-child agent: err = %w, want invalid request", err)
			}
			return executionstore.SendSubagentMessageForToolCall(
				executionstore.SendSubagentMessageInput{
					TargetAgentID: child.Agent.ID,
					Message:       "Skip the decision and continue.",
				},
				executionstore.ToolCallCompletionInput{
					Outcome:            executionstore.ToolResultOutcomeSucceeded,
					ResultContentParts: json.RawMessage(`[{"type":"text","text":"delivered"}]`),
				},
			), nil
		},
	); err != nil {
		t.Fatalf("send message to child: %v", err)
	}
	var interactionState, deliveryMode string
	if err := pool.QueryRow(
		ctx,
		`SELECT interaction.state, input.delivery_mode
		 FROM agent_interaction_read_projection interaction
		 JOIN agent_inputs input ON input.id = interaction.resolved_by_input_id
		 WHERE interaction.project_id = $1 AND interaction.agent_id = $2`,
		testProjectID,
		child.Agent.ID,
	).Scan(&interactionState, &deliveryMode); err != nil {
		t.Fatalf("load child question after parent message: %v", err)
	}
	if interactionState != string(executionstore.AgentInteractionStateCanceled) || deliveryMode != "steering" {
		t.Fatalf("child question state = %q resolved by %q input, want canceled by steering", interactionState, deliveryMode)
	}
	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].State == executionstore.SubagentStateWaitingOnInteraction {
		t.Fatalf("subagents after parent message = %+v", subagents)
	}
}

func TestStopSubagentCancelsThenArchives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-stop@example.com", "Subagent Stop")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-stop", "Subagent Stop", subagentParentYAML+"tools:\n  ask_question: {}\n",
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-stop-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "worker", "subagent-stop-child", nil, withoutLaunchMessage,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	parentLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, parent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire parent runtime lock: %v", err)
	}
	parentToolCallIDs := createReadyToolCallsForTest(
		t, ctx, store, parent.ID, user.ID, parentLaunch.AgentConfig.ID, parentLock, "subagent-stop-calls",
		[]toolCallSpecForTest{
			{Label: "cancel", Name: "stop_agent", Input: json.RawMessage(`{"agent_id":"x"}`)},
			{Label: "archive", Name: "stop_agent", Input: json.RawMessage(`{"agent_id":"x","archive":true}`)},
		},
	)
	childLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx, testProjectID, child.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire child runtime lock: %v", err)
	}
	childToolCallIDs := createReadyToolCallsForTest(
		t, ctx, store, child.Agent.ID, user.ID, child.AgentConfig.ID, childLock, "subagent-stop-question",
		[]toolCallSpecForTest{{
			Label: "question",
			Name:  "ask_question",
			Input: json.RawMessage(`{"questions":[{"prompt":"Continue?","options":[{"label":"Yes"}]}]}`),
		}},
	)
	form, err := interactionform.New(
		"Need a decision",
		nil,
		[]interactionform.Question{{Prompt: "Continue?", Options: []interactionform.Option{{Label: "Yes"}}}},
	)
	if err != nil {
		t.Fatalf("create question form: %v", err)
	}
	if _, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       child.Agent.ID,
			ToolCallID:    childToolCallIDs["question"],
			RuntimeLockID: childLock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{Form: form},
			), nil
		},
	); err != nil {
		t.Fatalf("create child question: %v", err)
	}

	stop := func(label string, archive bool) {
		t.Helper()
		_, err := store.Execution().ExecuteToolCall(
			ctx,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       parent.ID,
				ToolCallID:    parentToolCallIDs[label],
				RuntimeLockID: parentLock.ID,
			},
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.StopSubagentForToolCall(
					executionstore.StopSubagentInput{TargetAgentID: child.Agent.ID, Archive: archive},
					executionstore.ToolCallCompletionInput{
						Outcome:            executionstore.ToolResultOutcomeSucceeded,
						ResultContentParts: json.RawMessage(`[{"type":"text","text":"stopped"}]`),
					},
				), nil
			},
		)
		if err != nil {
			t.Fatalf("stop subagent (%s): %v", label, err)
		}
	}

	stop("cancel", false)
	afterCancel, err := store.Execution().GetAgentInProject(ctx, testProjectID, child.Agent.ID)
	if err != nil {
		t.Fatalf("load child after cancel: %v", err)
	}
	if afterCancel.State != executionstore.AgentStateActive {
		t.Fatalf("child state after cancel = %s, want active", afterCancel.State)
	}
	var interactionState string
	if err := pool.QueryRow(
		ctx,
		`SELECT state FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2`,
		testProjectID, child.Agent.ID,
	).Scan(&interactionState); err != nil {
		t.Fatalf("load child question after cancel: %v", err)
	}
	if interactionState != string(executionstore.AgentInteractionStateCanceled) {
		t.Fatalf("child question state after cancel = %q, want canceled", interactionState)
	}
	var kinds []string
	rows, err := pool.Query(
		ctx,
		`SELECT metadata->'subagent_message'->>'kind' FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2 AND idempotency_scope = 'subagent_message' ORDER BY queued_at`,
		testProjectID, parent.ID,
	)
	if err != nil {
		t.Fatalf("load parent notifications: %v", err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("scan parent notification: %v", err)
		}
		kinds = append(kinds, kind)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate parent notifications: %v", err)
	}
	if !slices.Contains(kinds, executionstore.SubagentMessageKindCanceled) {
		t.Fatalf("parent notifications after cancel = %v, want a canceled message", kinds)
	}
	subagents, err := store.Execution().ListSubagents(ctx, testProjectID, parent.ID)
	if err != nil {
		t.Fatalf("list subagents: %v", err)
	}
	if len(subagents) != 1 || subagents[0].Archived || subagents[0].HasOpenQuestion {
		t.Fatalf("subagents after cancel = %+v, want one active subagent without an open question", subagents)
	}

	stop("archive", true)
	afterArchive, err := store.Execution().GetAgentInProject(ctx, testProjectID, child.Agent.ID)
	if err != nil {
		t.Fatalf("load child after archive: %v", err)
	}
	if afterArchive.State != executionstore.AgentStateArchived {
		t.Fatalf("child state after archive = %s, want archived", afterArchive.State)
	}
}

func TestAgentRuntimeLockReaperSkipsSubagentWithContendedParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "subagent-reap@example.com", "Subagent Reap")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t, ctx, store, "subagent-reap", "Subagent Reap", subagentParentYAML,
	)
	parentLaunch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "subagent-reap-parent",
	})
	if err != nil {
		t.Fatalf("launch parent: %v", err)
	}
	parent := parentLaunch.Agent
	child, err := spawnSubagentForTest(
		t, ctx, store, parent, profile.CurrentConfigID, "reap", "subagent-reap-child", nil,
	)
	if err != nil {
		t.Fatalf("spawn subagent: %v", err)
	}
	fixture := runtimeLockLeaseFixture{Pool: pool, Store: store, AgentID: child.Agent.ID}
	lock := fixture.acquire(t, ctx, testWorkerProcessID, time.Minute)
	expireAgentRuntimeLockForTest(t, ctx, store, lock.ID)

	parentTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin parent lock holder: %v", err)
	}
	defer func() { _ = parentTx.Rollback(ctx) }()
	if _, err := parentTx.Exec(
		ctx,
		`SELECT id FROM agents WHERE project_id = $1 AND id = $2 FOR UPDATE`,
		testProjectID,
		parent.ID,
	); err != nil {
		t.Fatalf("lock parent row: %v", err)
	}

	reapCtx, cancelReap := context.WithTimeout(ctx, 2*time.Second)
	reaped, err := store.Execution().ReapExpiredAgentRuntimeLocks(reapCtx, 100)
	cancelReap()
	if err != nil {
		t.Fatalf("reap with contended parent: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("reaped %d runtime locks while parent was locked, want 0", reaped)
	}
	if err := parentTx.Commit(ctx); err != nil {
		t.Fatalf("release parent row: %v", err)
	}

	reaped, err = store.Execution().ReapExpiredAgentRuntimeLocks(ctx, 100)
	if err != nil {
		t.Fatalf("reap after parent contention cleared: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("reaped %d runtime locks after parent contention cleared, want 1", reaped)
	}
}
