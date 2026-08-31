import {
  type CreateSkillRequest,
  type ListProjectAvailableSkillsData,
  type ListSkillGrantsData,
  type ListSkillsData,
  sdk,
  type SkillOwnerInput,
  type UpdateSkillRequest,
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
import { cursorPagination } from './pagination'

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
    ...listSkillsInfiniteOptions({
      path: { orgID },
      query: { ...ownerFilterQuery(owner), ...list.query },
      client,
    }),
    ...cursorPagination,
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

export function useProjectAvailableSkills(
  orgID: string,
  projectID: string,
  options?: ProjectAvailableSkillListOptions,
) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListProjectAvailableSkillsData>(options)
  return useInfiniteQuery({
    ...listProjectAvailableSkillsInfiniteOptions({
      path: { orgID, projectID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
    enabled: list.enabled,
  })
}

export function useSkillGrants(orgID: string, skillID: string, options?: SkillGrantListOptions) {
  const client = useOmnaraClient()
  const list = paginatedListOptions<ListSkillGrantsData>(options)
  return useInfiniteQuery({
    ...listSkillGrantsInfiniteOptions({
      path: { orgID, skillID },
      query: list.query,
      client,
    }),
    ...cursorPagination,
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
      const entry = query.queryKey[0] as { _id?: string; path?: { orgID?: string } } | undefined
      return (
        entry?._id !== undefined &&
        SKILL_LIST_OPERATIONS.has(entry._id) &&
        entry.path?.orgID === orgID
      )
    },
  })
}

export function useCreateSkill(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreateSkillRequest) => {
      const { data } = await sdk.createSkill({
        path: { orgID },
        body,
        client,
        bodySerializer: (rawBody) => {
          const upload = rawBody as CreateSkillRequest
          const form = new FormData()
          form.append(
            'owner',
            new Blob([JSON.stringify(upload.owner)], { type: 'application/json' }),
          )
          form.append(
            'archive',
            upload.archive,
            upload.archive instanceof File ? upload.archive.name : 'skill.zip',
          )
          return form
        },
      })
      return data
    },
    onSuccess: async () => {
      await invalidateSkillLists(queryClient, orgID)
    },
  })
}

export function useUpdateSkill(orgID: string) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: async ({ skillID, body }: { skillID: string; body: UpdateSkillRequest }) => {
      const { data } = await sdk.updateSkill({
        path: { orgID, skillID },
        body,
        client,
        bodySerializer: () => {
          const form = new FormData()
          if ('archive' in body) {
            form.append(
              'archive',
              body.archive,
              body.archive instanceof File ? body.archive.name : 'skill.zip',
            )
          } else {
            form.append('skill_md', body.skill_md)
          }
          return form
        },
      })
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
