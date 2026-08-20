import { ChevronRightIcon, Trash2Icon } from 'lucide-react'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

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
  return (
    <Collapsible>
      <CollapsibleTrigger className="group flex items-center gap-1.5 text-left text-sm font-medium">
        <ChevronRightIcon className="text-muted-foreground size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        {title}
        {description && <span className="text-muted-foreground font-normal">— {description}</span>}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="pt-4">{children}</div>
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

export function CombinedEnvOverlayEditor({
  orgId,
  projectId,
  enabled,
  envRows,
  secretEnvRows,
  onChange,
}: {
  orgId: string
  projectId?: string
  enabled: boolean
  envRows: EnvOverlayRow[]
  secretEnvRows: SecretEnvOverlayRow[]
  onChange: (rows: { envRows: EnvOverlayRow[]; secretEnvRows: SecretEnvOverlayRow[] }) => void
}) {
  function updateEnvRow(id: string, patch: Partial<EnvOverlayRow>) {
    onChange({
      envRows: envRows.map((row) => (row.id === id ? { ...row, ...patch } : row)),
      secretEnvRows,
    })
  }
  function updateSecretRow(id: string, patch: Partial<SecretEnvOverlayRow>) {
    onChange({
      envRows,
      secretEnvRows: secretEnvRows.map((row) => (row.id === id ? { ...row, ...patch } : row)),
    })
  }
  const combinedRows = [
    ...envRows.map((row) => ({ kind: 'text' as const, ...row })),
    ...secretEnvRows.map((row) => ({ kind: 'secret' as const, ...row })),
  ]
  return (
    <Field>
      <FieldLabel>Environment variables</FieldLabel>
      {combinedRows.length > 0 && (
        <div className="space-y-2">
          {combinedRows.map((row) => {
            const unset = row.kind === 'text' ? row.value === null : row.secretId === null
            return (
              <div key={row.id} className="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2">
                <Input
                  value={row.key}
                  autoComplete="off"
                  placeholder="NAME"
                  aria-label="Variable name"
                  onChange={(event) => {
                    if (row.kind === 'text') {
                      updateEnvRow(row.id, { key: event.target.value })
                    } else {
                      updateSecretRow(row.id, { key: event.target.value })
                    }
                  }}
                />
                <Select
                  value={row.kind}
                  onValueChange={(kind) => {
                    if (kind === row.kind) return
                    if (kind === 'secret') {
                      onChange({
                        envRows: envRows.filter((candidate) => candidate.id !== row.id),
                        secretEnvRows: [
                          ...secretEnvRows,
                          { id: row.id, key: row.key, secretId: unset ? null : '' },
                        ],
                      })
                    } else {
                      onChange({
                        envRows: [
                          ...envRows,
                          { id: row.id, key: row.key, value: unset ? null : '' },
                        ],
                        secretEnvRows: secretEnvRows.filter((candidate) => candidate.id !== row.id),
                      })
                    }
                  }}
                >
                  <SelectTrigger className="w-24" aria-label="Variable type">
                    <SelectValue>{row.kind === 'text' ? 'Text' : 'Secret'}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="text">Text</SelectItem>
                    <SelectItem value="secret">Secret</SelectItem>
                  </SelectContent>
                </Select>
                {row.kind === 'text' ? (
                  <Input
                    value={row.value ?? ''}
                    autoComplete="off"
                    placeholder={unset ? 'unset — removes the pool value' : 'value'}
                    aria-label="Variable value"
                    onChange={(event) => {
                      updateEnvRow(row.id, { value: event.target.value })
                    }}
                  />
                ) : row.secretId === null ? (
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
                      updateSecretRow(row.id, { secretId })
                    }}
                  />
                )}
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  aria-label="Remove variable"
                  onClick={() => {
                    if (row.kind === 'text') {
                      onChange({
                        envRows: envRows.filter((candidate) => candidate.id !== row.id),
                        secretEnvRows,
                      })
                    } else {
                      onChange({
                        envRows,
                        secretEnvRows: secretEnvRows.filter((candidate) => candidate.id !== row.id),
                      })
                    }
                  }}
                >
                  <Trash2Icon />
                </Button>
              </div>
            )
          })}
        </div>
      )}
      <div className="mt-1 flex justify-end">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => {
            onChange({ envRows: [...envRows, newEnvOverlayRow()], secretEnvRows })
          }}
        >
          Add variable
        </Button>
      </div>
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
