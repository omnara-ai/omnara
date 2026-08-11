//go:build integration

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

var (
	kernelTestOrgID     = kernelTestID("org")
	kernelTestProjectID = kernelTestID("project")
	kernelTestUserID    = kernelTestID("user")
	kernelTestWorkerID  = kernelTestID("worker_process_instance")
)

const kernelTestRuntimeLockLeaseDuration = time.Minute

func immediateKernelModelRetryDelay(time.Duration) time.Duration {
	return 0
}

func kernelTestClaimInput(_ time.Time) executionstore.ClaimNextAgentWorkInput {
	return executionstore.ClaimNextAgentWorkInput{
		WorkerProcessID: kernelTestWorkerID,
		LeaseDuration:   kernelTestRuntimeLockLeaseDuration,
	}
}

func kernelTestID(seed string) storage.ID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-kernel-integration:"+seed))
}

func kernelTestUserPrincipal(userID storage.ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func compactionSourceStartForKernelTest(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	contextRow executionstore.ModelCallContextRecord,
) int64 {
	t.Helper()
	checkpoint, found, err := store.Execution().GetLatestApplicableContextCheckpoint(
		ctx,
		contextRow.ProjectID,
		contextRow.AgentID,
		contextRow.InputEventSequence,
	)
	if err != nil {
		t.Fatalf("load latest checkpoint for compaction source: %v", err)
	}
	if !found {
		return 1
	}
	return checkpoint.SummarizedThroughEventSequence + 1
}

func kernelTestKeyWrapper(t *testing.T) secrets.KeyWrapper {
	t.Helper()
	wrapper, err := secrets.NewLocalKeyWrapper(
		"kernel-test-key",
		map[string][]byte{"kernel-test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create kernel test key wrapper: %v", err)
	}
	return wrapper
}

func kernelTestOmnaraActorParams(t *testing.T, userID storage.ID) *executionstore.ActorParams {
	t.Helper()
	params, err := executionstore.OmnaraActorParams(kernelTestOrgID, kernelTestUserPrincipal(userID))
	if err != nil {
		t.Fatalf("create Omnara actor params: %v", err)
	}
	return params
}

type kernelFixture struct {
	Pool  *pgxpool.Pool
	Store *storage.Store
	Now   time.Time
}

type kernelConfiguredModelOptions struct {
	ContextWindowTokens *int
	MaxOutputTokens     *int
}

func newKernelFixture(t *testing.T, ctx context.Context) kernelFixture {
	t.Helper()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id, display_name, created_at, updated_at)
		VALUES ($1, 'Kernel Test User', $2, $2)
	`, kernelTestUserID, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO orgs(id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, 'Kernel Test Org', 'kernel-test-org', $2, $2)
	`, kernelTestOrgID, now); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, 'Kernel Test Project', 'kernel-test-project', $3, $3)
	`, kernelTestProjectID, kernelTestOrgID, now); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	store := storage.NewStore(
		pool,
		storage.WithSecretKeyWrapper(kernelTestKeyWrapper(t)),
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
		storage.WithModelCallRetryBackoff(func(int, string) time.Duration { return 0 }),
	)
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: kernelTestOrgID, UserID: kernelTestUserID, Role: "admin"},
	); err != nil {
		t.Fatalf("seed org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     kernelTestOrgID,
			ProjectID: kernelTestProjectID,
			UserID:    kernelTestUserID,
			Role:      "operator",
		},
	); err != nil {
		t.Fatalf("seed project membership: %v", err)
	}
	return kernelFixture{Pool: pool, Store: store, Now: now}
}

func (f kernelFixture) createAgent(
	t *testing.T,
	ctx context.Context,
	modelSelection string,
	now time.Time,
	tools ...string,
) (storage.ID, storage.ID) {
	t.Helper()
	return f.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Kernel Test",
		modelSelection,
		now,
		kernelConfiguredModelOptions{},
		tools...,
	)
}

func (f kernelFixture) createAgentWithModelOptions(
	t *testing.T,
	ctx context.Context,
	modelSelection string,
	now time.Time,
	modelOptions kernelConfiguredModelOptions,
	tools ...string,
) (storage.ID, storage.ID) {
	t.Helper()
	return f.createNamedAgentWithModelOptions(
		t,
		ctx,
		"Kernel Test",
		modelSelection,
		now,
		modelOptions,
		tools...,
	)
}

func (f kernelFixture) createNamedAgentWithModelOptions(
	t *testing.T,
	ctx context.Context,
	name, modelSelection string,
	now time.Time,
	modelOptions kernelConfiguredModelOptions,
	tools ...string,
) (storage.ID, storage.ID) {
	t.Helper()
	providerConfigBaseName, configuredModelName, ok := strings.Cut(modelSelection, "/")
	if !ok {
		t.Fatalf("kernel test model selection %q must be provider-config-base/configured-model-name", modelSelection)
	}
	providerConfigName := providerConfigBaseName + "-prod"
	agentProfileIdempotencyKey := "kernel-agent-" + providerConfigName + "-" + configuredModelName + "-" + now.Format(
		time.RFC3339Nano,
	)
	launchIdempotencyKey := agentProfileIdempotencyKey + "-launch"
	sourceYAML := "name: " + name + "\ninstruction: Help the user make progress.\nmodel:\n  provider_config: " + providerConfigName + "\n  name: " + configuredModelName + "\n"
	if len(tools) > 0 {
		sourceYAML += "tools:\n"
		sort.Strings(tools)
		for _, name := range tools {
			sourceYAML += "  " + name + ": {}\n"
		}
	}
	profileRecord := f.createConfigAndProfileBookmarkWithModelOptions(
		t,
		ctx,
		name,
		agentProfileIdempotencyKey,
		sourceYAML,
		now,
		modelOptions,
	)
	launch, err := f.Store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      kernelTestProjectID,
			ProfileID:      profileRecord.ID,
			AgentConfigID:  profileRecord.CurrentConfigID,
			LaunchedBy:     kernelTestUserPrincipal(kernelTestUserID),
			IdempotencyKey: launchIdempotencyKey,
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	return launch.Agent.ID, kernelTestUserID
}

func (f kernelFixture) createConfigAndProfileBookmark(
	t *testing.T,
	ctx context.Context,
	name, idempotencyKey, sourceYAML string,
	now time.Time,
) executionstore.AgentProfileRecord {
	t.Helper()
	return f.createConfigAndProfileBookmarkWithModelOptions(
		t,
		ctx,
		name,
		idempotencyKey,
		sourceYAML,
		now,
		kernelConfiguredModelOptions{},
	)
}

func (f kernelFixture) createConfigAndProfileBookmarkWithModelOptions(
	t *testing.T,
	ctx context.Context,
	name, idempotencyKey, sourceYAML string,
	now time.Time,
	modelOptions kernelConfiguredModelOptions,
) executionstore.AgentProfileRecord {
	t.Helper()
	compiled := f.compileAgentYAMLResolvedWithModelOptions(t, ctx, sourceYAML, now, modelOptions)
	config, err := f.Store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               kernelTestProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config: %v", err)
	}
	profileRecord, err := f.Store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       kernelTestProjectID,
		Name:            name,
		CurrentConfigID: config.ID,
		IdempotencyKey:  idempotencyKey,
	})
	if err != nil {
		t.Fatalf("create agent profile: %v", err)
	}
	return profileRecord
}

func (f kernelFixture) compileAgentYAMLResolved(
	t *testing.T,
	ctx context.Context,
	sourceYAML string,
	now time.Time,
) agentconfig.Result {
	t.Helper()
	return f.compileAgentYAMLResolvedWithModelOptions(t, ctx, sourceYAML, now, kernelConfiguredModelOptions{})
}

func (f kernelFixture) compileAgentYAMLResolvedWithModelOptions(
	t *testing.T,
	ctx context.Context,
	sourceYAML string,
	now time.Time,
	modelOptions kernelConfiguredModelOptions,
) agentconfig.Result {
	t.Helper()
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source: %v", err)
	}
	configuredModel := f.ensureModelSelection(t, ctx, source.Model.ProviderConfig, source.Model.Name, now, modelOptions)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedKernelAgentConfigModel(configuredModel), nil
		},
		ResolveSkillID: func(skillID string) (agentconfig.SkillResolution, error) {
			records, _, err := f.Store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
				OrgID:     kernelTestOrgID,
				ProjectID: kernelTestProjectID,
				IDs:       []string{skillID},
			})
			if err != nil {
				return agentconfig.SkillResolution{}, err
			}
			if len(records) != 1 {
				return agentconfig.SkillResolution{}, storeerr.ErrNotFound
			}
			return agentconfig.SkillResolution{PublicID: skillID, Name: records[0].Name}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent config: %v", err)
	}
	return compiled
}

func resolvedKernelAgentConfigModel(configuredModel modelstore.ConfiguredModelRecord) agentconfig.ResolvedModelSelection {
	supportsTools := configuredModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}
}

func (f kernelFixture) ensureModelSelection(
	t *testing.T,
	ctx context.Context,
	providerConfigName, configuredModelName string,
	now time.Time,
	options kernelConfiguredModelOptions,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	providerConfig, err := f.Store.Models().GetModelProviderConfigByName(ctx, kernelTestOrgID, providerConfigName)
	if err != nil {
		if !storeerr.IsNotFound(err) {
			t.Fatalf("load model provider config %q: %v", providerConfigName, err)
		}
		secret, err := f.ensureProviderCredential(t, ctx, providerConfigName, now)
		if err != nil {
			t.Fatalf("ensure provider credential: %v", err)
		}
		providerConfig, err = f.Store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
			OrgID:              kernelTestOrgID,
			Name:               providerConfigName,
			APIFormat:          modelprotocol.APIFormatOpenAIResponses,
			APIVariant:         "default",
			BaseURL:            "https://api.openai.com/v1",
			CredentialSecretID: secret.ID,
		})
		if err != nil {
			t.Fatalf("create model provider config %q: %v", providerConfigName, err)
		}
	}
	if providerConfig.ManagementKind == management.Cluster {
		configuredModel, err := f.Store.Models().GetConfiguredModelByName(
			ctx,
			kernelTestOrgID,
			providerConfig.ID,
			configuredModelName,
		)
		if err != nil {
			t.Fatalf("load cluster configured model %s/%s: %v", providerConfigName, configuredModelName, err)
		}
		return configuredModel
	}
	configuredModel, err := f.Store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 kernelTestOrgID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  configuredModelName,
		ProviderModelSlug:     configuredModelName,
		ContextWindowTokens:   firstKernelTestInt(options.ContextWindowTokens, 128000),
		MaxOutputTokens:       firstKernelTestInt(options.MaxOutputTokens, 8192),
	})
	if err != nil {
		t.Fatalf("create configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	if _, err := f.Store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             kernelTestOrgID,
		ProjectID:         kernelTestProjectID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant configured model %s/%s: %v", providerConfigName, configuredModelName, err)
	}
	return configuredModel
}

func (f kernelFixture) provisionClusterModel(
	t *testing.T,
	ctx context.Context,
	providerConfigName, configuredModelName string,
) {
	t.Helper()
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cluster model provisioning: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	credential, _, err := f.Store.Secrets().CreateTx(ctx, tx, secretstore.CreateSecretInput{
		OrgID:          kernelTestOrgID,
		ManagementKind: management.Cluster,
		OwnerKind:      secretstore.SecretOwnerOrg,
		Name:           providerConfigName + "-credential",
		Material:       secrets.GenericMaterial{Value: "test-key"},
		Metadata:       resourcemeta.Metadata{},
		Actor:          kernelTestUserPrincipal(kernelTestUserID),
	})
	if err != nil {
		t.Fatalf("provision cluster model credential: %v", err)
	}
	if err := f.Store.Models().ProvisionDefaultTx(
		ctx,
		tx,
		kernelTestOrgID,
		kernelTestProjectID,
		kernelTestUserID,
		credential.ID,
		modelstore.DefaultModelProviderTemplate{
			Provisioner:          "kernel-test",
			Name:                 providerConfigName,
			CredentialSecretName: credential.Name,
			APIFormat:            modelprotocol.APIFormatOpenAIResponses,
			BaseURL:              "https://api.openai.com/v1",
			AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
			Models: []modelstore.DefaultConfiguredModelTemplate{{
				Name:                configuredModelName,
				ProviderModelSlug:   configuredModelName,
				ContextWindowTokens: 128000,
				MaxOutputTokens:     8192,
			}},
		},
	); err != nil {
		t.Fatalf("provision cluster model: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cluster model provisioning: %v", err)
	}
}

func (f kernelFixture) setManagedWorkAdmission(
	t *testing.T,
	ctx context.Context,
	allowed bool,
) {
	t.Helper()
	if _, err := f.Pool.Exec(ctx, `
