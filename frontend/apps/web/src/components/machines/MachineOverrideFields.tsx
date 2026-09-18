import type { Secret } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { PillTabs } from '@/components/agents/PillTabs'
import { ChevronDownIcon, PlusIcon, Trash2Icon } from '@/components/icons'
import {
  type EnvOverlayRow,
  newEnvOverlayRow,
  type ProviderOptionsDraft,
  type SecretEnvOverlayRow,
} from '@/components/machines/machineOverrides'
import {
  isMachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import { NewSecretDialog } from '@/components/secrets/NewSecretDialog'
import { SecretSelect } from '@/components/secrets/SecretTypeaheadField'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { type ProviderOptions, providerOptionStrings } from '@/lib/provider-options'

import { StartupScriptField } from './StartupScriptField'

export function OverridesCollapsible({
  title = 'Overrides',
  description,
  keepMounted = false,
  children,
}: {
  title?: string
  description?: string
  keepMounted?: boolean
  children: ReactNode
}) {
  return (
    <Collapsible>
      <CollapsibleTrigger className="text-muted-foreground group flex items-center gap-2 text-left text-sm">
        <ChevronDownIcon className="size-4 transition-transform group-data-[state=open]:rotate-180" />
        {title}
        {description && <span className="text-muted-foreground font-normal">— {description}</span>}
      </CollapsibleTrigger>
      <CollapsibleContent
        forceMount={keepMounted || undefined}
        className={keepMounted ? 'data-[state=closed]:hidden' : undefined}
      >
        <div className="pt-4">{children}</div>
      </CollapsibleContent>
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
  const [creatingSecretFor, setCreatingSecretFor] = useState<string | null>(null)
  const [createdSecrets, setCreatedSecrets] = useState<ReadonlyMap<string, Secret>>(new Map())
  function setKind(row: (typeof combinedRows)[number], kind: string, unset: boolean) {
    if (kind === row.kind) return
    if (kind === 'secret') {
      onChange({
        envRows: envRows.filter((candidate) => candidate.id !== row.id),
        secretEnvRows: [
          ...secretEnvRows,
          { id: row.id, key: row.key, secretId: unset ? null : '' },
        ],
      })
      return
    }
    onChange({
      envRows: [...envRows, { id: row.id, key: row.key, value: unset ? null : '' }],
      secretEnvRows: secretEnvRows.filter((candidate) => candidate.id !== row.id),
    })
  }
  function removeRow(row: (typeof combinedRows)[number]) {
    if (row.kind === 'text') {
      onChange({ envRows: envRows.filter((candidate) => candidate.id !== row.id), secretEnvRows })
      return
    }
    onChange({
      envRows,
      secretEnvRows: secretEnvRows.filter((candidate) => candidate.id !== row.id),
    })
  }
  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <FieldLabel>Environment variables</FieldLabel>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="text-muted-foreground -my-1"
          onClick={() => {
            onChange({ envRows: [...envRows, newEnvOverlayRow()], secretEnvRows })
          }}
        >
          <PlusIcon />
          Add variable
        </Button>
      </div>
      <div className="overflow-hidden rounded-xl border">
        <div className="after:border-border/50 after:bg-muted/50 after:shadow-xs relative isolate after:pointer-events-none after:absolute after:-inset-x-px after:-top-px after:-z-10 after:h-[calc(2.25rem+2px)] after:rounded-xl after:border">
          <Table className="table-fixed">
            <TableHeader className="bg-transparent [&_tr]:border-0">
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-[calc(2.25rem+3px)] w-40 px-4 pb-[3px]">Type</TableHead>
                <TableHead className="h-[calc(2.25rem+3px)] px-4 pb-[3px]">Key</TableHead>
                <TableHead className="h-[calc(2.25rem+3px)] px-4 pb-[3px]">Value</TableHead>
                <TableHead className="h-[calc(2.25rem+3px)] w-14 px-2 pb-[3px]">
                  <span className="sr-only">Remove</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {combinedRows.length === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={4}
                    className="text-muted-foreground whitespace-normal px-4 py-3 text-center"
                  >
                    No environment variables
                  </TableCell>
                </TableRow>
              )}
              {combinedRows.map((row) => {
                const unset = row.kind === 'text' ? row.value === null : row.secretId === null
                return (
                  <TableRow key={row.id} className="hover:bg-transparent">
                    <TableCell className="px-4">
                      <PillTabs
                        value={row.kind}
                        onValueChange={(kind) => {
                          setKind(row, kind, unset)
                        }}
                        tabs={[
                          { value: 'text', label: 'Text' },
                          { value: 'secret', label: 'Secret' },
                        ]}
                      />
                    </TableCell>
                    <TableCell className="px-4">
                      <Input
                        value={row.key}
                        autoComplete="off"
                        placeholder="NAME"
                        aria-label="Variable name"
                        className="font-mono"
                        onChange={(event) => {
                          if (row.kind === 'text') {
                            updateEnvRow(row.id, { key: event.target.value })
                          } else {
                            updateSecretRow(row.id, { key: event.target.value })
                          }
                        }}
                      />
                    </TableCell>
                    <TableCell className="px-4">
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
                          knownSecret={createdSecrets.get(row.secretId)}
                          onChange={(secretId) => {
                            updateSecretRow(row.id, { secretId })
                          }}
                          onCreateSecret={() => {
                            setCreatingSecretFor(row.id)
                          }}
                        />
                      )}
                    </TableCell>
                    <TableCell className="px-2">
                      <Button
                        type="button"
                        size="icon"
                        variant="ghost"
                        className="text-muted-foreground size-10 sm:size-8"
                        aria-label="Remove variable"
                        onClick={() => {
                          removeRow(row)
                        }}
                      >
                        <Trash2Icon />
                      </Button>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </div>
      </div>
      {creatingSecretFor !== null && (
        <NewSecretDialog
          orgId={orgId}
          projectId={projectId}
          defaultName={secretEnvRows.find((row) => row.id === creatingSecretFor)?.key ?? ''}
          onClose={() => {
            setCreatingSecretFor(null)
          }}
          onCreated={(secret) => {
            setCreatedSecrets((current) => new Map(current).set(secret.id, secret))
            updateSecretRow(creatingSecretFor, { secretId: secret.id })
            setCreatingSecretFor(null)
          }}
        />
      )}
    </Field>
  )
}
