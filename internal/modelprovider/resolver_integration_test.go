//go:build integration

package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/patch"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

func modelProviderUserPrincipal(userID storage.ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: userID}
}

func integrationResolver(store *storage.Store) Resolver {
	return Resolver{Models: store.Models(), Secrets: store.Secrets()}
}

func TestResolverUsesClusterManagedDefaultProvider(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")

	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"cluster-resolver-test-key",
		map[string][]byte{"cluster-resolver-test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(keyWrapper))
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		DisplayName: "Cluster Resolver Tester",
		Email:       "cluster-resolver@example.com",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	template := modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "omnara-openrouter",
		CredentialSecretName: "omnara-openrouter-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://openrouter.ai/api/v1",
		EndpointPath:         "/chat/completions",
		AuthKind:             modelstore.ModelProviderAuthKindBearerToken,
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name:                "cluster-default",
			ProviderModelSlug:   "anthropic/claude-sonnet-4.5",
			ContextWindowTokens: 200000,
			MaxOutputTokens:     8192,
		}},
	}
	orgID := uuid.New()
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		OrgID:          orgID,
		UserID:         user.ID,
		Name:           "Cluster Resolver Org",
		IdempotencyKey: "cluster-resolver-org",
		DefaultModelProvider: &modelstore.ProvisionedDefaultModelProvider{
			Template:        template,
			CredentialValue: "sk-cluster-resolver",
		},
	})
	if err != nil {
		t.Fatalf("create org with cluster default provider: %v", err)
	}
	provider, err := store.Models().GetModelProviderConfigByName(
		ctx,
		created.Org.ID,
		"omnara-openrouter",
	)
	if err != nil {
		t.Fatalf("get cluster provider: %v", err)
	}
	if provider.ManagementKind != management.Cluster {
		t.Fatalf("provider management kind = %q, want cluster", provider.ManagementKind)
	}
	models, err := store.Models().ListConfiguredModels(ctx, modelstore.ListConfiguredModelsInput{
		OrgID: created.Org.ID, ProviderConfigID: provider.ID, Limit: 10,
	})
	if err != nil || len(models.Models) != 1 {
		t.Fatalf("list cluster configured models: models=%+v err=%v", models.Models, err)
	}
	configuredModel := models.Models[0]
	resolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: configuredModel.CurrentRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve cluster configured model: %v", err)
	}
	client, ok := resolved.Client.(openaichatcompletions.Client)
	if !ok {
		t.Fatalf("resolved client type = %T, want openaichatcompletions.Client", resolved.Client)
	}
	if client.BaseURL != "https://openrouter.ai/api/v1" ||
		client.EndpointPath != "/chat/completions" ||
		client.RequestedProviderModelSlug() != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("resolved cluster client fields mismatch: %+v", client)
	}
	bearer, ok := client.Auth.(route.BearerToken)
	if !ok || bearer.Token != "sk-cluster-resolver" {
		t.Fatalf("resolved cluster auth = %#v, want cluster credential", client.Auth)
	}
}

