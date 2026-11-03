import type { CronTriggerTarget } from '@omnara/sdk'

import { BrandMark } from '@/components/brand/OmnaraMark'
import {
  integrationScheduleEditor,
  integrationScheduleFieldError,
  integrationScheduleJson,
} from '@/components/integrations/integration-schedule-schema'
import { IntegrationIcon } from '@/components/integrations/IntegrationIcon'
import { IntegrationProfilePicker } from '@/components/integrations/IntegrationProfilePicker'
import type { useIntegrationScheduleSettings } from '@/components/integrations/useIntegrationScheduleSettings'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

type IntegrationTarget = Extract<CronTriggerTarget, { type: 'integration' }>

export function IntegrationCronTriggerFields({
  orgId,
  projectId,
  schedule: { integration, jsonDraft, setJsonDraft },
  value,
  onChange,
}: {
  orgId: string
  projectId: string
  schedule: ReturnType<typeof useIntegrationScheduleSettings>
  value: IntegrationTarget
  onChange: (value: IntegrationTarget) => void
}) {
  if (!integration.data)
    return (
      <div role="status" className="text-muted-foreground text-sm">
        {integration.isError ? (
          <>
            Could not load schedule settings.{' '}
            <Button type="button" variant="link" onClick={() => void integration.refetch()}>
              Retry settings
            </Button>
          </>
        ) : (
          'Loading schedule settings…'
        )}
      </div>
    )
  const capability = integration.data.capabilities.schedule
  if (!capability) return <p role="alert">This integration does not support schedules.</p>
  const { form, json, jsonValid } = integrationScheduleEditor(
    capability.input_schema,
    value.settings,
    jsonDraft,
  )
  if (!form)
    return (
      <Field>
        <FieldLabel htmlFor="cron-trigger-settings">Settings (JSON)</FieldLabel>
        <Textarea
          id="cron-trigger-settings"
          className="font-mono"
          rows={8}
          value={json}
          aria-invalid={!jsonValid}
          onChange={(event) => {
            const next = event.target.value
            setJsonDraft(next)
            const parsed = integrationScheduleJson.safeParse(next)
            if (parsed.success) onChange({ ...value, settings: parsed.data })
          }}
        />
        <FieldDescription>
          {capability.description ?? 'Enter the settings for this scheduled action.'}
        </FieldDescription>
        {!jsonValid && (
          <p role="alert" className="text-destructive text-sm">
            Enter a JSON object.
          </p>
        )}
        <details className="text-muted-foreground text-sm">
          <summary>Settings schema</summary>
          <pre className="max-h-48 overflow-auto whitespace-pre-wrap">
            {JSON.stringify(capability.input_schema, null, 2)}
          </pre>
        </details>
      </Field>
    )

  const renderField = ({
    key,
    property,
    required,
    value: current,
  }: (typeof form.fields)[number]) => {
    const label = property.title ?? key
    const description = property.description
    const error = integrationScheduleFieldError(current, property, required)
    const change = (next: string) => {
      const settings = Object.fromEntries(
        Object.entries(value.settings).filter(([name]) => name !== key),
      )
      if (required || next !== '') settings[key] = next
      onChange({ ...value, settings })
    }
    if (property['x-omnara-control'] === 'agent_profile')
      return (
        <IntegrationProfilePicker
          key={key}
          orgId={orgId}
          projectId={projectId}
          single
          label={label}
          description={description}
          value={current ? [current] : []}
          onChange={(profileIds) => {
            change(profileIds[0] ?? '')
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
  }

  return (
    <>
      {form.fields.map(renderField)}
      {form.sections.map((section) => (
        <div
          key={section.title}
          role="group"
          aria-label={section.title}
          className="flex flex-col gap-5 rounded-lg border p-4"
        >
          <div className="flex flex-col gap-1.5 text-sm">
            <h3 className="flex items-center gap-2 font-medium">
              {section.icon === 'omnara' ? (
                <BrandMark className="size-5" />
              ) : (
                <IntegrationIcon
                  integrationKind={integration.data.integration_kind}
                  className="size-5"
                />
              )}
              {section.title}
            </h3>
            {section.description && <p className="text-muted-foreground">{section.description}</p>}
          </div>
          {section.fields.map(renderField)}
        </div>
      ))}
    </>
  )
}
