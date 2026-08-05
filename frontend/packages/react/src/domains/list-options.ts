import type { CreatedResourceListSort, ResourceListSort } from '@omnara/sdk'
import { zCreatedResourceListSort, zResourceListSort } from '@omnara/sdk/zod'

/** Query controls owned by cursor pagination rather than the caller. */
type PaginationControls = 'cursor' | 'limit' | 'sort'

/** Runtime sort values generated from the canonical OpenAPI schemas. */
export const RESOURCE_LIST_SORTS: readonly ResourceListSort[] = zResourceListSort.unwrap().options
export const CREATED_RESOURCE_LIST_SORTS: readonly CreatedResourceListSort[] =
  zCreatedResourceListSort.unwrap().options

export type ListQuery<TData extends { query?: unknown }> = NonNullable<TData['query']>

/** Endpoint-specific filters, derived directly from the generated API query type. */
export type ListFilters<TData extends { query?: unknown }> = Omit<
  ListQuery<TData>,
  PaginationControls
>

/** Endpoint-specific sort values, including descending values prefixed with `-`. */
export type ListSort<TData extends { query?: unknown }> =
  ListQuery<TData> extends {
    sort?: infer TSort
  }
    ? TSort
    : never

/**
 * Shared options accepted by every rich, cursor-paginated resource hook.
 * The cursor is managed by TanStack Query and intentionally cannot be set by callers.
 */
export interface PaginatedListOptions<TData extends { query?: unknown }> {
  filters?: ListFilters<TData>
  sort?: ListSort<TData>
  pageSize?: number
  enabled?: boolean
}

export const DEFAULT_LIST_PAGE_SIZE = 15

export function paginatedListOptions<TData extends { query?: unknown }>(
  options: PaginatedListOptions<TData> | undefined,
) {
  const { filters, sort, pageSize = DEFAULT_LIST_PAGE_SIZE, enabled = true } = options ?? {}
  return {
    query: {
      ...filters,
      ...(sort === undefined ? undefined : { sort }),
      limit: pageSize,
    } as ListQuery<TData>,
    enabled,
  }
}
