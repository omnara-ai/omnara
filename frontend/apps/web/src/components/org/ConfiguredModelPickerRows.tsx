import type { DiscoveredProviderModel } from '@omnara/sdk'
import { type ComponentProps, type KeyboardEvent, type Ref, useEffect, useRef } from 'react'

import { Check, Plus } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { formatCompactCount } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { ConfiguredModelDraft } from './CreateConfiguredModelDialogState'

const rowClass =
  'flex min-h-11 items-center gap-3 rounded-lg px-3 has-[[data-picker-row]:focus-visible]:bg-muted/70'

/** A theme-colored checkbox; the native one renders harsh white borders in dark mode. */
function PickerCheckbox({ className, ...props }: ComponentProps<'input'>) {
  return (
    <span className="relative grid size-4 shrink-0 place-items-center">
      <input
        type="checkbox"
        className={cn(
          'border-input peer size-4 cursor-pointer appearance-none rounded-[4px] border bg-transparent outline-none transition-colors',
          'checked:border-primary checked:bg-primary disabled:cursor-not-allowed disabled:opacity-50',
          'focus-visible:ring-ring/50 focus-visible:ring-2',
          className,
        )}
        {...props}
      />
      <Check
        aria-hidden="true"
        strokeWidth={3}
        className="text-primary-foreground pointer-events-none absolute size-3 opacity-0 peer-checked:opacity-100"
      />
    </span>
  )
}

/** Moves focus between rows; each row's primary control forwards its key presses here. */
type RowKeyDown = (event: KeyboardEvent<HTMLElement>) => void

/** A model chosen for creation; expanded, it edits the name and token limits. */
export function SelectedModelRow({
  draft,
  error,
  index,
  expanded,
  disabled,
  onToggleExpanded,
  onChange,
  onRemove,
  onRowKeyDown,
}: {
  draft: ConfiguredModelDraft
  /** Why the draft cannot be created yet, or ''. */
  error: string
  index: number
  expanded: boolean
  disabled: boolean
  onToggleExpanded: () => void
  onChange: (draft: ConfiguredModelDraft) => void
  onRemove: () => void
  onRowKeyDown: RowKeyDown
}) {
  const id = `cm-draft-${index}`
  const checkboxRef = useRef<HTMLInputElement>(null)
  const nameRef = useRef<HTMLInputElement>(null)
  const focusNameOnExpand = useRef(false)

  useEffect(() => {
    if (!expanded || !focusNameOnExpand.current) return
    focusNameOnExpand.current = false
    nameRef.current?.focus()
  }, [expanded])

  function toggleExpanded() {
    focusNameOnExpand.current = !expanded
    onToggleExpanded()
  }

  function onCheckboxKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowRight' && !expanded) {
      event.preventDefault()
      toggleExpanded()
    } else if (event.key === 'ArrowLeft' && expanded) {
      event.preventDefault()
      onToggleExpanded()
    } else {
      onRowKeyDown(event)
    }
  }

  // Enter in a field finishes editing, like the Done button, and returns to the row.
  function onFieldKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== 'Enter' || event.metaKey || event.ctrlKey) return
    event.preventDefault()
    onToggleExpanded()
    checkboxRef.current?.focus()
  }

  return (
    <li className={cn('rounded-lg', expanded && 'bg-muted/50 pb-3')}>
      <div className={rowClass}>
        <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-3">
          <PickerCheckbox
            ref={checkboxRef}
            checked
            disabled={disabled}
            aria-keyshortcuts="ArrowRight"
            onChange={onRemove}
            onKeyDown={onCheckboxKeyDown}
            data-picker-row=""
          />
          <span className="truncate font-mono text-sm">{draft.slug}</span>
        </label>
        {!expanded && error && <span className="text-destructive text-xs">Needs details</span>}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-foreground -mr-2"
          aria-expanded={expanded}
          aria-label={`${expanded ? 'Done editing' : 'Edit'} ${draft.slug}`}
          disabled={disabled}
          onClick={toggleExpanded}
        >
          {expanded ? 'Done' : 'Edit'}
        </Button>
      </div>
      {expanded && (
        <div className="flex flex-col gap-2 pl-10 pr-3">
          <div className="grid gap-3 sm:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)_minmax(0,1fr)]">
            <DraftInput
              ref={nameRef}
              id={`${id}-name`}
              label="Name"
              onKeyDown={onFieldKeyDown}
              value={draft.name}
              disabled={disabled}
              onChange={(name) => {
                onChange({ ...draft, name })
              }}
            />
            <DraftInput
              id={`${id}-context`}
              label="Context"
              onKeyDown={onFieldKeyDown}
              numeric
              value={draft.contextWindowTokens}
              disabled={disabled}
              onChange={(contextWindowTokens) => {
                onChange({ ...draft, contextWindowTokens })
              }}
            />
            <DraftInput
              id={`${id}-max-output`}
              label="Max output"
              onKeyDown={onFieldKeyDown}
              numeric
              placeholder="Optional"
              value={draft.maxOutputTokens}
              disabled={disabled}
              onChange={(maxOutputTokens) => {
                onChange({ ...draft, maxOutputTokens })
              }}
            />
          </div>
          {error && <p className="text-destructive text-xs">{error}</p>}
        </div>
      )}
    </li>
  )
}

