//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestAgentExecutorKeepsPromptPrefixStableAcrossTurns(t *testing.T) {
	ctx := context.Background()
	fixture := newKernelFixture(t, ctx)
	now := fixture.Now
	skill, err := fixture.Store.Skills().CreateSkillRevision(ctx, skillstore.CreateSkillInput{
		OrgID:          kernelTestOrgID,
		OwnerKind:      skillstore.SkillOwnerProject,
		OwnerProjectID: kernelTestProjectID,
		Name:           "release-checklist",
		Description:    "Verify a release before shipping.",
		SkillMd:        "# Release checklist\nConfirm the changelog and the tag.",
		ArchiveBytes:   []byte("release checklist archive"),
		Actor:          kernelTestUserPrincipal(kernelTestUserID),
	})
	if err != nil {
		t.Fatalf("create skill: %v", err)
	}
	skillPublicID, err := publicid.Encode(publicid.KindSkill, skill.ID)
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	machinePool := createKernelMachinePool(t, ctx, fixture, "kernel-prompt-cache")
	profile := fixture.createConfigAndProfileBookmark(
		t,
		ctx,
		"Kernel Prompt Cache",
		"kernel-prompt-cache",
		fmt.Sprintf(`instruction: Help the user ship the release.
model:
  provider_config: openai-prod
  name: kernel-prompt-cache
machine_sources:
  - machine_pool_name: %s
    max_machines: 1
    initial_num_machines: 0
tools:
  run_command:
    permission:
      mode: always_allow
      parameters: {}
  create_machine:
    permission:
      mode: always_allow
      parameters: {}
  lookup_customer:
    type: custom
    permission:
      mode: always_ask
      parameters: {}
    description: Look up a customer by email.
    input_schema:
      type: object
      properties:
        email:
          type: string
      required: [email]
skills:
  - %s
`, machinePool.Name, skillPublicID),
		now,
	)
	launch, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      kernelTestProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
		IdempotencyKey: "kernel-prompt-cache-launch",
	})
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	agentID := launch.Agent.ID
	attachKernelSlackTarget(t, ctx, fixture, agentID, profile.ID, "prompt-cache", "C_PROMPT_CACHE:1.0")

	text := func(id, value string) model.Response {
		return model.Response{
			ID:         id,
			Content:    []model.ResponsePart{{Type: model.ResponsePartTypeText, Text: value}},
			StopReason: model.StopReasonEndTurn,
		}
	}
	modelClient := &sequenceKernelModel{
		providerModelSlug: "kernel-prompt-cache",
		responses: []model.Response{
			text("resp_prompt_cache_1", "The checklist covers the changelog and the tag."),
			{
				ID:         "resp_prompt_cache_2",
				StopReason: model.StopReasonToolUse,
				Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{{
					ID:    "call_prompt_cache_skill",
					Name:  toolcatalog.ToolNameSkill,
					Input: json.RawMessage(`{"name":"release-checklist"}`),
				}}),
			},
			text("resp_prompt_cache_3", "Loaded the release checklist."),
			text("resp_prompt_cache_4", "Done."),
		},
	}
	executor := AgentExecutor{
		Store:         fixture.Store,
		ModelResolver: liveTestModelResolver(fixture.Store, modelClient),
		ToolExecutor:  tools.Executor{Store: fixture.Store},
		Now:           func() time.Time { return now.Add(2 * time.Second) },
	}

	first := fixture.admitContentInputTurn(
		t, ctx, agentID, kernelTestUserID, "what does the checklist cover?", now.Add(time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, first); err != nil {
		t.Fatalf("execute first turn: %v", err)
	}
	releaseKernelTurn(t, ctx, fixture, first)
	second := fixture.admitContentInputTurn(
		t, ctx, agentID, kernelTestUserID, "load the checklist skill", now.Add(2*time.Second),
	)
	continuation := executeAsyncToolTurn(t, ctx, fixture, executor, second)
	releaseKernelTurn(t, ctx, fixture, continuation)
	third := fixture.admitContentInputTurn(
		t, ctx, agentID, kernelTestUserID, "thanks, that is all", now.Add(3*time.Second),
	)
	if err := executor.ExecuteModelWork(ctx, third); err != nil {
		t.Fatalf("execute third turn: %v", err)
	}

	if modelClient.preparedCount() != 4 {
		t.Fatalf("prepared requests = %d, want three turns plus one tool continuation", modelClient.preparedCount())
	}
	bundles := make([]modelcontext.Bundle, 0, len(modelClient.prepared))
	for _, snapshot := range modelClient.prepared {
		bundles = append(bundles, snapshot.Bundle)
	}
	last := bundles[len(bundles)-1]
	if !modelcontext.HasTool(last.ToolSpecs, toolcatalog.ToolNameSkill) ||
		!modelcontext.HasTool(last.ToolSpecs, toolcatalog.ToolNameSendIntegrationMessage) ||
		!modelcontext.HasTool(last.ToolSpecs, "lookup_customer") ||
		len(last.AvailableMachinePools) != 1 ||
		len(last.IntegrationTargets) != 1 ||
		len(last.ToolResults) != 1 ||
		!strings.Contains(last.SystemPrompt, "<available_skills>") {
		t.Fatalf("final bundle lacks the expected tools, pool, target, tool result, or skill catalog: %+v", last)
	}

	routes := []struct {
		name   string
		client model.Client
	}{
		{name: "anthropic", client: anthropicmessages.Client{
			EndpointPath:      "/messages",
			ProviderModelSlug: "claude-sonnet-5",
		}},
		{name: "openrouter claude", client: openaichatcompletions.Client{
			EndpointPath:      "/chat/completions",
			ProviderModelSlug: "anthropic/claude-sonnet-5",
			APIVariant:        modelprotocol.APIVariantOpenRouter,
		}},
		{name: "openrouter automatic", client: openaichatcompletions.Client{
			EndpointPath:      "/chat/completions",
			ProviderModelSlug: "moonshotai/kimi-k3",
			APIVariant:        modelprotocol.APIVariantOpenRouter,
		}},
		{name: "openai chat", client: openaichatcompletions.Client{
			EndpointPath:      "/chat/completions",
			ProviderModelSlug: "gpt-test",
		}},
		{name: "openai responses", client: openairesponses.Client{EndpointPath: "/responses", ProviderModelSlug: "gpt-test"}},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			previous := modeltest.PreparePrefix(t, route.client, bundles[0])
			for index, bundle := range bundles[1:] {
				next := modeltest.PreparePrefix(t, route.client, bundle)
				if violation := modeltest.PrefixViolation(previous, next); violation != "" {
					t.Fatalf("request %d broke the cached prefix: %s", index+1, violation)
				}
				previous = next
			}
		})
	}
}

