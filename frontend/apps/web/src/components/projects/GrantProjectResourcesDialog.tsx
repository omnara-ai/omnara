import { type ComponentType, useId } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import type {
  ResourceComboboxQuery,
  ResourceComboboxSearch,
} from '@/components/ui/resource-combobox'
import { useBatchGrantSubmit } from '@/hooks/use-batch-grant-submit'

/**
 * Pick resources the viewer owns (or manages for the org) and share them with
 * one project. The caller loads and filters the candidates; this owns the form.
 */
export function GrantProjectResourcesDialog<TItem>({
  open,
  onOpenChange,
  noun,
  description,
  combobox: ResourceMultiCombobox,
  itemKey,
  items,
  search,
  query,
  pending,
  isError,
  onRetry,
  emptyHint,
  grant,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Singular resource name, e.g. "secret". */
  noun: string
  description: string
  combobox: ComponentType<{
    id: string
    items: TItem[]
    value: TItem[]
    onValueChange: (items: TItem[]) => void
    search: ResourceComboboxSearch
    query: ResourceComboboxQuery
    pending: boolean
    disabled: boolean
  }>
  itemKey: (item: TItem) => string
  items: TItem[]
  search: ResourceComboboxSearch
  query: ResourceComboboxQuery
  pending: boolean
  isError: boolean
  onRetry: () => void
  emptyHint: string
  grant: (item: TItem) => Promise<void>
}) {
  const plural = `${noun}s`
  const batch = useBatchGrantSubmit<TItem>({
    label: noun,
    fallbackError: `Could not share ${plural}`,
    itemKey,
    grant,
    onSuccess: () => {
      onOpenChange(false)
    },
  })
  const inputId = useId()
  const locked = batch.isSubmitting || isError || pending

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="first-letter:uppercase">Share {plural}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void batch.submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor={inputId} className="first-letter:uppercase">
                {plural}
              </FieldLabel>
              <ResourceMultiCombobox
                id={inputId}
                items={items}
                value={batch.items}
                onValueChange={batch.setItems}
                search={search}
                query={query}
                pending={pending}
                disabled={locked}
              />
              {!isError && !pending && items.length === 0 && (
                <FieldDescription>{emptyHint}</FieldDescription>
              )}
            </Field>
            {isError && (
              <p className="text-destructive text-sm">
                Could not load {plural}.{' '}
                <button type="button" className="underline" onClick={onRetry}>
                  Retry
                </button>
              </p>
            )}
            {batch.errorMessage && <p className="text-destructive text-sm">{batch.errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={locked || batch.items.length === 0}
                loading={batch.isSubmitting}
              >
                Share {plural}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
