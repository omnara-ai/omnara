import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxValue,
} from '@/components/ui/combobox'
import { ResourceComboboxContent } from '@/components/ui/resource-combobox-content'
import {
  type ResourceComboboxBaseProps,
  type ResourceComboboxConfig,
  useResourceComboboxRootProps,
} from '@/components/ui/resource-combobox-core'

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
    value: TItem[]
    onValueChange: (items: TItem[]) => void
  }) {
    const rootProps = useResourceComboboxRootProps(config, search, items, disabled)

    return (
      <Combobox {...rootProps} multiple value={value} onValueChange={onValueChange}>
        <ComboboxChips>
          <ComboboxValue>
            {value.map((item) => (
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
