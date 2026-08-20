import { Combobox, ComboboxInput } from '@/components/ui/combobox'
import { ResourceComboboxContent } from '@/components/ui/resource-combobox-content'
import {
  type ResourceComboboxBaseProps,
  type ResourceComboboxConfig,
  useResourceComboboxRootProps,
} from '@/components/ui/resource-combobox-core'

export type {
  ResourceComboboxQuery,
  ResourceComboboxSearch,
} from '@/components/ui/resource-combobox-core'

export function createResourceCombobox<TItem>(config: ResourceComboboxConfig<TItem>) {
  return function BoundResourceCombobox({
    items,
    id,
    required,
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
    value: TItem | null
    onValueChange: (item: TItem | null) => void
  }) {
    const {
      inputValue: searchValue,
      onInputValueChange: setSearchValue,
      ...rootProps
    } = useResourceComboboxRootProps(config, search, items, disabled)
    const inputValue = searchValue || (value ? config.itemLabel(value) : '')

    return (
      <Combobox
        {...rootProps}
        id={id}
        required={required}
        inputValue={inputValue}
        onInputValueChange={(nextValue, eventDetails) => {
          // Only user typing is a search query. Selection and popup lifecycle
          // updates should restore the selected item's label in the input.
          setSearchValue(eventDetails.reason === 'input-change' ? nextValue : '')
        }}
        value={value}
        onValueChange={onValueChange}
      >
        <ComboboxInput aria-label={id ? undefined : placeholder} placeholder={placeholder} />
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