func TestResolverMaterializesBedrockAnthropicClient(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")

	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"bedrock-resolver-test-key",
		map[string][]byte{"bedrock-resolver-test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(keyWrapper))
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{
		DisplayName: "Bedrock Resolver Tester",
		Email:       "bedrock-resolver@example.com",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID:         user.ID,
		Name:           "Bedrock Resolver Org",
		IdempotencyKey: "bedrock-resolver-org",
	})
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     created.Org.ID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "bedrock-key",
		Material:  secrets.GenericMaterial{Value: "bedrock-resolver-key"},
		Actor:     modelProviderUserPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create credential secret: %v", err)
	}
	providerConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              created.Org.ID,
		Name:               "bedrock-anthropic",
		APIFormat:          modelprotocol.APIFormatAnthropicMessages,
		APIVariant:         modelprotocol.APIVariantBedrock,
		BaseURL:            "https://bedrock-mantle.us-west-2.api.aws/anthropic/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 created.Org.ID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  "claude-haiku",
		ProviderModelSlug:     "anthropic.claude-haiku-4-5",
		ContextWindowTokens:   200000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             created.Org.ID,
		ProjectID:         created.Project.ID,
		ConfiguredModelID: configuredModel.ID,
	}); err != nil {
		t.Fatalf("grant configured model: %v", err)
	}

	resolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: configuredModel.CurrentRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve Bedrock Anthropic model: %v", err)
	}
	client, ok := resolved.Client.(anthropicmessages.Client)
	if !ok {
		t.Fatalf("resolved client type = %T, want anthropicmessages.Client", resolved.Client)
	}
	if client.BaseURL != "https://bedrock-mantle.us-west-2.api.aws/anthropic/v1" ||
		client.EndpointPath != "/messages" ||
		client.RequestedProviderModelSlug() != "anthropic.claude-haiku-4-5" ||
		client.ModelAPIVariant() != modelprotocol.APIVariantBedrock {
		t.Fatalf("resolved Bedrock Anthropic client mismatch: %+v", client)
	}
	headerAuth, ok := client.Auth.(route.HeaderAuth)
	if !ok || !strings.EqualFold(headerAuth.Header, "x-api-key") || headerAuth.Value != "bedrock-resolver-key" {
		t.Fatalf("resolved Bedrock Anthropic auth = %#v, want x-api-key credential", client.Auth)
	}
	wantReplayIdentity := modelenvelope.ProviderReplayIdentity{
		ModelProviderConfigID:      providerConfig.ID.String(),
		RequestedProviderModelSlug: "anthropic.claude-haiku-4-5",
		APIFormat:                  modelprotocol.APIFormatAnthropicMessages,
		APIVariant:                 modelprotocol.APIVariantBedrock,
	}
	if got := model.ProviderReplayIdentityForClient(providerConfig.ID.String(), client); got != wantReplayIdentity {
		t.Fatalf("resolved replay identity = %+v, want %+v", got, wantReplayIdentity)
	}
}

