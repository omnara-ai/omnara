import type { ReactNode } from 'react'

import {
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxLoading,
  ComboboxStatus,
} from '@/components/ui/combobox'
import type {
  ResourceComboboxConfig,
  ResourceComboboxQuery,
} from '@/components/ui/resource-combobox-core'

export function ResourceComboboxContent<TItem>({
  config,
  pending,
  emptyMessage,
  query,
  action,
}: {
  config: ResourceComboboxConfig<TItem>
  pending: boolean
  emptyMessage: string
  query?: ResourceComboboxQuery
  action?: ReactNode
}) {
  return (
    <ComboboxContent>
      {action && <div className="border-b p-1">{action}</div>}
      <ComboboxEmpty>{query?.isError ? null : pending ? 'Searching…' : emptyMessage}</ComboboxEmpty>
      <ComboboxList>
        {(item: TItem) => (
          <ComboboxItem key={config.itemKey(item)} value={item}>
            {config.renderItem?.(item) ?? config.itemLabel(item)}
          </ComboboxItem>
        )}
      </ComboboxList>
      {pending && !query?.isError && <ComboboxLoading />}
      {query?.isError && (
        <ComboboxStatus>
          <span className="text-destructive">Could not load results.</span>{' '}
          <button
            type="button"
            className="text-foreground underline disabled:opacity-50"
            disabled={query.isFetching}
            onClick={() => {
              void query.refetch()
            }}
          >
            {query.isFetching ? 'Retrying…' : 'Retry'}
          </button>
        </ComboboxStatus>
      )}
      {!pending && !query?.isError && query?.hasNextPage && (
        <ComboboxStatus>
          <button
            type="button"
            className="text-foreground hover:underline disabled:opacity-50"
            disabled={query.isFetchingNextPage}
            onClick={() => {
              void query.fetchNextPage()
            }}
          >
            {query.isFetchingNextPage ? 'Loading more…' : 'Load more results'}
          </button>
        </ComboboxStatus>
      )}
    </ComboboxContent>
  )
}
