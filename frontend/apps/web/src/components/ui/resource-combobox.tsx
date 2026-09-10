import { type RefObject, useRef, useState } from 'react'

import { XIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Combobox, ComboboxInput, ComboboxTrigger } from '@/components/ui/combobox'
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
    clearable = true,
    triggerRef: externalTriggerRef,
  }: ResourceComboboxBaseProps<TItem> & {
    value: TItem | null
    onValueChange: (item: TItem | null) => void
    clearable?: boolean
    triggerRef?: RefObject<HTMLButtonElement | null>
  }) {
    const rootProps = useResourceComboboxRootProps(config, search, items, disabled)
    const localTriggerRef = useRef<HTMLButtonElement>(null)
    const triggerRef = externalTriggerRef ?? localTriggerRef
    const [open, setOpen] = useState(false)
    const canClear = clearable && value !== null

    return (
      <Combobox
        {...rootProps}
        id={id}
        required={required}
        open={open}
        onOpenChange={setOpen}
        value={value}
        onValueChange={onValueChange}
      >
        <div className="relative">
          <ComboboxTrigger
            ref={triggerRef}
            id={id}
            aria-label={id ? undefined : config.placeholder}
            className={canClear ? '[&>span]:pr-8' : undefined}
            onKeyDown={(event) => {
              if (
                event.key.length !== 1 ||
                event.key === ' ' ||
                event.ctrlKey ||
                event.metaKey ||
                event.altKey
              )
                return
              event.preventDefault()
              event.preventBaseUIHandler()
              rootProps.onInputValueChange(event.key)
              setOpen(true)
            }}
          >
            <span className="truncate">{value ? config.itemLabel(value) : placeholder}</span>
          </ComboboxTrigger>
          {canClear && (
            <Button
              variant="ghost"
              size="icon"
              className="text-muted-foreground absolute right-8 top-1 size-8"
              aria-label={`Clear ${config.itemLabel(value)}`}
              disabled={disabled}
              onClick={() => {
                rootProps.onInputValueChange('')
                onValueChange(null)
                triggerRef.current?.focus()
              }}
            >
              <XIcon className="size-4" />
            </Button>
          )}
        </div>
        <ResourceComboboxContent
          config={config}
          pending={pending}
          emptyMessage={emptyMessage}
          query={query}
          action={action}
          searchInput={
            <ComboboxInput
              showTrigger={false}
              aria-label={config.placeholder}
              placeholder={config.placeholder}
            />
          }
        />
      </Combobox>
    )
  }
}
