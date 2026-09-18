import { useOmnaraClient, useProjectModelGrants } from '@omnara/react'
import { type ConfiguredModelSummary, type MachinePoolSummary, sdk } from '@omnara/sdk'
import { listProjectMachinePoolGrantsQueryKey } from '@omnara/sdk/tanstack'
import { useQuery } from '@tanstack/react-query'

interface ProjectDefaults {
  ready: boolean
  defaultModel?: ConfiguredModelSummary
  defaultPool?: MachinePoolSummary
}

const defaultPoolName = 'default-pool'

const preferredModel = { provider_config: 'omnara-openrouter', name: 'openai/gpt-5.6-sol' }

export function useProjectDefaults(orgId: string, projectId: string): ProjectDefaults {
  const client = useOmnaraClient()
  const path = { orgID: orgId, projectID: projectId }
  const defaultPoolQuery = useQuery({
    queryKey: [...listProjectMachinePoolGrantsQueryKey({ path, client }), 'default'],
    queryFn: async ({ signal }) => {
      const clusters: MachinePoolSummary[] = []
      let cursor: string | undefined
      do {
        const { data } = await sdk.listProjectMachinePoolGrants({
          path,
          client,
          signal,
          query: { sort: 'created_at', limit: 50, cursor },
        })
        const named = data.data.find(({ machine_pool }) => machine_pool.name === defaultPoolName)
        if (named) return named.machine_pool
        for (const { machine_pool } of data.data) {
          if (machine_pool.management_kind === 'cluster') clusters.push(machine_pool)
        }
        cursor = data.next_cursor ?? undefined
      } while (cursor)
      return clusters[0] ?? null
    },
  })
  const modelGrantsQuery = useProjectModelGrants(orgId, projectId, {
    sort: 'created_at',
    pageSize: 100,
  })
  const modelGrants = modelGrantsQuery.data?.pages[0]?.data ?? []
  const defaultModel =
    modelGrants.find(
      ({ model }) =>
        model.provider_config === preferredModel.provider_config &&
        model.name === preferredModel.name,
    )?.model ?? modelGrants[0]?.model
  return {
    ready: !defaultPoolQuery.isPending && !modelGrantsQuery.isPending,
    defaultModel,
    defaultPool: defaultPoolQuery.data ?? undefined,
  }
}
