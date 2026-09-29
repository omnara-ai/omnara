import {
  type CreateSkillRequest,
  type ListProjectAvailableSkillsData,
  type ListSkillGrantsData,
  type ListSkillsData,
  sdk,
  type Skill,
  type SkillOwnerInput,
} from '@omnara/sdk'
import {
  getSkillOptions,
  getSkillQueryKey,
  listProjectAvailableSkillsInfiniteOptions,
  listSkillGrantsInfiniteOptions,
  listSkillsInfiniteOptions,
} from '@omnara/sdk/tanstack'
import {
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import {
  type ListFilters,
  type ListSort,
  type PaginatedListOptions,
  paginatedListOptions,
} from './list-options'
import { cursorPaginated } from './pagination'
import { generatedQueryKey } from './query-keys'

export type SkillOwnerScope = SkillOwnerInput
/** Owner scope travels through the dedicated `owner` argument, not filters. */
export type SkillListFilters = Omit<ListFilters<ListSkillsData>, 'owner_kind' | 'owner_project_id'>
export type SkillListSort = ListSort<ListSkillsData>
export type SkillListOptions = Omit<PaginatedListOptions<ListSkillsData>, 'filters'> & {
  filters?: SkillListFilters
}

function ownerFilterQuery(owner: SkillOwnerScope | undefined) {
  if (!owner) return undefined
  return {
    owner_kind: owner.kind,
    ...(owner.kind === 'project' ? { owner_project_id: owner.project_id } : undefined),
  }
}
export type ProjectAvailableSkillListFilters = ListFilters<ListProjectAvailableSkillsData>
export type ProjectAvailableSkillListSort = ListSort<ListProjectAvailableSkillsData>
export type ProjectAvailableSkillListOptions = PaginatedListOptions<ListProjectAvailableSkillsData>
export type SkillGrantListFilters = ListFilters<ListSkillGrantsData>
export type SkillGrantListSort = ListSort<ListSkillGrantsData>
export type SkillGrantListOptions = PaginatedListOptions<ListSkillGrantsData>

export function useSkills(orgID: string, owner?: SkillOwnerScope, options?: SkillListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListSkillsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listSkillsInfiniteOptions({
        path: { orgID },
        query: { ...ownerFilterQuery(owner), ...list.query },
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useSkill(orgID: string, skillID: string, enabled = true) {
  const client = useOmnaraClient()
  return useQuery({
    ...getSkillOptions({ path: { orgID, skillID }, client }),
    enabled,
  })
}

const SKILL_LOOKUP_PAGE_SIZE = 100

export function useSkillNameLookup(orgID: string, owner: SkillOwnerScope) {
  const client = useOmnaraClient()
  return async (names: string[]): Promise<ReadonlyMap<string, Skill>> => {
    const wanted = new Set(names)
    const found = new Map<string, Skill>()
    let cursor: string | undefined
    while (wanted.size > found.size) {
      const { data: page } = await sdk.listSkills({
        path: { orgID },
        query: { ...ownerFilterQuery(owner), limit: SKILL_LOOKUP_PAGE_SIZE, cursor },
        client,
      })
      for (const skill of page.data) {
        if (wanted.has(skill.name)) found.set(skill.name, skill)
      }
      if (page.next_cursor === null) break
      cursor = page.next_cursor
    }
    return found
  }
}

export function useProjectAvailableSkills(
  orgID: string,
  projectID: string,
  options?: ProjectAvailableSkillListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectAvailableSkillsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listProjectAvailableSkillsInfiniteOptions({
        path: { orgID, projectID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

export function useSkillGrants(orgID: string, skillID: string, options?: SkillGrantListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListSkillGrantsData>(options)
  return useInfiniteQuery({
    ...cursorPaginated(
      listSkillGrantsInfiniteOptions({
        path: { orgID, skillID },
        query: list.query,
        client,
      }),
    ),
    enabled: list.enabled,
  })
}

const SKILL_LIST_OPERATIONS = new Set([
  'listSkills',
  'listProjectAvailableSkills',
  'listSkillGrants',
])

function invalidateSkillLists(queryClient: QueryClient, orgID: string) {
  return queryClient.invalidateQueries({
    predicate: (query) => {
      const entry = generatedQueryKey(query)
      return (
        entry !== undefined && SKILL_LIST_OPERATIONS.has(entry._id) && entry.path?.orgID === orgID
      )
    },
  })
}

export function useCreateSkill(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreateSkillRequest) => {
      const { data } = await sdk.createSkill({ path: { orgID }, body, client })
      return data
    },
    onSuccess: async () => {
      await invalidateSkillLists(queryClient, orgID)
    },
  })
}

export type UpdateSkillUpload = { archive: Blob | File } | { skill_md: string }

export function useUpdateSkill(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ skillID, body }: { skillID: string; body: UpdateSkillUpload }) => {
      const { data } = await sdk.updateSkill({ path: { orgID, skillID }, body, client })
      return data
    },
    onSuccess: async (_data, { skillID }) => {
      await Promise.all([
        invalidateSkillLists(queryClient, orgID),
        queryClient.invalidateQueries({
          queryKey: getSkillQueryKey({ path: { orgID, skillID }, client }),
        }),
      ])
    },
  })
}

export function useDeleteSkill(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (skillID: string) => {
      const { data } = await sdk.deleteSkill({ path: { orgID, skillID }, client })
      return data
    },
    onSuccess: async () => {
      await invalidateSkillLists(queryClient, orgID)
    },
  })
}

export function useGrantSkillToProject(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ skillID, projectID }: { skillID: string; projectID: string }) => {
      const { data } = await sdk.createSkillGrant({
        path: { orgID, skillID },
        body: { target_project_id: projectID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateSkillLists(queryClient, orgID)
    },
  })
}

export function useDeleteSkillGrant(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ skillID, grantID }: { skillID: string; grantID: string }) => {
      const { data } = await sdk.deleteSkillGrant({
        path: { orgID, skillID, grantID },
        client,
      })
      return data
    },
    onSuccess: async () => {
      await invalidateSkillLists(queryClient, orgID)
    },
  })
}
