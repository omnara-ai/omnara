import type { ConfiguredModel, ModelProviderConfig, OmnaraClient } from '@omnara/sdk'
import { bearerToken, createOmnaraClient, sdk } from '@omnara/sdk'

import { ensureOrg, reasoningEfforts } from './bootstrap.js'
import { loadEnv } from './env.js'
import { loadCliState } from './state.js'

interface SeedModel {
  name: string
  contextWindowTokens: number
  maxOutputTokens: number
  supportsReasoning: boolean
}

function family(names: string[], contextWindowTokens: number, maxOutputTokens: number, supportsReasoning: boolean): SeedModel[] {
  return names.map((name) => ({ name, contextWindowTokens, maxOutputTokens, supportsReasoning }))
}

const gptModels: SeedModel[] = [
  ...family(['gpt-5.6', 'gpt-5.6-sol', 'gpt-5.5', 'gpt-5.5-mini', 'gpt-5.5-nano'], 400000, 128000, true),
  ...family(['gpt-5.1', 'gpt-5.1-codex', 'gpt-5.1-codex-mini', 'gpt-5.1-chat-latest'], 400000, 128000, true),
  ...family(['gpt-5', 'gpt-5-mini', 'gpt-5-nano', 'gpt-5-codex'], 400000, 128000, true),
  ...family(['gpt-4.1', 'gpt-4.1-mini', 'gpt-4.1-nano'], 1000000, 32768, false),
  ...family(['gpt-4o', 'gpt-4o-mini'], 128000, 16384, false),
]

function isOpenAIFamily(config: ModelProviderConfig): boolean {
  return config.api_format === 'openai-responses' || config.api_format === 'openai-chat-completions'
}

async function ensureSeedModel(
  client: OmnaraClient,
  orgId: string,
  config: ModelProviderConfig,
  existing: Map<string, ConfiguredModel>,
  seed: SeedModel,
): Promise<ConfiguredModel> {
  const current = existing.get(seed.name)
  if (current == null) {
    const { data } = await sdk.createConfiguredModel({
      client,
      path: { orgID: orgId, modelProviderConfigID: config.id },
      headers: { 'Idempotency-Key': `cli-agent-seed-${config.id}-${seed.name}` },
      body: {
        name: seed.name,
        provider_model_slug: seed.name,
        context_window_tokens: seed.contextWindowTokens,
        max_output_tokens: seed.maxOutputTokens,
        supports_tools: true,
        ...(seed.supportsReasoning
          ? { supports_reasoning: true, supported_reasoning_efforts: reasoningEfforts }
          : {}),
      },
    })
    console.log(`created  ${config.name}/${seed.name} (${data.id})`)
    return data
  }
  if (seed.supportsReasoning && !current.supports_reasoning) {
    const { data } = await sdk.updateConfiguredModel({
      client,
      path: { orgID: orgId, modelProviderConfigID: config.id, configuredModelID: current.id },
      body: { supports_reasoning: true, supported_reasoning_efforts: reasoningEfforts },
    })
    console.log(`updated  ${config.name}/${seed.name} (reasoning enabled)`)
    return data
  }
  console.log(`exists   ${config.name}/${seed.name}`)
  return current
}

async function main(): Promise<void> {
  const env = loadEnv()
  const client = createOmnaraClient({ baseUrl: env.apiUrl, auth: bearerToken(env.apiKey) })
  const orgId = await ensureOrg(client, env, loadCliState(env).orgId)

  const { data: configs } = await sdk.listModelProviderConfigs({ client, path: { orgID: orgId } })
  const openAIConfigs = configs.data.filter(isOpenAIFamily)
  if (openAIConfigs.length === 0) {
    throw new Error('no OpenAI-format model provider configs in this org; create one first (e.g. via the CLI bootstrap)')
  }

  const { data: projects } = await sdk.listVisibleProjects({ client, path: { orgID: orgId } })
  const seeded: ConfiguredModel[] = []
  for (const config of openAIConfigs) {
    const { data: models } = await sdk.listConfiguredModels({
      client,
      path: { orgID: orgId, modelProviderConfigID: config.id },
      query: { limit: 100 },
    })
    const existing = new Map(models.data.map((model) => [model.name, model]))
    for (const seed of gptModels) {
      seeded.push(await ensureSeedModel(client, orgId, config, existing, seed))
    }
  }

  for (const project of projects.data) {
    const granted = new Set<string>()
    let cursor: string | undefined
    do {
      const { data } = await sdk.listProjectModelGrants({
        client,
        path: { orgID: orgId, projectID: project.id },
        query: { cursor },
      })
      for (const item of data.data) granted.add(item.grant.configured_model_id)
      cursor = data.next_cursor ?? undefined
    } while (cursor != null)
    let created = 0
    for (const model of seeded) {
      if (granted.has(model.id)) continue
      await sdk.createProjectModelGrant({
        client,
        path: { orgID: orgId, projectID: project.id },
        headers: { 'Idempotency-Key': `cli-agent-seed-grant-${project.id}-${model.id}` },
        body: { configured_model_id: model.id },
      })
      created += 1
    }
    console.log(`granted  ${created} new models to ${project.name} (${project.id}); ${granted.size} pre-existing grants`)
  }
}

main().catch((error: unknown) => {
  console.error(`error: ${error instanceof Error ? error.message : String(error)}`)
  process.exit(1)
})