INSERT INTO org_managed_work_admission(org_id, new_managed_work_allowed)
VALUES ($1, $2)
ON CONFLICT (org_id) DO UPDATE
SET new_managed_work_allowed = EXCLUDED.new_managed_work_allowed
`, kernelTestOrgID, allowed); err != nil {
		t.Fatalf("set managed work admission to %v: %v", allowed, err)
	}
}

func firstKernelTestInt(value *int, fallback int) int {
	if value != nil {
		return *value
	}
	return fallback
}

func (f kernelFixture) ensureProviderCredential(
	t *testing.T,
	ctx context.Context,
	providerConfigName string,
	now time.Time,
) (secretstore.SecretRecord, error) {
	t.Helper()
	name := "kernel-provider-" + providerConfigName
	secret, err := f.Store.Secrets().GetSecretByOwnerName(
		ctx,
		kernelTestOrgID,
		secretstore.SecretOwnerOrg,
		storage.NilID,
		storage.NilID,
		name,
	)
	if err == nil {
		return secret, nil
	}
	if !storeerr.IsNotFound(err) {
		return secretstore.SecretRecord{}, err
	}
	secret, _, err = f.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     kernelTestOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      name,
		Material:  secrets.GenericMaterial{Value: "test-key"},
		Actor:     kernelTestUserPrincipal(kernelTestUserID),
	})
	return secret, err
}

func parseConfiguredModelID(t *testing.T, compiled agentconfig.Result) storage.ID {
	t.Helper()
	id, err := storage.ParseID(compiled.Compiled.Model.ConfiguredModelID)
	if err != nil {
		t.Fatalf("parse compiled configured model id: %v", err)
	}
	return id
}

func configuredModelIDForKernelConfig(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	config executionstore.AgentConfigRecord,
) storage.ID {
	t.Helper()
	return currentConfiguredModelForKernelConfig(t, ctx, store, config).ID
}

func currentRevisionIDForKernelConfig(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	config executionstore.AgentConfigRecord,
) storage.ID {
	t.Helper()
	return currentRevisionIDForKernelConfiguredModelID(t, ctx, store, config.ConfiguredModelID)
}

func currentProjectModelGrantIDForKernelConfig(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	config executionstore.AgentConfigRecord,
) storage.ID {
	t.Helper()
	return currentProjectModelGrantIDForKernelConfiguredModelID(
		t,
		ctx,
		store,
		config.OrgID,
		config.ProjectID,
		config.ConfiguredModelID,
	)
}

func currentProjectModelGrantIDForKernelConfiguredModelID(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID storage.ID,
	projectID storage.ID,
	configuredModelID storage.ID,
) storage.ID {
	t.Helper()
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(ctx, orgID, projectID, configuredModelID)
	if err != nil {
		t.Fatalf("load project model grant for configured model %s: %v", configuredModelID, err)
	}
	return grant.ID
}

func currentRevisionIDForKernelConfiguredModelID(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	configuredModelID storage.ID,
) storage.ID {
	t.Helper()
	configuredModel, err := store.Models().GetConfiguredModel(ctx, kernelTestOrgID, configuredModelID)
	if err != nil {
		t.Fatalf("load configured model %s: %v", configuredModelID, err)
	}
	return configuredModel.CurrentRevisionID
}

func currentConfiguredModelForKernelConfig(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	config executionstore.AgentConfigRecord,
) modelstore.ConfiguredModelRecord {
	t.Helper()
	configuredModel, err := store.Models().GetConfiguredModel(ctx, kernelTestOrgID, config.ConfiguredModelID)
	if err != nil {
		t.Fatalf("load configured model %s: %v", config.ConfiguredModelID, err)
	}
	return configuredModel
}

func (f kernelFixture) currentAgentConfig(
	t *testing.T,
	ctx context.Context,
	agentID storage.ID,
) executionstore.AgentConfigRecord {
	t.Helper()
	agent, err := f.Store.Execution().GetAgentInProject(ctx, kernelTestProjectID, agentID)
	if err != nil {
		t.Fatalf("load agent %s: %v", agentID, err)
	}
	config, found, err := f.Store.Execution().GetAgentConfig(ctx, kernelTestProjectID, agent.CurrentConfigID)
	if err != nil || !found {
		t.Fatalf("load current agent config %s: found=%v err=%v", agent.CurrentConfigID, found, err)
	}
	return config
}

func (f kernelFixture) admitContentInputTurn(
	t *testing.T,
	ctx context.Context,
	agentID, userID storage.ID,
	text string,
	now time.Time,
) ModelWorkExecution {
	t.Helper()
	input, _, _, err := f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      kernelTestProjectID,
		AgentID:        agentID,
		Actor:          kernelTestOmnaraActorParams(t, userID),
		ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": text}}),
		IdempotencyKey: "kernel-input-" + agentID.String() + "-" + text,
	})
	if err != nil {
		t.Fatalf("create agent input: %v", err)
	}
	claim, found, err := f.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(now.Add(2*time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("claim input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel {
		t.Fatalf("content input %s was not admitted", input.ID)
	}
	admitted := claim.Model.AdmittedInputTurn
	lock := claim.RuntimeLock
	return ModelWorkExecution{
		Kind:                 executionstore.ModelWorkStart,
		OrgID:                claim.OrgID,
		ProjectID:            kernelTestProjectID,
		AgentID:              agentID,
		TurnID:               admitted.Turn.ID,
		InputIDs:             []storage.ID{input.ID},
		OpeningEventSequence: claim.Model.OpeningEventSequence,
		RuntimeLockID:        lock.ID,
		Now:                  now.Add(3 * time.Millisecond),
	}
}

func modelWorkExecutionFromClaimForKernelTest(claim executionstore.ClaimedAgentWork, now time.Time) ModelWorkExecution {
	return ModelWorkExecution{
		Kind:                     claim.Model.Kind,
		OrgID:                    claim.OrgID,
		ProjectID:                claim.ProjectID,
		AgentID:                  claim.AgentID,
		ModelCallContextID:       claim.Model.ModelCallContextID,
		SourceModelCallContextID: claim.Model.SourceModelCallContextID,
		SourceModelOutputID:      claim.Model.SourceModelOutputID,
		TurnID:                   claim.Model.TurnID,
		InputIDs:                 claim.Model.InputIDs,
		OpeningEventSequence:     claim.Model.OpeningEventSequence,
		RuntimeLockID:            claim.RuntimeLock.ID,
		Now:                      now,
	}
}

func nextToolWorkExecution(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	prior ModelWorkExecution,
) ToolWorkExecution {
	t.Helper()
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		prior.ProjectID,
		prior.AgentID,
		prior.RuntimeLockID,
	); err != nil {
		t.Fatalf("release model-work runtime: %v", err)
	}
	claim := claimNextAgentWorkForKernelTest(
		t,
		ctx,
		fixture,
		prior.AgentID,
		executionstore.AgentWorkTool,
		prior.Now.Add(time.Second),
	)
	return ToolWorkExecution{
		ProjectID:          claim.ProjectID,
		AgentID:            claim.AgentID,
		TurnID:             claim.Tool.TurnID,
		ModelCallContextID: claim.Tool.ModelCallContextID,
		ModelOutputID:      claim.Tool.ModelOutputID,
		SourceEventID:      claim.Tool.SourceEventID,
		RuntimeLockID:      claim.RuntimeLock.ID,
		Now:                prior.Now.Add(2 * time.Second),
	}
}

func executeNextToolWork(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	executor AgentExecutor,
	prior ModelWorkExecution,
) *tools.AsyncExecutionScope {
	t.Helper()
	work := nextToolWorkExecution(t, ctx, fixture, prior)
	scope := tools.NewAsyncExecutionScope(nil)
	if err := executor.ExecuteToolWork(
		tools.WithAsyncExecutionScope(ctx, scope),
		work,
	); err != nil {
		scope.Seal()
		t.Fatalf("execute claimed tool work: %v", err)
	}
	scope.Seal()
	release := func() {
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			context.Background(),
			work.ProjectID,
			work.AgentID,
			work.RuntimeLockID,
		); err != nil && !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
			t.Errorf("release tool-work runtime: %v", err)
		}
	}
	if scope.Started() {
		go func() {
			<-scope.Done()
			release()
		}()
	} else {
		release()
	}
	return scope
}

func executeNextModelWork(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	executor AgentExecutor,
	prior ModelWorkExecution,
) ModelWorkExecution {
	t.Helper()
	claim := claimNextAgentWorkForKernelTest(
		t,
		ctx,
		fixture,
		prior.AgentID,
		executionstore.AgentWorkModel,
		prior.Now.Add(3*time.Second),
	)
	work := modelWorkExecutionFromClaimForKernelTest(claim, prior.Now.Add(4*time.Second))
	if err := executor.ExecuteModelWork(ctx, work); err != nil {
		t.Fatalf("execute claimed model work: %v", err)
	}
	return work
}

func claimNextAgentWorkForKernelTest(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	agentID storage.ID,
	kind executionstore.AgentWorkKind,
	now time.Time,
) executionstore.ClaimedAgentWork {
	t.Helper()
	const retryInterval = 25 * time.Millisecond
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		claimAt := now
		if wallNow := time.Now().UTC(); claimAt.Before(wallNow) {
			claimAt = wallNow.Add(time.Second)
		}
		claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
			ctx,
			kernelTestClaimInput(claimAt),
		)
		if errors.Is(err, storeerr.ErrNoClaimableAgentWakeup) || (err == nil && !found) {
			time.Sleep(retryInterval)
			continue
		}
		if err != nil {
			t.Fatalf("claim next agent work: %v", err)
		}
		if !found || claim.Kind == executionstore.AgentWorkNone {
			time.Sleep(retryInterval)
			continue
		}
		if claim.AgentID != agentID || claim.Kind != kind {
			t.Fatalf(
				"claimed agent work = %+v, want agent %s kind %d",
				claim,
				agentID,
				kind,
			)
		}
		return claim
	}
	t.Fatalf("timed out claiming agent %s work kind %d", agentID, kind)
	return executionstore.ClaimedAgentWork{}
}

func executeAsyncToolTurn(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	executor AgentExecutor,
	input ModelWorkExecution,
) ModelWorkExecution {
	t.Helper()
	if err := executor.ExecuteModelWork(ctx, input); err != nil {
		t.Fatalf("execute model work producing async tool call: %v", err)
	}
	scope := executeNextToolWork(t, ctx, fixture, executor, input)
	select {
	case <-scope.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("async tool work did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("execute async tool work: %v", err)
	}
	return executeNextModelWork(t, ctx, fixture, executor, input)
}

func continueTurnOnNewLeaseForKernelTest(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	prior ModelWorkExecution,
	now time.Time,
) ModelWorkExecution {
	t.Helper()
	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		prior.ProjectID,
		prior.AgentID,
		prior.RuntimeLockID,
	); err != nil {
		t.Fatalf("release prior runtime lease: %v", err)
	}
	claimAt := now
	if wallNow := time.Now().UTC(); claimAt.Before(wallNow) {
		claimAt = wallNow.Add(time.Second)
	}
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(claimAt),
	)
	if err != nil {
		var wakeups, checkpoints, checkpointFrontiers, activeContexts int
		var contexts string
		_ = fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, prior.ProjectID, prior.AgentID).Scan(&wakeups)
		_ = fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM context_checkpoints checkpoint JOIN agents agent ON agent.id = checkpoint.agent_id WHERE agent.project_id = $1 AND checkpoint.agent_id = $2`, prior.ProjectID, prior.AgentID).Scan(&checkpoints)
		_ = fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM agent_unconsumed_context_checkpoint_frontiers($1, $2)`, prior.ProjectID, prior.AgentID).Scan(&checkpointFrontiers)
		_ = fixture.Pool.QueryRow(ctx, `SELECT count(*) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2 AND state = 'started'`, prior.ProjectID, prior.AgentID).Scan(&activeContexts)
		_ = fixture.Pool.QueryRow(ctx, `
			SELECT coalesce(string_agg(
				context.operation_kind || ':' || context.state || ':' ||
				coalesce(context.error_code, '') || ':' || coalesce(context.recovery_kind, ''),
				', ' ORDER BY context.created_at, context.attempt_number
			), '')
			FROM model_call_contexts context
			WHERE context.project_id = $1 AND context.agent_id = $2`, prior.ProjectID, prior.AgentID).Scan(&contexts)
		t.Fatalf(
			"claim continuation on new lease: %v (wakeups=%d checkpoints=%d checkpoint_frontiers=%d active_contexts=%d contexts=%q)",
			err,
			wakeups,
			checkpoints,
			checkpointFrontiers,
			activeContexts,
			contexts,
		)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || claim.AgentID != prior.AgentID {
		t.Fatalf("continuation claim = %+v found=%v, want executable agent %s", claim, found, prior.AgentID)
	}
	return modelWorkExecutionFromClaimForKernelTest(claim, now)
}

func assertDurableModelErrorForKernelTest(
	t *testing.T,
	ctx context.Context,
	fixture kernelFixture,
	agentID, turnID storage.ID,
	errorKind, errorCode string,
) {
	t.Helper()
	var count int
	if err := fixture.Pool.QueryRow(ctx, `
		SELECT count(*)
			FROM model_outputs output
			JOIN agent_events event
			  ON event.agent_id = output.agent_id
		 AND event.model_output_id = output.id
			JOIN content_blocks block
			  ON block.agent_id = output.agent_id
		 AND block.owner_model_output_id = output.id
			JOIN model_call_contexts context
			  ON context.agent_id = output.agent_id
		 AND context.id = output.model_call_context_id
			WHERE context.project_id = $1
			  AND output.agent_id = $2
		  AND event.turn_id = $3
		  AND event.event_kind = 'model_output'
		  AND output.stop_reason = 'error'
		  AND block.block_kind = 'error'
		  AND block.text_content <> ''
		  AND context.state = 'failed'
		  AND context.recovery_kind IS NULL
		  AND context.error_kind = $5
		  AND context.error_code = $4`,
		kernelTestProjectID,
		agentID,
		turnID,
		errorCode,
		errorKind,
	).Scan(&count); err != nil {
		t.Fatalf("count durable model error %s/%s: %v", errorKind, errorCode, err)
	}
	if count != 1 {
		t.Fatalf("durable model error %s/%s count = %d, want 1", errorKind, errorCode, count)
	}
}

type sequenceKernelModel struct {
	providerModelSlug           string
	apiFormat                   modelprotocol.APIFormat
	capabilities                model.Capabilities
	preparedInputTokenEstimate  int
	preparedInputTokenEstimates []int
	preparedInputTokenEstimator func(modelcontext.Bundle) int
	prepareErr                  error
	afterPrepare                func()
	mu                          sync.Mutex
	prepared                    []modelcontextSnapshot
	responded                   []modelcontextSnapshot
	respondHadSink              []bool
	responses                   []model.Response
	errorResponses              []model.Response
	errs                        []error
	afterRespond                func(model.Response)
	streamEvents                []model.StreamEvent
}

type modelcontextSnapshot struct {
	Messages           int
	ToolResults        int
	ContextCheckpoints int
	ToolSpecs          []modelcontext.ToolSpec
	ProviderReplays    []json.RawMessage
	Policy             model.RequestPolicy
	ProviderRequest    json.RawMessage
}

func (m *sequenceKernelModel) RequestedProviderModelSlug() string {
	return m.providerModelSlug
}

func (m *sequenceKernelModel) APIFormat() modelprotocol.APIFormat {
	if m.apiFormat != "" {
		return m.apiFormat
	}
	return modelprotocol.APIFormatOpenAIResponses
}

func (m *sequenceKernelModel) ModelAPIVariant() modelprotocol.APIVariant {
	return modelprotocol.APIVariantDefault
}

func (m *sequenceKernelModel) Capabilities() model.Capabilities {
	capabilities := m.capabilities
	if capabilities.ContextWindowTokens == 0 {
		capabilities.ContextWindowTokens = 128000
	}
	if capabilities.MaxOutputTokens == 0 {
		capabilities.MaxOutputTokens = 8192
	}
	return capabilities
}

func (m *sequenceKernelModel) Prepare(_ context.Context, input model.PrepareInput) (model.PreparedRequest, error) {
	if m.prepareErr != nil {
		return model.PreparedRequest{}, m.prepareErr
	}
	checkpointCount := 0
	if input.Context.ContextCheckpoint != nil {
		checkpointCount = 1
	}
	body := mustKernelJSON(map[string]any{
		"api_format":         m.APIFormat(),
		"model":              m.providerModelSlug,
		"messages":           input.Context.Messages,
		"tool_results":       input.Context.ToolResults,
		"context_checkpoint": input.Context.ContextCheckpoint,
	})
	providerReplays := make([]json.RawMessage, 0, len(input.Context.Messages))
	for _, message := range input.Context.Messages {
		if len(message.ProviderReplay) > 0 {
			providerReplays = append(providerReplays, append(json.RawMessage(nil), message.ProviderReplay...))
		}
	}
	m.mu.Lock()
	m.prepared = append(
		m.prepared,
		modelcontextSnapshot{
			Messages:           len(input.Context.Messages),
			ToolResults:        len(input.Context.ToolResults),
			ContextCheckpoints: checkpointCount,
			ToolSpecs:          append([]modelcontext.ToolSpec(nil), input.Context.ToolSpecs...),
			ProviderReplays:    providerReplays,
			Policy:             input.Policy,
			ProviderRequest:    body,
		},
	)
	estimate := m.preparedInputTokenEstimate
	if m.preparedInputTokenEstimator != nil {
		estimate = m.preparedInputTokenEstimator(input.Context)
	} else if len(m.preparedInputTokenEstimates) > 0 {
		estimate = m.preparedInputTokenEstimates[0]
		m.preparedInputTokenEstimates = m.preparedInputTokenEstimates[1:]
	}
	m.mu.Unlock()
	if m.afterPrepare != nil {
		m.afterPrepare()
	}
	if estimate <= 0 {
		estimate = modelcontext.EstimatePreparedRequest(body, nil)
	}
	return model.PreparedRequest{Body: body, InputTokenEstimate: estimate}, nil
}

func isCompactionRequestBundle(bundle modelcontext.Bundle) bool {
	return len(bundle.Messages) == 1 &&
		strings.HasPrefix(bundle.Messages[0].ID, "compaction_source_")
}

func (m *sequenceKernelModel) Respond(ctx context.Context, request model.Request) (model.Response, error) {
	m.mu.Lock()
	m.respondHadSink = append(m.respondHadSink, request.DeltaSink != nil)
	if len(m.prepared) > 0 {
		m.responded = append(m.responded, m.prepared[len(m.prepared)-1])
	}
	if request.DeltaSink != nil && len(m.streamEvents) > 0 {
		streamEvents := m.streamEvents
		m.streamEvents = nil
		for _, event := range streamEvents {
			request.DeltaSink.Emit(ctx, event)
		}
	}
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		if err != nil {
			response := model.Response{}
			if len(m.errorResponses) > 0 {
				response = m.errorResponses[0]
				m.errorResponses = m.errorResponses[1:]
			}
			m.mu.Unlock()
			return response, err
		}
	}
	if len(m.responses) == 0 {
		m.mu.Unlock()
		return model.Response{}, errors.New("unexpected extra model call")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	afterRespond := m.afterRespond
	m.mu.Unlock()
	if afterRespond != nil {
		afterRespond(response)
	}
	return response, nil
}

func (m *sequenceKernelModel) preparedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prepared)
}

func (m *sequenceKernelModel) respondedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.responded)
}

type selectionRecordingResolver struct {
	client     *sequenceKernelModel
	selections []model.Selection
}

type providerErrorTestResolver struct {
	err error
}

func (r providerErrorTestResolver) Resolve(
	context.Context,
	model.Selection,
) (model.ResolvedClient, error) {
	return model.ResolvedClient{}, r.err
}

func (r *selectionRecordingResolver) Resolve(
	_ context.Context,
	selection model.Selection,
) (model.ResolvedClient, error) {
	r.selections = append(r.selections, selection)
	capabilities := model.Capabilities{
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultMaxOutputTokens: 4096,
		DefaultCacheRetention:  selection.Options.CacheRetention,
		SupportsReasoning:      true,
	}
	if selection.Options.ContextWindowTokens != nil {
		capabilities.ContextWindowTokens = *selection.Options.ContextWindowTokens
	}
	if selection.Options.DefaultMaxOutputTokens != nil {
		capabilities.DefaultMaxOutputTokens = *selection.Options.DefaultMaxOutputTokens
	}
	r.client.capabilities = capabilities
	return model.ResolvedClient{
		Client:                    r.client,
		ConfiguredModelRevisionID: selection.ConfiguredModelRevisionID,
	}, nil
}

func mustKernelJSON(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return body
}

func kernelModelCallOutcomeAmbiguous(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	var details struct {
		OutcomeAmbiguous bool `json:"outcome_ambiguous"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatalf("decode model call error details: %v", err)
	}
	return details.OutcomeAmbiguous
}

