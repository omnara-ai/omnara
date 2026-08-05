import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react'

import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxLoading,
  ComboboxStatus,
  ComboboxValue,
} from '@/components/ui/combobox'

export interface ResourceComboboxConfig<TItem> {
  itemKey: (item: TItem) => string
  itemLabel: (item: TItem) => string
  renderItem?: (item: TItem) => ReactNode
  placeholder: string
  emptyMessage?: string
}

/** State and pagination surface of a React Query infinite query result. */
export interface ResourceComboboxQuery {
  isPending: boolean
  isError: boolean
  isFetching: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => unknown
  refetch: () => unknown
}

/** External search state, e.g. from useTypeaheadSearch when filtering server-side. */
export interface ResourceComboboxSearch {
  search: string
  setSearch: (value: string) => void
}

interface ResourceComboboxBaseProps<TItem> {
  items: TItem[]
  /** Omit to let the combobox manage its own input state (client-side filtering only). */
  search?: ResourceComboboxSearch
  /** Infinite query backing the list; drives pending state and the load-more row. */
  query?: ResourceComboboxQuery
  pending?: boolean
  placeholder?: string
  emptyMessage?: string
  disabled?: boolean
  action?: ReactNode
}

/** Root props shared by the single- and multi-select factories. */
function useResourceComboboxRootProps<TItem>(
  config: ResourceComboboxConfig<TItem>,
  search: ResourceComboboxSearch | undefined,
  items: TItem[],
  disabled: boolean | undefined,
) {
  const [localSearch, setLocalSearch] = useState('')
  return {
    items,
    inputValue: search ? search.search : localSearch,
    onInputValueChange: search ? search.setSearch : setLocalSearch,
    itemToStringLabel: config.itemLabel,
    itemToStringValue: config.itemKey,
    isItemEqualToValue: (item: TItem, other: TItem) =>
      config.itemKey(item) === config.itemKey(other),
    autoHighlight: true,
    disabled,
  }
}

export function createResourceCombobox<TItem>(config: ResourceComboboxConfig<TItem>) {
  return function BoundResourceCombobox({
    items,
    value,
    onValueChange,
    search,
    query,
    pending = query?.isPending ?? false,
    placeholder = config.placeholder,
    emptyMessage = config.emptyMessage ?? 'No matching results.',
    disabled,
    action,
  }: ResourceComboboxBaseProps<TItem> & {
    value: string | null
    onValueChange: (item: TItem | null) => void
  }) {
    const {
      inputValue: searchValue,
      onInputValueChange: setSearchValue,
      ...rootProps
    } = useResourceComboboxRootProps(config, search, items, disabled)
    const selected = useCachedItems(items, value === null ? [] : [value], config.itemKey)[0] ?? null
    const inputValue = searchValue || (selected ? config.itemLabel(selected) : '')

    return (
      <Combobox
        {...rootProps}
        inputValue={inputValue}
        onInputValueChange={(nextValue, eventDetails) => {
          // Only user typing is a search query. Selection and popup lifecycle
          // updates should restore the selected item's label in the input.
          setSearchValue(eventDetails.reason === 'input-change' ? nextValue : '')
        }}
        value={selected}
        onValueChange={onValueChange}
      >
        <ComboboxInput aria-label={placeholder} placeholder={placeholder} />
        <ResourceComboboxContent
          config={config}
          pending={pending}
          emptyMessage={emptyMessage}
          query={query}
          action={action}
        />
      </Combobox>
    )
  }
}

export function createResourceMultiCombobox<TItem>(config: ResourceComboboxConfig<TItem>) {
  return function BoundResourceMultiCombobox({
    items,
    value,
    onValueChange,
    search,
    query,
    pending = query?.isPending ?? false,
    placeholder = config.placeholder,
    emptyMessage = config.emptyMessage ?? 'No matching results.',
    disabled,
    action,
  }: ResourceComboboxBaseProps<TItem> & {
    value: string[]
    onValueChange: (items: TItem[]) => void
  }) {
    const rootProps = useResourceComboboxRootProps(config, search, items, disabled)
    const selected = useCachedItems(items, value, config.itemKey)

    return (
      <Combobox {...rootProps} multiple value={selected} onValueChange={onValueChange}>
        <ComboboxChips>
          <ComboboxValue>
            {selected.map((item) => (
              <ComboboxChip
                key={config.itemKey(item)}
                removeLabel={`Remove ${config.itemLabel(item)}`}
              >
                {config.itemLabel(item)}
              </ComboboxChip>
            ))}
          </ComboboxValue>
          <ComboboxChipsInput aria-label={placeholder} placeholder={placeholder} />
        </ComboboxChips>
        <ResourceComboboxContent
          config={config}
          pending={pending}
          emptyMessage={emptyMessage}
          query={query}
          action={action}
        />
      </Combobox>
    )
  }
}

/**
 * Retain controlled selections while server-side search or pagination replaces
 * the current result set. Base UI values are resource objects, while callers
 * intentionally store stable resource IDs.
 */
function useCachedItems<TItem>(
  items: TItem[],
  selectedKeys: string[],
  itemKey: (item: TItem) => string,
) {
  const cache = useRef(new Map<string, TItem>())
  const currentItems = useMemo(
    () => new Map(items.map((item) => [itemKey(item), item])),
    [itemKey, items],
  )

  useEffect(() => {
    const retainedKeys = new Set(selectedKeys)
    for (const key of cache.current.keys()) {
      if (!retainedKeys.has(key) && !currentItems.has(key)) {
        cache.current.delete(key)
      }
    }
    for (const [key, item] of currentItems) {
      cache.current.set(key, item)
    }
  }, [currentItems, selectedKeys])

  return selectedKeys.flatMap((key) => {
    const item = currentItems.get(key) ?? cache.current.get(key)
    return item === undefined ? [] : [item]
  })
}

function ResourceComboboxContent<TItem>({
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