func releaseKernelTurn(t *testing.T, ctx context.Context, fixture kernelFixture, work ModelWorkExecution) {
	t.Helper()
	err := fixture.Store.Execution().ReleaseAgentRuntimeLock(ctx, work.ProjectID, work.AgentID, work.RuntimeLockID)
	if err != nil && !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("release turn runtime lock: %v", err)
	}
}

func createKernelMachinePool(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	name string,
) executionstore.MachinePoolRecord {
	t.Helper()
	providerAuthSecret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     kernelTestOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      name + "-provider-auth",
		Material:  secrets.GenericMaterial{Value: "test-token"},
		Actor:     kernelTestUserPrincipal(kernelTestUserID),
	})
	if err != nil {
		t.Fatalf("create machine pool provider auth secret: %v", err)
	}
	machinePool, err := fixture.Store.Execution().CreateMachinePool(ctx, executionstore.CreateMachinePoolInput{
		OrgID:                         kernelTestOrgID,
		Name:                          name,
		Provider:                      "test.provider",
		DefaultMachineCPU:             intPtrForKernelCompactionTest(1),
		DefaultMachineMemoryMB:        intPtrForKernelCompactionTest(1024),
		DefaultMachineProviderOptions: json.RawMessage(`{"image":"prompt-cache"}`),
		ProviderAuthSecretID:          providerAuthSecret.ID,
		MaxTotalMachines:              1,
		MaxTotalCPU:                   intPtrForKernelCompactionTest(1),
		MaxTotalMemoryMB:              intPtrForKernelCompactionTest(1024),
		MaxMachineCPU:                 intPtrForKernelCompactionTest(1),
		MaxMachineMemoryMB:            intPtrForKernelCompactionTest(1024),
	})
	if err != nil {
		t.Fatalf("create machine pool: %v", err)
	}
	if _, err := fixture.Store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:          kernelTestOrgID,
			ProjectID:      kernelTestProjectID,
			MachinePoolID:  machinePool.ID,
			IdempotencyKey: name + "-grant",
		},
	); err != nil {
		t.Fatalf("grant machine pool to project: %v", err)
	}
	return machinePool
}
