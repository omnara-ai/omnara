import { useUpdateIntegration } from '@omnara/react'
import type { Integration, IntegrationKind } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { errorMessage } from '@/lib/submit-status'

import { IntegrationLauncherFields } from './IntegrationFormFields'
import {
  integrationFormRequest,
  type IntegrationFormValues,
  integrationFormValues,
  validateIntegrationForm,
} from './integrationFormState'

export interface IntegrationFormProps {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
  integration: Integration
  onSaved: (integration: Integration) => void
  onCancel?: () => void
  cancelLabel?: string
  defaultLauncherEnabled?: boolean
}

export function IntegrationForm(props: IntegrationFormProps) {
  const [base, setBase] = useState(props.integration)
  const stale = base.id !== props.integration.id || base.updated_at !== props.integration.updated_at
  return (
    <IntegrationFormEditor
      key={`${base.id}/${base.updated_at}`}
      {...props}
      integration={base}
      stale={stale}
      onReload={() => {
        setBase(props.integration)
      }}
    />
  )
}

function IntegrationFormEditor({
  orgId,
  projectId,
  integrationKind,
  integration,
  onSaved,
  onCancel,
  cancelLabel = 'Cancel',
  defaultLauncherEnabled = false,
  stale,
  onReload,
}: IntegrationFormProps & { stale: boolean; onReload: () => void }) {
  const update = useUpdateIntegration(orgId, projectId)
  const [values, setValues] = useState(() =>
    integrationFormValues(integrationKind, integration, defaultLauncherEnabled),
  )
  const [error, setError] = useState('')
  const submitting = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const busy = update.isPending
  const validation = validateIntegrationForm(integrationKind, values, integration)
  const missingProfile =
    integrationKind === 'github_pr' && values.launcher && values.profileIds.length === 0
  function change(patch: Partial<IntegrationFormValues>) {
    setValues((previous) => ({ ...previous, ...patch }))
  }
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting.current || stale || validation.error) return
    submitting.current = true
    setError('')
    try {
      const request = integrationFormRequest(integrationKind, values, integration)
      const saved = await update.mutateAsync({ integrationID: integration.id, ...request })
      if (mounted.current) onSaved(saved)
    } catch (cause) {
      setError(errorMessage(cause, 'Could not save integration.'))
    }
    submitting.current = false
  }
  return (
    <form onSubmit={(event) => void submit(event)}>
      <FieldGroup>
        {stale && (
          <div role="alert" className="flex flex-col gap-2 text-sm">
            <p>
              Integration changed. Reload settings before saving. Reloading discards unsaved edits.
            </p>
            <Button type="button" variant="outline" disabled={busy} onClick={onReload}>
              Reload settings
            </Button>
          </div>
        )}
        <fieldset disabled={busy || stale} className="flex flex-col gap-6">
          <IntegrationLauncherFields
            orgId={orgId}
            projectId={projectId}
            integrationKind={integrationKind}
            integration={integration}
            values={values}
            onChange={change}
            disabled={busy || stale}
            profileCount={validation.profileCount}
          />
        </fieldset>
        {error && (
          <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
            {error}
          </p>
        )}
        {!error && validation.error && !missingProfile && (
          <p className="text-muted-foreground text-sm">{validation.error}</p>
        )}
        <div className="flex justify-end gap-2">
          {onCancel && (
            <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
              {cancelLabel}
            </Button>
          )}
          <Button
            type="submit"
            loading={busy}
            disabled={busy || stale || Boolean(validation.error)}
          >
            Save changes
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}