type fakeKernelMCPClient struct {
	mu sync.Mutex

	agentID                 string
	initializeAgentIDs      []string
	lastAgentID             string
	protocolVersion         string
	tools                   []*sdkmcp.Tool
	failInitializeEndpoints map[string]error
	failInitializeSequences map[string][]error
	listToolsErrors         []error
	callToolErrors          []error
	callToolResult          *sdkmcp.CallToolResult
	callToolConns           []mcp.Conn

	initializeCount int
	notifyCount     int
	listToolsCount  int
	callToolCount   int
}

func (c *fakeKernelMCPClient) Initialize(
	_ context.Context,
	conn mcp.Conn,
	clientProtocolVersion string,
) (string, mcp.InitializeResult, error) {
	c.mu.Lock()
	c.initializeCount++
	if sequence := c.failInitializeSequences[conn.EndpointURL]; len(sequence) != 0 {
		err := sequence[0]
		c.failInitializeSequences[conn.EndpointURL] = sequence[1:]
		if err != nil {
			c.mu.Unlock()
			return "", mcp.InitializeResult{}, err
		}
	}
	err := c.failInitializeEndpoints[conn.EndpointURL]
	agentID := c.agentID
	if len(c.initializeAgentIDs) != 0 {
		agentID = c.initializeAgentIDs[0]
		c.initializeAgentIDs = c.initializeAgentIDs[1:]
	}
	c.lastAgentID = agentID
	c.mu.Unlock()
	if err != nil {
		return "", mcp.InitializeResult{}, err
	}
	if conn.EndpointURL == "" {
		return "", mcp.InitializeResult{}, errors.New("empty endpoint")
	}
	if clientProtocolVersion == "" {
		return "", mcp.InitializeResult{}, errors.New("empty client protocol version")
	}
	return agentID, mcp.InitializeResult{
		ProtocolVersion:    c.protocolVersion,
		ServerInfo:         json.RawMessage(`{"name":"fake-mcp","version":"v0"}`),
		ServerCapabilities: json.RawMessage(`{}`),
	}, nil
}

