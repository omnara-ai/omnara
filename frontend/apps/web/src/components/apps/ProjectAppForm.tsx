import { useCreateProjectApp, useUpdateProjectApp } from '@omnara/react'
import type { AppType, ProjectApp } from '@omnara/sdk'
import { type SyntheticEvent, useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/submit-status'

import { ProjectAppLauncherFields } from './ProjectAppFormFields'
import {
  projectAppFormRequest,
  type ProjectAppFormValues,
  projectAppFormValues,
  validateProjectAppForm,
} from './projectAppFormState'

export interface ProjectAppFormProps {
  orgId: string
  projectId: string
  appType: AppType
  app?: ProjectApp
  onSaved: (app: ProjectApp) => void
  onCancel?: () => void
}

export function ProjectAppForm(props: ProjectAppFormProps) {
  const [base, setBase] = useState(props.app)
  const stale = Boolean(
    base && props.app && (base.id !== props.app.id || base.updated_at !== props.app.updated_at),
  )
  return (
    <ProjectAppFormEditor
      key={base ? `${base.id}/${base.updated_at}` : 'create'}
      {...props}
      app={base}
      stale={stale}
      onReload={() => {
        setBase(props.app)
      }}
    />
  )
}

function ProjectAppFormEditor({
  orgId,
  projectId,
  appType,
  app,
  onSaved,
  onCancel,
  stale,
  onReload,
}: ProjectAppFormProps & { stale: boolean; onReload: () => void }) {
  const create = useCreateProjectApp(orgId, projectId)
  const update = useUpdateProjectApp(orgId, projectId)
  const [values, setValues] = useState(() => projectAppFormValues(appType, app))
  const [error, setError] = useState('')
  const submitting = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const busy = create.isPending || update.isPending
  const validation = validateProjectAppForm(appType, values, app)
  function change(patch: Partial<ProjectAppFormValues>) {
    setValues((previous) => ({ ...previous, ...patch }))
  }
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (submitting.current || stale || validation.error) return
    submitting.current = true
    setError('')
    try {
      const request = projectAppFormRequest(appType, values, app)
      let saved: ProjectApp
      if (app) saved = await update.mutateAsync({ appID: app.id, ...request })
      else saved = await create.mutateAsync(request)
      if (mounted.current) onSaved(saved)
    } catch (cause) {
      setError(errorMessage(cause, 'Could not save app.'))
    }
    submitting.current = false
  }
  return (
    <form onSubmit={(event) => void submit(event)}>
      <FieldGroup>
        {stale && (
          <div role="alert" className="flex flex-col gap-2 text-sm">
            <p>App changed. Reload settings before saving. Reloading discards unsaved edits.</p>
            <Button type="button" variant="outline" disabled={busy} onClick={onReload}>
              Reload settings
            </Button>
          </div>
        )}
        <fieldset disabled={busy || stale} className="flex flex-col gap-6">
          {!app && (
            <Field>
              <FieldLabel htmlFor="app-name">App name</FieldLabel>
              <Input
                id="app-name"
                name="name"
                required
                maxLength={32}
                pattern="[A-Za-z][A-Za-z0-9-]{0,31}"
                value={values.name}
                onChange={(event) => {
                  change({ name: event.target.value })
                }}
              />
              <FieldDescription>
                Start with a letter; use up to 32 letters, numbers or hyphens. This name cannot be
                changed.
              </FieldDescription>
            </Field>
          )}
          {app && (
            <ProjectAppLauncherFields
              orgId={orgId}
              projectId={projectId}
              appType={appType}
              app={app}
              values={values}
              onChange={change}
              disabled={busy || stale}
              workspaceId={app.provider_tenant_id}
              slotCount={validation.slotCount}
            />
          )}
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
              Cancel
            </Button>
          )}
          <Button
            type="submit"
            loading={busy}
            disabled={busy || stale || Boolean(validation.error)}
          >
            {app ? 'Save changes' : 'Continue to connection'}
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}
