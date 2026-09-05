import { type ReactNode, useState } from 'react'

export interface ResourceComboboxConfig<TItem> {
  itemKey: (item: TItem) => string
  itemLabel: (item: TItem) => string
  renderItem?: (item: TItem) => ReactNode
  placeholder: string
  emptyMessage?: string
}

export interface ResourceComboboxQuery {
  isPending: boolean
  isError: boolean
  isFetching: boolean
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => void
  refetch: () => void
}

export interface ResourceComboboxSearch {
  search: string
  setSearch: (value: string) => void
}

export interface ResourceComboboxBaseProps<TItem> {
  items: TItem[]
  id?: string
  required?: boolean
  search?: ResourceComboboxSearch
  query?: ResourceComboboxQuery
  pending?: boolean
  placeholder?: string
  emptyMessage?: string
  disabled?: boolean
  action?: ReactNode
}

export function useResourceComboboxRootProps<TItem>(
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
