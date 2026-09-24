import { useUpdateProjectIntegration } from '@omnara/react'
import type { IntegrationType, ProjectIntegration } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { errorMessage } from '@/lib/submit-status'

import { ProjectIntegrationLauncherFields } from './ProjectIntegrationFormFields'
import {
  projectIntegrationFormRequest,
  type ProjectIntegrationFormValues,
  projectIntegrationFormValues,
  validateProjectIntegrationForm,
} from './projectIntegrationFormState'

export interface ProjectIntegrationFormProps {
  orgId: string
  projectId: string
  integrationType: IntegrationType
  integration: ProjectIntegration
  onSaved: (integration: ProjectIntegration) => void
  onCancel?: () => void
  cancelLabel?: string
}

export function ProjectIntegrationForm(props: ProjectIntegrationFormProps) {
  const [base, setBase] = useState(props.integration)
  const stale = base.id !== props.integration.id || base.updated_at !== props.integration.updated_at
  return (
    <ProjectIntegrationFormEditor
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

function ProjectIntegrationFormEditor({
  orgId,
  projectId,
  integrationType,
  integration,
  onSaved,
  onCancel,
  cancelLabel = 'Cancel',
  stale,
  onReload,
}: ProjectIntegrationFormProps & { stale: boolean; onReload: () => void }) {
  const update = useUpdateProjectIntegration(orgId, projectId)
  const [values, setValues] = useState(() =>
    projectIntegrationFormValues(integrationType, integration),
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
  const validation = validateProjectIntegrationForm(integrationType, values, integration)
  function change(patch: Partial<ProjectIntegrationFormValues>) {
    setValues((previous) => ({ ...previous, ...patch }))
  }
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting.current || stale || validation.error) return
    submitting.current = true
    setError('')
    try {
      const request = projectIntegrationFormRequest(integrationType, values, integration)
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
          <ProjectIntegrationLauncherFields
            orgId={orgId}
            projectId={projectId}
            integrationType={integrationType}
            integration={integration}
            values={values}
            onChange={change}
            disabled={busy || stale}
            workspaceId={integration.provider_tenant_id}
            slotCount={validation.slotCount}
          />
        </fieldset>
        {error && (
          <p role="alert" className="text-destructive whitespace-pre-wrap text-sm">
            {error}
          </p>
        )}
        {!error && validation.error && (
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