func (c *fakeKernelMCPClient) Notify(_ context.Context, conn mcp.Conn, method string, _ json.RawMessage) error {
	c.mu.Lock()
	c.notifyCount++
	agentID := c.expectedAgentIDLocked()
	c.mu.Unlock()
	if method != "notifications/initialized" {
		return fmt.Errorf("unexpected notify method %q", method)
	}
	if conn.MCPSessionID != agentID || conn.ProtocolVersion != c.protocolVersion {
		return fmt.Errorf("unexpected notify conn: %+v", conn)
	}
	return nil
}

func (c *fakeKernelMCPClient) Call(context.Context, mcp.Conn, string, json.RawMessage, int64) (json.RawMessage, error) {
	return nil, errors.New("unexpected generic mcp call")
}

func (c *fakeKernelMCPClient) ListTools(_ context.Context, conn mcp.Conn, requestID int64) ([]*sdkmcp.Tool, error) {
	c.mu.Lock()
	c.listToolsCount++
	agentID := c.expectedAgentIDLocked()
	var err error
	if len(c.listToolsErrors) != 0 {
		err = c.listToolsErrors[0]
		c.listToolsErrors = c.listToolsErrors[1:]
	}
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if requestID <= 0 {
		return nil, fmt.Errorf("request id = %d, want positive", requestID)
	}
	if conn.MCPSessionID != agentID || conn.ProtocolVersion != c.protocolVersion {
		return nil, fmt.Errorf("unexpected list conn: %+v", conn)
	}
	return c.tools, nil
}

func (c *fakeKernelMCPClient) CallTool(
	_ context.Context,
	conn mcp.Conn,
	_ int64,
	_ string,
	_ json.RawMessage,
) (*sdkmcp.CallToolResult, error) {
	c.mu.Lock()
	c.callToolCount++
	c.callToolConns = append(c.callToolConns, conn)
	var err error
	if len(c.callToolErrors) != 0 {
		err = c.callToolErrors[0]
		c.callToolErrors = c.callToolErrors[1:]
	}
	result := c.callToolResult
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("unexpected mcp tool call")
	}
	return result, nil
}

func (c *fakeKernelMCPClient) expectedAgentIDLocked() string {
	if c.lastAgentID != "" {
		return c.lastAgentID
	}
	return c.agentID
}
