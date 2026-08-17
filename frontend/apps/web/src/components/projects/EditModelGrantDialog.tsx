import { useConfiguredModels, useUpdateProjectModelGrant } from '@omnara/react'
import { type ConfiguredModel, type ProjectModelGrantListItem } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

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
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError, submitting } from '@/lib/submit-status'

import {
  type CacheRetentionDraft,
  type InheritableToggleDraft,
  MODEL_GRANT_TEXT_FIELDS,
  MODEL_GRANT_TOKEN_FIELDS,
  modelGrantDraftFromGrant,
  type ModelGrantOverrideDraft,
  modelGrantOverridesValid,
  modelGrantUpdateRequest,
} from './EditModelGrantDialogState'

const CACHE_RETENTIONS: Exclude<CacheRetentionDraft, 'inherit'>[] = ['none', 'short', 'long']

function placeholderText(value: string | undefined) {
  return value === '' ? undefined : value
}

function InheritableToggleField({
  label,
  value,
  inheritedValue,
  onValueChange,
}: {
  label: string
  value: InheritableToggleDraft
  inheritedValue: boolean | undefined
  onValueChange: (value: InheritableToggleDraft) => void
}) {
  const inheritLabel =
    inheritedValue == null ? 'Inherit' : `Inherit (${inheritedValue ? 'enabled' : 'disabled'})`
  return (
    <Field>
      <FieldLabel>{label}</FieldLabel>
      <Select
        value={value}
        onValueChange={(next) => {
          onValueChange(next as InheritableToggleDraft)
        }}
      >
        <SelectTrigger className="w-full">
          <SelectValue>
            {value === 'inherit' ? inheritLabel : value === 'enabled' ? 'Enabled' : 'Disabled'}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="inherit">{inheritLabel}</SelectItem>
          <SelectItem value="enabled">Enabled</SelectItem>
          <SelectItem value="disabled">Disabled</SelectItem>
        </SelectContent>
      </Select>
    </Field>
  )
}

export function EditModelGrantDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
  item,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
  item: ProjectModelGrantListItem
}) {
  const updateGrant = useUpdateProjectModelGrant(orgId, projectId)
  const modelsQuery = useConfiguredModels(orgId, item.model.model_provider_config_id, {
    enabled: open,
  })
  const completeModels = useCompleteInfiniteQueryItems(modelsQuery, open)
  const model: ConfiguredModel | undefined = completeModels.items.find(
    (candidate) => candidate.id === item.grant.configured_model_id,
  )
  const [draft, setDraft] = useState<ModelGrantOverrideDraft>(() =>
    modelGrantDraftFromGrant(item.grant),
  )
  const [status, setStatus] = useState<SubmitStatus>(idle)
  const isSubmitting = status.phase === 'submitting'
  const errorMessage = statusError(status)
  const idPrefix = `${item.grant.id}-edit`

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setStatus(submitting)
    try {
      await updateGrant.mutateAsync({
        modelGrantID: item.grant.id,
        ...modelGrantUpdateRequest(draft),
      })
    } catch (err) {
      setStatus(submitError(err, 'Could not update model grant'))
      return
    }
    setStatus(idle)
    onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Edit model grant</DialogTitle>
          <DialogDescription>
            Overrides for {item.model.name} in this project. Overrides can only narrow what the
            configured model allows.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <FieldDescription>
              Configured-model values are shown as placeholders. Fields left empty inherit from the
              configured model; values you enter are stored as overrides for this grant.
            </FieldDescription>
            <div className="grid gap-4 sm:grid-cols-2">
              {MODEL_GRANT_TOKEN_FIELDS.map((field) => {
                const inherited = model ? field.inherited(model) : null
                return (
                  <Field key={field.key}>
                    <FieldLabel htmlFor={`${idPrefix}-${field.key}`}>{field.label}</FieldLabel>
                    <Input
                      id={`${idPrefix}-${field.key}`}
                      type="number"
                      min={0}
                      value={draft[field.key]}
                      placeholder={inherited == null ? undefined : String(inherited)}
                      onChange={(event) => {
                        setDraft({ ...draft, [field.key]: event.target.value })
                      }}
                    />
                  </Field>
                )
              })}
            </div>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field>
                <FieldLabel>Cache retention</FieldLabel>
                <Select
                  value={draft.cacheRetention}
                  onValueChange={(cacheRetention) => {
                    setDraft({ ...draft, cacheRetention: cacheRetention as CacheRetentionDraft })
                  }}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue>
                      {draft.cacheRetention === 'inherit'
                        ? model
                          ? `Inherit (${model.default_cache_retention})`
                          : 'Inherit'
                        : draft.cacheRetention}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="inherit">
                      {model ? `Inherit (${model.default_cache_retention})` : 'Inherit'}
                    </SelectItem>
                    {CACHE_RETENTIONS.map((retention) => (
                      <SelectItem key={retention} value={retention}>
                        {retention}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <InheritableToggleField
                label="Tools"
                value={draft.supportsTools}
                inheritedValue={model?.supports_tools}
                onValueChange={(supportsTools) => {
                  setDraft({ ...draft, supportsTools })
                }}
              />
              <InheritableToggleField
                label="Reasoning"
                value={draft.supportsReasoning}
                inheritedValue={model?.supports_reasoning}
                onValueChange={(supportsReasoning) => {
                  setDraft({ ...draft, supportsReasoning })
                }}
              />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              {MODEL_GRANT_TEXT_FIELDS.map((field) => (
                <Field key={field.key}>
                  <FieldLabel htmlFor={`${idPrefix}-${field.key}`}>{field.label}</FieldLabel>
                  <Input
                    id={`${idPrefix}-${field.key}`}
                    value={draft[field.key]}
                    placeholder={placeholderText(model ? field.inherited(model) : undefined)}
                    onChange={(event) => {
                      setDraft({ ...draft, [field.key]: event.target.value })
                    }}
                  />
                  {field.commaSeparated && <FieldDescription>Comma-separated.</FieldDescription>}
                </Field>
              ))}
            </div>
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={isSubmitting || !modelGrantOverridesValid(draft)}
                loading={isSubmitting}
              >
                Save changes
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
