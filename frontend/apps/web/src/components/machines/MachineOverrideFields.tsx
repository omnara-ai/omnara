import { type ReactNode, useState } from 'react'

import { ChevronDownIcon } from '@/components/icons'
import { type ProviderOptionsDraft } from '@/components/machines/machineOverrides'
import {
  isMachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import { CollapseBody } from '@/components/ui/collapse-body'
import { Collapsible, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { type ProviderOptions, providerOptionStrings } from '@/lib/provider-options'

import { StartupScriptField } from './StartupScriptField'

export function OverridesCollapsible({
  title = 'Overrides',
  description,
  children,
}: {
  title?: string
  description?: string
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="text-muted-foreground group flex items-center gap-2 text-left text-sm">
        <ChevronDownIcon className="size-4 transition-transform group-data-[state=open]:rotate-180" />
        {title}
        {description && <span className="text-muted-foreground font-normal">— {description}</span>}
      </CollapsibleTrigger>
      <CollapseBody open={open}>
        <div className="pt-4">{children}</div>
      </CollapseBody>
    </Collapsible>
  )
}

function stringDefault(defaults: Partial<Record<string, string>>, key: string) {
  const value = defaults[key]
  return value === '' ? undefined : value
}

export function ProviderOptionsOverrideFields({
  idPrefix,
  pool,
  defaults,
  values,
  onChange,
}: {
  idPrefix: string
  pool: { provider: string; management_kind: string }
  /**
   * Pool provider options shown as placeholders for empty inputs. When set,
   * only real pool values appear — no example placeholders that could read as
   * inherited values.
   */
  defaults?: ProviderOptions
  values: ProviderOptionsDraft
  onChange: (values: ProviderOptionsDraft) => void
}) {
  if (!isMachinePoolProvider(pool.provider)) return null
  const definition = machinePoolProviderDefinitions[pool.provider]
  const defaultStrings = defaults && providerOptionStrings(defaults)
  const placeholders = defaultStrings
    ? {
        resource: stringDefault(defaultStrings, definition.resource.key),
        location: stringDefault(defaultStrings, definition.location.key),
        startupScript: stringDefault(defaultStrings, 'startup_script'),
      }
    : {
        resource: definition.resource.placeholder,
        location: definition.location.placeholder,
        startupScript: 'apt-get update\napt-get install -y ripgrep',
      }
  return (
    <>
      {pool.management_kind !== 'cluster' && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${idPrefix}-resource`}>{definition.resource.label}</FieldLabel>
            <Input
              id={`${idPrefix}-resource`}
              value={values.resource}
              autoComplete="off"
              placeholder={placeholders.resource}
              onChange={(event) => {
                onChange({ ...values, resource: event.target.value })
              }}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${idPrefix}-location`}>{definition.location.label}</FieldLabel>
            <Input
              id={`${idPrefix}-location`}
              value={values.location}
              autoComplete="off"
              placeholder={placeholders.location}
              onChange={(event) => {
                onChange({ ...values, location: event.target.value })
              }}
            />
          </Field>
        </div>
      )}
      <StartupScriptField
        id={`${idPrefix}-startup-script`}
        label="Startup script"
        provider={pool.provider}
        value={values.startupScript}
        placeholder={placeholders.startupScript}
        onChange={(startupScript) => {
          onChange({ ...values, startupScript })
        }}
      />
    </>
  )
}
