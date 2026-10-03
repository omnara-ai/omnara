import type { ModelProviderConfig } from '@omnara/sdk'
import type { KeyboardEvent, ReactNode, Ref } from 'react'

import { ProjectShareChips } from '@/components/projects/ProjectShareChips'
import { Button } from '@/components/ui/button'
import { DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/** The picker's top bar: which provider to pick from, then the search box. */
export function ConfiguredModelPickerHeader({
  providers,
  providerId,
  onProviderChange,
  search,
  onSearchChange,
  onSearchKeyDown,
  searchRef,
  disabled,
}: {
  providers: ModelProviderConfig[]
  providerId: string
  onProviderChange: (providerId: string) => void
  search: string
  onSearchChange: (search: string) => void
  onSearchKeyDown: (event: KeyboardEvent<HTMLInputElement>) => void
  searchRef: Ref<HTMLInputElement>
  disabled: boolean
}) {
  const provider = providers.find((candidate) => candidate.id === providerId)
  return (
    <>
      <DialogHeader className="sr-only">
        <DialogTitle>Add models</DialogTitle>
        <DialogDescription>
          Choose models from {provider?.name ?? 'the provider'} to configure.
        </DialogDescription>
      </DialogHeader>
      <div className="-mx-4 -mt-4 flex items-center gap-2 border-b py-3 pl-4 pr-12 sm:-mx-6 sm:-mt-6 sm:py-4 sm:pl-6 sm:pr-14">
        {providers.length > 1 ? (
          <Select value={providerId} disabled={disabled} onValueChange={onProviderChange}>
            <SelectTrigger
              aria-label="Provider"
              className="text-muted-foreground h-8 w-auto max-w-48 shrink-0 border-0 bg-transparent px-1.5 shadow-none"
            >
              <SelectValue>{provider?.name}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {providers.map((option) => (
                <SelectItem key={option.id} value={option.id}>
                  {option.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <span className="text-muted-foreground max-w-48 shrink-0 truncate text-sm">
            {provider?.name}
          </span>
        )}
        <span aria-hidden="true" className="text-muted-foreground/50">
          /
        </span>
        <input
          ref={searchRef}
          aria-label="Search models"
          autoComplete="off"
          spellCheck={false}
          value={search}
          disabled={disabled}
          placeholder="Search models or enter a slug"
          className="placeholder:text-muted-foreground min-w-0 flex-1 bg-transparent font-mono text-sm outline-none placeholder:font-sans"
          onChange={(event) => {
            onSearchChange(event.target.value)
          }}
          onKeyDown={onSearchKeyDown}
        />
      </div>
    </>
  )
}

/** The picker's bottom bar: projects to share with, then dismiss and the primary action. */
export function ConfiguredModelPickerFooter({
  orgId,
  projectIds,
  onProjectIdsChange,
  failedProjectIds,
  submitting,
  dismissLabel,
  onDismiss,
  primaryLabel,
  primaryDisabled,
  onPrimary,
}: {
  orgId: string
  projectIds: string[]
  onProjectIdsChange: (projectIds: string[]) => void
  /** Selected projects whose share failed, highlighted so they can be removed before a retry. */
  failedProjectIds: string[]
  submitting: boolean
  /** Omitted to hide the secondary button. */
  dismissLabel?: ReactNode
  onDismiss: () => void
  primaryLabel: string
  primaryDisabled: boolean
  onPrimary: () => void
}) {
  return (
    <div className="-mx-4 -mb-4 flex flex-wrap items-center gap-3 border-t px-4 py-3 sm:-mx-6 sm:-mb-6 sm:px-6 sm:py-4">
      <ProjectShareChips
        orgId={orgId}
        value={projectIds}
        onChange={onProjectIdsChange}
        failedProjectIds={failedProjectIds}
        disabled={submitting}
        isProjectEligible={(project) => project.access.can_manage_access}
      />
      <div className="ml-auto flex gap-2">
        {dismissLabel !== undefined && (
          <Button type="button" variant="ghost" disabled={submitting} onClick={onDismiss}>
            {dismissLabel}
          </Button>
        )}
        <Button
          type="button"
          aria-keyshortcuts="Meta+Enter Control+Enter"
          disabled={primaryDisabled}
          loading={submitting}
          onClick={onPrimary}
        >
          {primaryLabel}
        </Button>
      </div>
    </div>
  )
}