func TestResolverMaterializesConfiguredModelRevisionAndCredential(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")

	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"resolver-test-key",
		map[string][]byte{"resolver-test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		t.Fatalf("create key wrapper: %v", err)
	}
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(keyWrapper))
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	user, err := storagetest.CreateVerifiedUser(ctx, pool, storagetest.CreateVerifiedUserInput{DisplayName: "Resolver Tester", Email: "resolver@example.com"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	created, err := store.Organizations().CreateOrgForUser(
		ctx,
		orglifecycle.CreateOrgForUserInput{
			UserID:         user.ID,
			Name:           "Resolver Org",
			IdempotencyKey: "resolver-org",
		},
	)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	credential, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     created.Org.ID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "openai-key",
		Material:  secrets.GenericMaterial{Value: "sk-resolver"},
		Actor:     modelProviderUserPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create credential secret: %v", err)
	}
	providerConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              created.Org.ID,
		Name:               "openai-prod",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://proxy.example.test/v1",
		EndpointPath:       "/custom-responses",
		RequestTimeoutMS:   45000,
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                  created.Org.ID,
		ModelProviderConfigID:  providerConfig.ID,
		Name:                   "coding-default",
		ProviderModelSlug:      "gpt-resolver",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        8192,
		DefaultCacheRetention:  modelstore.ModelCacheRetentionLong,
		DefaultMaxOutputTokens: intPtr(4096),
		SupportsReasoning:      true,
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	grantMaxOutput := 4096
	grantDefaultMaxOutput := 2048
	grantContextWindow := 64000
	grantSupportsTools := false
	grant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:                  created.Org.ID,
		ProjectID:              created.Project.ID,
		ConfiguredModelID:      configuredModel.ID,
		ContextWindowTokens:    &grantContextWindow,
		MaxOutputTokens:        &grantMaxOutput,
		DefaultMaxOutputTokens: &grantDefaultMaxOutput,
		SupportsTools:          &grantSupportsTools,
	})
	if err != nil {
		t.Fatalf("grant configured model: %v", err)
	}
	oldRevisionID := configuredModel.CurrentRevisionID

	agentContextWindow := 32000
	agentMaxOutput := 1024
	resolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: configuredModel.CurrentRevisionID.String(),
		Options: model.SelectionOptions{
			ContextWindowTokens:    &agentContextWindow,
			DefaultMaxOutputTokens: &agentMaxOutput,
			CacheRetention:         model.CacheRetentionShort,
			ReasoningEffort:        "high",
		},
	})
	if err != nil {
		t.Fatalf("resolve model: %v", err)
	}
	client := resolved.Client
	openAIClient, ok := client.(openairesponses.Client)
	if !ok {
		t.Fatalf("resolved client type = %T, want openairesponses.Client", client)
	}
	if openAIClient.BaseURL != "https://proxy.example.test/v1" || openAIClient.EndpointPath != "/custom-responses" ||
		openAIClient.RequestedProviderModelSlug() != "gpt-resolver" {
		t.Fatalf("resolved client fields mismatch: %+v", openAIClient)
	}
	if openAIClient.ModelProviderConfigID != providerConfig.ID.String() {
		t.Fatalf(
			"resolved client provider config = %s, want %s",
			openAIClient.ModelProviderConfigID,
			providerConfig.ID,
		)
	}
	if resolved.ConfiguredModelRevisionID != configuredModel.CurrentRevisionID.String() {
		t.Fatalf(
			"resolved revision = %s, want %s",
			resolved.ConfiguredModelRevisionID,
			configuredModel.CurrentRevisionID,
		)
	}
	bearer, ok := openAIClient.Auth.(route.BearerToken)
	if !ok || bearer.Token != "sk-resolver" {
		t.Fatalf("resolved auth = %#v, want bearer token from secret", openAIClient.Auth)
	}
	if openAIClient.HTTPClient == nil || openAIClient.HTTPClient.Timeout != 45*time.Second {
		t.Fatalf("resolved client timeout = %+v, want 45s", openAIClient.HTTPClient)
	}
	capabilities := openAIClient.Capabilities()
	if capabilities.ContextWindowTokens != agentContextWindow || capabilities.MaxOutputTokens != grantMaxOutput ||
		capabilities.DefaultMaxOutputTokens != agentMaxOutput ||
		capabilities.DefaultCacheRetention != model.CacheRetentionShort ||
		capabilities.DefaultReasoningEffort != "high" ||
		capabilities.SupportsTools == nil ||
		*capabilities.SupportsTools ||
		!capabilities.SupportsReasoning {
		t.Fatalf("resolved capabilities = %+v", capabilities)
	}

	replayIdentity := modelenvelope.ProviderReplayIdentity{
		ModelProviderConfigID:      providerConfig.ID.String(),
		RequestedProviderModelSlug: "gpt-resolver",
		APIFormat:                  modelprotocol.APIFormatOpenAIResponses,
		APIVariant:                 modelprotocol.APIVariantDefault,
	}
	if got := model.ProviderReplayIdentityForClient(providerConfig.ID.String(), openAIClient); got != replayIdentity {
		t.Fatalf("resolved replay identity = %+v, want %+v", got, replayIdentity)
	}
	replay := json.RawMessage(`[
		{"id":"rs_old","type":"reasoning","encrypted_content":"enc_old_credential"},
		{
			"type":"function_call","id":"fc_old","call_id":"call_1","name":"run_command",
			"arguments":"{\"command\":\"true\"}","status":"completed"
		}
	]`)
	replayBundle := modelcontext.Bundle{
		Messages: []modelcontext.Message{{
			Role:                 modelprotocol.RoleAssistant,
			Sequence:             1,
			ModelCallContextID:   "mcc_1",
			Content:              json.RawMessage(`[{"type":"tool_call","tool_call_id":"tcl_1"}]`),
			ProviderReplay:       replay,
			ProviderReplaySource: replayIdentity,
		}},
		ToolResults: []modelcontext.ToolResultRef{{
			ToolCallID:          "tcl_1",
			ModelCallContextID:  "mcc_1",
			ProviderCallID:      "call_1",
			Name:                "run_command",
			Input:               json.RawMessage(`{"command":"true"}`),
			ContentParts:        json.RawMessage(`[{"type":"text","text":"done"}]`),
			SourceEventSequence: 1,
			ResultEventSequence: 2,
		}},
	}
	preparedWithOriginalCredential, err := openAIClient.Prepare(
		ctx,
		model.PrepareInput{Context: replayBundle},
	)
	if err != nil {
		t.Fatalf("prepare provider replay with original credential: %v", err)
	}
	if !strings.Contains(string(preparedWithOriginalCredential.Body), "enc_old_credential") {
		t.Fatalf("original credential did not replay compatible provider state: %s", preparedWithOriginalCredential.Body)
	}

	rotatedCredential, rotatedVersion, err := store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:    created.Org.ID,
		SecretID: credential.ID,
		Material: secrets.GenericMaterial{Value: "sk-resolver-rotated"},
		Actor:    modelProviderUserPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("rotate credential secret: %v", err)
	}
	if rotatedCredential.CurrentVersionID != rotatedVersion.ID || rotatedVersion.ID == credential.CurrentVersionID {
		t.Fatalf(
			"rotated credential version = %s/%s, want a new current version after %s",
			rotatedCredential.CurrentVersionID,
			rotatedVersion.ID,
			credential.CurrentVersionID,
		)
	}
	resolvedAfterRotation, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: oldRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve pinned model after credential rotation: %v", err)
	}
	clientAfterRotation, ok := resolvedAfterRotation.Client.(openairesponses.Client)
	if !ok {
		t.Fatalf("rotated credential client type = %T, want openairesponses.Client", resolvedAfterRotation.Client)
	}
	rotatedBearer, ok := clientAfterRotation.Auth.(route.BearerToken)
	if !ok || rotatedBearer.Token != "sk-resolver-rotated" {
		t.Fatalf("resolved rotated auth = %#v, want rotated bearer token", clientAfterRotation.Auth)
	}
	preparedAfterRotation, err := clientAfterRotation.Prepare(ctx, model.PrepareInput{Context: replayBundle})
	if err != nil {
		t.Fatalf("prepare canonical replay after credential rotation: %v", err)
	}
	if !strings.Contains(string(preparedAfterRotation.Body), "enc_old_credential") {
		t.Fatalf("credential rotation discarded route-compatible provider replay: %s", preparedAfterRotation.Body)
	}

	updatedProviderModelSlug := "gpt-resolver-v2"
	updatedContextWindow := 256000
	updatedMaxOutput := 16384
	updatedDefaultMaxOutput := 8192
	updatedCacheRetention := modelstore.ModelCacheRetentionShort
	updatedSupportsReasoning := false
	updatedModel, err := store.Models().PatchConfiguredModel(ctx, modelstore.PatchConfiguredModelInput{
		OrgID:                  created.Org.ID,
		ModelProviderConfigID:  providerConfig.ID,
		ID:                     configuredModel.ID,
		ProviderModelSlug:      &updatedProviderModelSlug,
		ContextWindowTokens:    &updatedContextWindow,
		MaxOutputTokens:        &updatedMaxOutput,
		DefaultCacheRetention:  &updatedCacheRetention,
		DefaultMaxOutputTokens: patch.NullableInt{Set: true, Value: &updatedDefaultMaxOutput},
		SupportsReasoning:      &updatedSupportsReasoning,
	})
	if err != nil {
		t.Fatalf("update configured model: %v", err)
	}
	if updatedModel.CurrentRevisionID == oldRevisionID {
		t.Fatal("configured model update did not create a new revision")
	}

	oldResolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: oldRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve old configured model revision: %v", err)
	}
	oldClient := oldResolved.Client
	oldOpenAIClient, ok := oldClient.(openairesponses.Client)
	if !ok {
		t.Fatalf("resolved old revision client type = %T, want openairesponses.Client", oldClient)
	}
	oldCapabilities := oldOpenAIClient.Capabilities()
	if oldOpenAIClient.RequestedProviderModelSlug() != "gpt-resolver" ||
		oldResolved.ConfiguredModelRevisionID != oldRevisionID.String() ||
		oldCapabilities.MaxOutputTokens != grantMaxOutput ||
		oldCapabilities.DefaultMaxOutputTokens != grantDefaultMaxOutput ||
		oldCapabilities.SupportsReasoning != true {
		t.Fatalf(
			"resolved old revision did not preserve pinned facts: resolved=%+v client=%+v capabilities=%+v",
			oldResolved,
			oldOpenAIClient,
			oldCapabilities,
		)
	}

	currentResolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: updatedModel.CurrentRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve current configured model revision: %v", err)
	}
	currentClient := currentResolved.Client
	currentOpenAIClient, ok := currentClient.(openairesponses.Client)
	if !ok {
		t.Fatalf("resolved current revision client type = %T, want openairesponses.Client", currentClient)
	}
	currentCapabilities := currentOpenAIClient.Capabilities()
	if currentOpenAIClient.RequestedProviderModelSlug() != "gpt-resolver-v2" ||
		currentCapabilities.MaxOutputTokens != grantMaxOutput ||
		currentCapabilities.DefaultMaxOutputTokens != grantDefaultMaxOutput ||
		currentCapabilities.SupportsReasoning != false {
		t.Fatalf("resolved current revision mismatch: client=%+v capabilities=%+v", currentOpenAIClient, currentCapabilities)
	}
	preparedForNewModel, err := currentOpenAIClient.Prepare(ctx, model.PrepareInput{Context: replayBundle})
	if err != nil {
		t.Fatalf("prepare canonical fallback for changed model: %v", err)
	}
	if body := string(preparedForNewModel.Body); strings.Contains(body, "enc_old_credential") ||
		!strings.Contains(body, `"call_id":"call_1"`) {
		t.Fatalf("changed model reused incompatible replay instead of canonical history: %s", body)
	}

	crossFormatClient := openaichatcompletions.Client{
		ModelProviderConfigID: providerConfig.ID.String(),
		EndpointPath:          "/chat/completions",
		ProviderModelSlug:     "gpt-resolver",
	}
	preparedForNewFormat, err := crossFormatClient.Prepare(ctx, model.PrepareInput{Context: replayBundle})
	if err != nil {
		t.Fatalf("prepare canonical fallback for changed API format: %v", err)
	}
	if body := string(preparedForNewFormat.Body); strings.Contains(body, "enc_old_credential") ||
		!strings.Contains(body, `"id":"call_1"`) {
		t.Fatalf("changed API format reused incompatible replay instead of canonical history: %s", body)
	}

	if _, err := store.Models().DeleteProjectModelGrant(
		ctx,
		created.Org.ID,
		created.Project.ID,
		grant.ID,
	); err != nil {
		t.Fatalf("revoke original configured model grant: %v", err)
	}
	replacementMaxOutput := 3072
	replacementDefaultMaxOutput := 1536
	_, err = store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:                  created.Org.ID,
		ProjectID:              created.Project.ID,
		ConfiguredModelID:      configuredModel.ID,
		MaxOutputTokens:        &replacementMaxOutput,
		DefaultMaxOutputTokens: &replacementDefaultMaxOutput,
	})
	if err != nil {
		t.Fatalf("create replacement configured model grant: %v", err)
	}
	replacedResolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: oldRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve pinned revision with replacement grant: %v", err)
	}
	replacedClient, ok := replacedResolved.Client.(openairesponses.Client)
	if !ok {
		t.Fatalf("replacement resolved client type = %T, want openairesponses.Client", replacedResolved.Client)
	}
	replacedCapabilities := replacedClient.Capabilities()
	if replacedClient.RequestedProviderModelSlug() != "gpt-resolver" ||
		replacedCapabilities.MaxOutputTokens != replacementMaxOutput ||
		replacedCapabilities.DefaultMaxOutputTokens != replacementDefaultMaxOutput {
		t.Fatalf(
			"replacement grant was not applied to pinned revision: resolved=%+v capabilities=%+v",
			replacedResolved,
			replacedCapabilities,
		)
	}

	otherModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 created.Org.ID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  "other-grant-model",
		ProviderModelSlug:     "gpt-other-grant",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create other configured model: %v", err)
	}
	otherGrant, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             created.Org.ID,
		ProjectID:         created.Project.ID,
		ConfiguredModelID: otherModel.ID,
	})
	if err != nil {
		t.Fatalf("grant other configured model: %v", err)
	}
	resolvedWithOtherGrant, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: oldRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve pinned revision while another model is granted: %v", err)
	}
	resolvedWithOtherGrantClient, ok := resolvedWithOtherGrant.Client.(openairesponses.Client)
	if !ok || resolvedWithOtherGrantClient.Capabilities().MaxOutputTokens != replacementMaxOutput {
		t.Fatalf("unrelated grant changed resolved capabilities: %+v", resolvedWithOtherGrant.Client)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, created.Org.ID, created.Project.ID, otherGrant.ID); err != nil {
		t.Fatalf("revoke other configured model grant: %v", err)
	}
	_, err = integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: otherModel.CurrentRevisionID.String(),
	})
	if !errors.Is(err, storeerr.ErrModelGrantUnavailable) {
		t.Fatalf("resolve unavailable project model grant error = %v, want ErrModelGrantUnavailable", err)
	}

	imageOnlyModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 created.Org.ID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  "image-only-grant-model",
		ProviderModelSlug:     "gpt-image-only-grant",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
	})
	if err != nil {
		t.Fatalf("create image-only grant configured model: %v", err)
	}
	_, err = store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             created.Org.ID,
		ProjectID:         created.Project.ID,
		ConfiguredModelID: imageOnlyModel.ID,
		InputModalities:   []string{"image"},
		OutputModalities:  []string{"text"},
	})
	if err != nil {
		t.Fatalf("grant image-only configured model: %v", err)
	}
	imageOnlyResolved, err := integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: imageOnlyModel.CurrentRevisionID.String(),
	})
	if err != nil {
		t.Fatalf("resolve image-only effective modality: %v", err)
	}
	imageOnlyCapabilities := model.CapabilitiesForClient(imageOnlyResolved.Client)
	if len(imageOnlyCapabilities.InputModalities) != 1 || imageOnlyCapabilities.InputModalities[0] != "image" {
		t.Fatalf("resolved image-only input modalities = %+v, want [image]", imageOnlyCapabilities.InputModalities)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE configured_models SET deleted_at = $1, updated_at = $1 WHERE org_id = $2 AND id = $3`,
		now.Add(6*time.Second),
		created.Org.ID,
		configuredModel.ID,
	); err != nil {
		t.Fatalf("force archive configured model: %v", err)
	}
	_, err = integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: oldRevisionID.String(),
	})
	if !storeerr.IsNotFound(err) {
		t.Fatalf("resolve archived configured model revision error = %v, want not found", err)
	}

	providerArchiveConfig, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID:              created.Org.ID,
		Name:               "openai-provider-archived",
		APIFormat:          modelprotocol.APIFormatOpenAIResponses,
		BaseURL:            "https://provider-archived.example.test/v1",
		CredentialSecretID: credential.ID,
	})
	if err != nil {
		t.Fatalf("create provider archive config: %v", err)
	}
	providerArchiveModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 created.Org.ID,
		ModelProviderConfigID: providerArchiveConfig.ID,
		Name:                  "provider-archived-model",
		ProviderModelSlug:     "gpt-provider-archived",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       8192,
	})
	if err != nil {
		t.Fatalf("create provider archive configured model: %v", err)
	}
	_, err = store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             created.Org.ID,
		ProjectID:         created.Project.ID,
		ConfiguredModelID: providerArchiveModel.ID,
	})
	if err != nil {
		t.Fatalf("grant provider archive configured model: %v", err)
	}
	// Public provider archive is blocked while active configured models exist;
	// force the state to prove the resolver's fail-closed predicate.
	if _, err := pool.Exec(
		ctx,
		`UPDATE model_provider_configs SET deleted_at = $1, updated_at = $1 WHERE org_id = $2 AND id = $3`,
		now.Add(9*time.Second),
		created.Org.ID,
		providerArchiveConfig.ID,
	); err != nil {
		t.Fatalf("force archive provider config: %v", err)
	}
	_, err = integrationResolver(store).Resolve(ctx, model.Selection{
		OrgID:                     created.Org.ID.String(),
		ProjectID:                 created.Project.ID.String(),
		ConfiguredModelRevisionID: providerArchiveModel.CurrentRevisionID.String(),
	})
	if !storeerr.IsNotFound(err) {
		t.Fatalf("resolve revision under archived provider config error = %v, want not found", err)
	}
}