function DraftInput({
  ref,
  id,
  label,
  value,
  numeric = false,
  placeholder,
  disabled,
  onChange,
  onKeyDown,
}: {
  ref?: Ref<HTMLInputElement>
  id: string
  label: string
  value: string
  numeric?: boolean
  placeholder?: string
  disabled: boolean
  onChange: (value: string) => void
  onKeyDown: (event: KeyboardEvent<HTMLInputElement>) => void
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <label htmlFor={id} className="text-muted-foreground text-xs">
        {label}
      </label>
      <Input
        ref={ref}
        id={id}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        className={cn('h-9', numeric && 'font-mono tabular-nums')}
        {...(numeric ? { type: 'number', min: '1', step: '1' } : { autoComplete: 'off' })}
        onChange={(event) => {
          onChange(event.target.value)
        }}
        onKeyDown={onKeyDown}
      />
    </div>
  )
}

/**
 * A model to pick; added marks a slug the provider already has or that is already picked,
 * picked to configure again.
 */
export function DiscoveredModelRow({
  model,
  added = false,
  disabled,
  onSelect,
  onRowKeyDown,
}: {
  model: DiscoveredProviderModel
  added?: boolean
  disabled: boolean
  onSelect: () => void
  onRowKeyDown: RowKeyDown
}) {
  return (
    <li>
      <label
        className={cn(rowClass, 'hover:bg-muted/40 cursor-pointer')}
        title={model.display_name ?? model.slug}
      >
        <PickerCheckbox
          checked={false}
          disabled={disabled}
          onChange={onSelect}
          onKeyDown={onRowKeyDown}
          data-picker-row=""
        />
        <span
          className={cn(
            'min-w-0 flex-1 truncate font-mono text-sm',
            added && 'text-muted-foreground/70',
          )}
        >
          {model.slug}
        </span>
        <span className="text-muted-foreground text-xs tabular-nums">
          {added
            ? 'Added'
            : model.context_window_tokens === undefined
              ? 'No limits'
              : formatCompactCount(model.context_window_tokens)}
        </span>
      </label>
    </li>
  )
}

export function CustomModelRow({
  slug,
  disabled,
  onAdd,
  onRowKeyDown,
}: {
  slug: string
  disabled: boolean
  onAdd: () => void
  onRowKeyDown: RowKeyDown
}) {
  return (
    <li>
      <button
        type="button"
        disabled={disabled}
        className={cn(
          rowClass,
          'hover:bg-muted/40 focus-visible:bg-muted/70 w-full text-left text-sm outline-none',
        )}
        onClick={onAdd}
        onKeyDown={onRowKeyDown}
        data-picker-row=""
      >
        <Plus aria-hidden="true" className="text-muted-foreground size-4 shrink-0" />
        <span className="min-w-0 truncate">
          Add <span className="font-mono">{slug}</span> as a custom model
        </span>
      </button>
    </li>
  )
}
