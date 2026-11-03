import { useState } from 'react'

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
    closeOnSelect = false,
  }: ResourceComboboxBaseProps<TItem> & {
    value: TItem[]
    onValueChange: (items: TItem[]) => void
    closeOnSelect?: boolean
  }) {
    const rootProps = useResourceComboboxRootProps(config, search, items, disabled)
    const [open, setOpen] = useState(false)

    return (
      <Combobox
        {...rootProps}
        id={id}
        required={required}
        multiple
        open={open}
        onOpenChange={setOpen}
        value={value}
        onValueChange={(items, details) => {
          onValueChange(items)
          if (closeOnSelect && details.reason === 'item-press') setOpen(false)
        }}
      >
        <ComboboxChips>
          <ComboboxValue>
            {value.map((item) => (
              <ComboboxChip
                key={config.itemKey(item)}
                removeLabel={`Remove ${config.itemLabel(item)}`}
              >
                {config.renderValue?.(item) ?? config.itemLabel(item)}
              </ComboboxChip>
            ))}
          </ComboboxValue>
          <ComboboxChipsInput aria-label={id ? undefined : placeholder} placeholder={placeholder} />
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
