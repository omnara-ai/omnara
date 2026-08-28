import { useProjectMachinePoolGrants, useProjectModelGrants } from '@omnara/react'
import type { ConfiguredModelSummary, MachinePoolSummary } from '@omnara/sdk'

interface ProjectDefaults {
  ready: boolean
  defaultModel?: ConfiguredModelSummary
  defaultPool?: MachinePoolSummary
}

const preferredModel = { provider_config: 'omnara-openrouter', name: 'openai/gpt-5.6-sol' }

export function useProjectDefaults(orgId: string, projectId: string): ProjectDefaults {
  const poolGrantsQuery = useProjectMachinePoolGrants(orgId, projectId, {
    sort: 'created_at',
    pageSize: 50,
  })
  const modelGrantsQuery = useProjectModelGrants(orgId, projectId, {
    sort: 'created_at',
    pageSize: 100,
  })
  const poolGrants = poolGrantsQuery.data?.pages[0]?.data ?? []
  const modelGrants = modelGrantsQuery.data?.pages[0]?.data ?? []
  const defaultPool = (
    poolGrants.find((grant) => grant.machine_pool.management_kind === 'cluster') ?? poolGrants[0]
  )?.machine_pool
  const defaultModel =
    modelGrants.find(
      ({ model }) =>
        model.provider_config === preferredModel.provider_config &&
        model.name === preferredModel.name,
    )?.model ?? modelGrants[0]?.model
  return {
    ready: !poolGrantsQuery.isPending && !modelGrantsQuery.isPending,
    defaultModel,
    defaultPool,
  }
}
