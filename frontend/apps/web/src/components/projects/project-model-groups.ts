import type {
  AgentProfileSummary,
  ModelProviderConfig,
  ProjectModelGrantListItem,
} from '@omnara/sdk'

import { clusterFirst } from '@/lib/management-kind'

/** Provider a group of shared models belongs to; `config` is absent when the viewer can't list it. */
export interface ProviderGroup {
  id: string
  name: string
  config?: ModelProviderConfig
  items: ProjectModelGrantListItem[]
}

export function profileModelKey(providerConfig: string, name: string) {
  return `${providerConfig}/${name}`
}

export function countProfilesByModel(profiles: AgentProfileSummary[]) {
  const counts = new Map<string, number>()
  for (const profile of profiles) {
    const { name, provider_config: providerConfig } = profile.current_config.model
    const key = profileModelKey(providerConfig, name)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return counts
}

export function groupByProvider(
  items: ProjectModelGrantListItem[],
  providers: ModelProviderConfig[],
  includeEmpty: boolean,
): ProviderGroup[] {
  const byProvider = new Map<string, ProjectModelGrantListItem[]>()
  for (const item of items) {
    const id = item.model.model_provider_config_id
    byProvider.set(id, [...(byProvider.get(id) ?? []), item])
  }
  const known = clusterFirst(
    [...providers].sort((left, right) => left.name.localeCompare(right.name)),
  )
  const groups: ProviderGroup[] = known
    .filter((config) => includeEmpty || byProvider.has(config.id))
    .map((config) => ({
      id: config.id,
      name: config.name,
      config,
      items: byProvider.get(config.id) ?? [],
    }))
  const knownIds = new Set(known.map((config) => config.id))
  for (const [id, groupItems] of byProvider) {
    if (knownIds.has(id) || !groupItems[0]) continue
    groups.push({ id, name: groupItems[0].model.provider_config, items: groupItems })
  }
  return groups
}

/** Count label for shared models; "shared" when shown against the org total. */
export function sharedCountLabel(orgTotal: number, sharedCount: number) {
  if (orgTotal > 0) return 'shared'
  return sharedCount === 1 ? 'model' : 'models'
}
