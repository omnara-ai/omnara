import { useProjectApp } from '@omnara/react'
import { type CronTriggerTarget, zJsonText } from '@omnara/sdk'
import { useEffect, useState } from 'react'

import {
  appScheduleFieldError,
  appScheduleFields,
  appScheduleSettings,
} from '@/components/apps/app-schedule-schema'
import { ProjectAppProfilePicker } from '@/components/apps/ProjectAppProfilePicker'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

export function AppCronTriggerFields({
  orgId,
  projectId,
  value,
  onChange,
  onValidityChange,
}: {
  orgId: string
  projectId: string
  value: Extract<CronTriggerTarget, { type: 'app' }>
  onChange: (value: Extract<CronTriggerTarget, { type: 'app' }>) => void
  onValidityChange: (valid: boolean) => void
}) {
  const app = useProjectApp(orgId, projectId, value.app_id)
  const schema = app.data?.capabilities.schedule?.input_schema
  const [jsonDraft, setJsonDraft] = useState<string>()
  const fields =
    schema && jsonDraft === undefined ? appScheduleFields(schema, value.settings) : null
  const json = jsonDraft ?? JSON.stringify(value.settings, null, 2)
  const jsonSettings = zJsonText.pipe(appScheduleSettings).safeParse(json)
  const valid = Boolean(
    schema &&
    (fields
      ? fields.every(
          ({ value, property, required }) => !appScheduleFieldError(value, property, required),
        )
      : jsonSettings.success),
  )
  useEffect(() => {
    onValidityChange(valid)
  }, [onValidityChange, valid])

  if (!app.data)
    return (
      <div role="status" className="text-muted-foreground text-sm">
        {app.isError ? (
          <>
            Could not load schedule settings.{' '}
            <Button type="button" variant="link" onClick={() => void app.refetch()}>
              Retry settings
            </Button>
          </>
        ) : (
          'Loading schedule settings…'
        )}
      </div>
    )
  if (!schema) return <p role="alert">This app does not support schedules.</p>
  if (!fields)
    return (
      <Field>
        <FieldLabel htmlFor="cron-trigger-settings">Settings (JSON)</FieldLabel>
        <Textarea
          id="cron-trigger-settings"
          className="font-mono"
          rows={8}
          value={json}
          aria-invalid={!jsonSettings.success}
          onChange={(event) => {
            const next = event.target.value
            setJsonDraft(next)
            const parsed = zJsonText.pipe(appScheduleSettings).safeParse(next)
            if (parsed.success) onChange({ ...value, settings: parsed.data })
          }}
        />
        <FieldDescription>
          {app.data.capabilities.schedule?.description ??
            'Enter the settings for this scheduled action.'}
        </FieldDescription>
        {!jsonSettings.success && (
          <p role="alert" className="text-destructive text-sm">
            Enter a JSON object.
          </p>
        )}
        <details className="text-muted-foreground text-sm">
          <summary>Settings schema</summary>
          <pre className="max-h-48 overflow-auto whitespace-pre-wrap">
            {JSON.stringify(schema, null, 2)}
          </pre>
        </details>
      </Field>
    )

  return fields.map(({ key, property, required, value: current }) => {
    const label = property.title ?? key
    const description = property.description
    const error = appScheduleFieldError(current, property, required)
    const change = (next: string) => {
      const settings = Object.fromEntries(
        Object.entries(value.settings).filter(([name]) => name !== key),
      )
      if (required || next !== '') settings[key] = next
      onChange({ ...value, settings })
    }
    if (property['x-omnara-control'] === 'agent_profile')
      return (
        <ProjectAppProfilePicker
          key={key}
          orgId={orgId}
          projectId={projectId}
          single
          label={label}
          description={description ?? 'Choose a profile for future runs.'}
          value={current ? [{ id: current, name: current }] : []}
          onChange={(profiles) => {
            change(profiles[0]?.id ?? '')
          }}
        />
      )
    const Control = property['x-omnara-control'] === 'textarea' ? Textarea : Input
    return (
      <Field key={key}>
        <FieldLabel htmlFor={`cron-trigger-setting-${key}`}>{label}</FieldLabel>
        <Control
          id={`cron-trigger-setting-${key}`}
          required={required}
          value={current ?? ''}
          aria-invalid={Boolean(current && error)}
          onChange={(event) => {
            change(event.target.value)
          }}
        />
        {description && <FieldDescription>{description}</FieldDescription>}
        {Boolean(current && error) && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
      </Field>
    )
  })
}
