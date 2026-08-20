import { ChevronDownIcon, Trash2Icon } from 'lucide-react'
import type { ReactNode } from 'react'

import {
  type EnvOverlayRow,
  newEnvOverlayRow,
  newSecretEnvOverlayRow,
  type ProviderOptionsDraft,
  type SecretEnvOverlayRow,
} from '@/components/machines/machineOverrides'
import {
  isMachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import { SecretSelect } from '@/components/secrets/SecretTypeaheadField'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { StartupScriptField } from './StartupScriptField'

export function OverridesCollapsible({
  title = 'Overrides',
  children,
}: {
  title?: string
  children: ReactNode
}) {
  return (
    <Collapsible className="border-border rounded-md border">
      <CollapsibleTrigger className="text-muted-foreground group flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm">
        {title}
        <ChevronDownIcon className="size-4 transition-transform group-data-[state=open]:rotate-180" />
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="p-3">{children}</div>
      </CollapsibleContent>
    </Collapsible>
  )
}

function stringDefault(defaults: Record<string, unknown>, key: string) {
  const value = defaults[key]
  return typeof value === 'string' && value !== '' ? value : undefined
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
  defaults?: Record<string, unknown>
  values: ProviderOptionsDraft
  onChange: (values: ProviderOptionsDraft) => void
}) {
  if (!isMachinePoolProvider(pool.provider)) return null
  const definition = machinePoolProviderDefinitions[pool.provider]
  const placeholders = defaults
    ? {
        resource: stringDefault(defaults, definition.resource.key),
        location: stringDefault(defaults, definition.location.key),
        startupScript: stringDefault(defaults, 'startup_script'),
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

export function EnvOverlayEditor({
  label,
  description,
  rows,
  onRowsChange,
}: {
  label: string
  description?: string
  rows: EnvOverlayRow[]
  onRowsChange: (rows: EnvOverlayRow[]) => void
}) {
  function updateRow(id: string, patch: Partial<EnvOverlayRow>) {
    onRowsChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)))
  }
  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>{label}</FieldLabel>
          {description && <FieldDescription>{description}</FieldDescription>}
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => {
            onRowsChange([...rows, newEnvOverlayRow()])
          }}
        >
          Add variable
        </Button>
      </div>
      {rows.length > 0 && (
        <div className="space-y-2">
          {rows.map((row) => (
            <div key={row.id} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
              <Input
                value={row.key}
                autoComplete="off"
                placeholder="NAME"
                aria-label="Variable name"
                onChange={(event) => {
                  updateRow(row.id, { key: event.target.value })
                }}
              />
              <Input
                value={row.value ?? ''}
                autoComplete="off"
                placeholder={row.value === null ? 'unset — removes the pool value' : 'value'}
                aria-label="Variable value"
                onChange={(event) => {
                  updateRow(row.id, { value: event.target.value })
                }}
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label="Remove variable"
                onClick={() => {
                  onRowsChange(rows.filter((candidate) => candidate.id !== row.id))
                }}
              >
                <Trash2Icon />
              </Button>
            </div>
          ))}
        </div>
      )}
    </Field>
  )
}

export function SecretEnvOverlayEditor({
  orgId,
  projectId,
  enabled,
  label,
  description,
  rows,
  onRowsChange,
}: {
  orgId: string
  /** When set, offers project-available secrets instead of org-owned ones. */
  projectId?: string
  enabled: boolean
  label: string
  description?: string
  rows: SecretEnvOverlayRow[]
  onRowsChange: (rows: SecretEnvOverlayRow[]) => void
}) {
  function updateRow(id: string, patch: Partial<SecretEnvOverlayRow>) {
    onRowsChange(rows.map((row) => (row.id === id ? { ...row, ...patch } : row)))
  }
  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>{label}</FieldLabel>
          {description && <FieldDescription>{description}</FieldDescription>}
        </div>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => {
            onRowsChange([...rows, newSecretEnvOverlayRow()])
          }}
        >
          Add variable
        </Button>
      </div>
      {rows.length > 0 && (
        <div className="space-y-2">
          {rows.map((row) => (
            <div key={row.id} className="grid grid-cols-[1fr_1fr_auto] items-center gap-2">
              <Input
                value={row.key}
                autoComplete="off"
                placeholder="NAME"
                aria-label="Variable name"
                onChange={(event) => {
                  updateRow(row.id, { key: event.target.value })
                }}
              />
              {row.secretId === null ? (
                <Input
                  disabled
                  value=""
                  placeholder="unset — removes the pool value"
                  aria-label="Variable value"
                />
              ) : (
                <SecretSelect
                  orgId={orgId}
                  projectId={projectId}
                  enabled={enabled}
                  value={row.secretId}
                  onChange={(secretId) => {
                    updateRow(row.id, { secretId })
                  }}
                />
              )}
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label="Remove variable"
                onClick={() => {
                  onRowsChange(rows.filter((candidate) => candidate.id !== row.id))
                }}
              >
                <Trash2Icon />
              </Button>
            </div>
          ))}
        </div>
      )}
    </Field>
  )
}
