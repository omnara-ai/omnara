import { Trash2Icon } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'

import type {
  BasicMachineSource,
  MachineSourceKind,
} from '@/components/agents/agentConfigBasic'
import {
  MachineSourceCombobox,
  PoolSourceCombobox,
} from '@/components/agents/AgentConfigMachineSourceComboboxes'
import { SourceOverridesSection } from '@/components/agents/AgentConfigMachineSourceOverrides'
import { emptyProviderOptions } from '@/components/machines/machineOverrides'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

function newMachineSource(kind: MachineSourceKind): BasicMachineSource {
  return {
    id: crypto.randomUUID(),
    kind,
    name: '',
    provider: '',
    managementKind: '',
    defaultCwd: '',
    initialNumMachines: '',
    maxMachines: '',
    machineCpu: '',
    machineMemoryMb: '',
    providerOptions: emptyProviderOptions,
    envRows: [],
    secretEnvRows: [],
  }
}

export function AgentConfigMachineSourcesField({
  orgId,
  projectId,
  sources,
  onSourcesChange,
  onUnavailableIdsChange,
}: {
  orgId: string
  projectId: string
  sources: BasicMachineSource[]
  onSourcesChange: (sources: BasicMachineSource[]) => void
  onUnavailableIdsChange: (ids: string[]) => void
}) {
  function updateSource(id: string, patch: Partial<BasicMachineSource>) {
    onSourcesChange(sources.map((source) => (source.id === id ? { ...source, ...patch } : source)))
  }

  const [unavailableIds, setUnavailableIds] = useState<ReadonlySet<string>>(new Set())
  const reportAvailability = useCallback((id: string, unavailable: boolean) => {
    setUnavailableIds((prev) => {
      if (prev.has(id) === unavailable) return prev
      const next = new Set(prev)
      if (unavailable) {
        next.add(id)
      } else {
        next.delete(id)
      }
      return next
    })
  }, [])
  useEffect(() => {
    // Filter to live rows: a removed row's combobox never reports back.
    onUnavailableIdsChange(
      sources.filter((source) => unavailableIds.has(source.id)).map((source) => source.id),
    )
  }, [onUnavailableIdsChange, sources, unavailableIds])

  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>Machine sources</FieldLabel>
          <FieldDescription>Machines the agent can run commands on.</FieldDescription>
        </div>
        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button type="button" size="sm" variant="outline">
                Add source
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onSelect={() => {
                  onSourcesChange([...sources, newMachineSource('pool')])
                }}
              >
                Machine pool
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => {
                  onSourcesChange([...sources, newMachineSource('machine')])
                }}
              >
                Existing machine
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      <div className="space-y-3">
        {sources.length === 0 ? (
          <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
            No machine sources
          </div>
        ) : (
          sources.map((source) => (
            <div
              key={source.id}
              className="border-border bg-background space-y-4 rounded-lg border p-4"
            >
              <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
                <Field>
                  <FieldLabel>{source.kind === 'pool' ? 'Machine pool' : 'Machine'}</FieldLabel>
                  {source.kind === 'pool' ? (
                    <PoolSourceCombobox
                      orgId={orgId}
                      projectId={projectId}
                      value={source.name}
                      onChange={(name, pool) => {
                        if (name === source.name) return
                        updateSource(source.id, {
                          name,
                          provider: pool?.provider ?? '',
                          managementKind: pool?.management_kind ?? '',
                          machineCpu: '',
                          machineMemoryMb: '',
                          providerOptions: emptyProviderOptions,
                          envRows: [],
                          secretEnvRows: [],
                        })
                      }}
                      onUnavailableChange={(unavailable) => {
                        reportAvailability(source.id, unavailable)
                      }}
                      onPoolResolved={(pool) => {
                        if (
                          source.provider === pool.provider &&
                          source.managementKind === pool.management_kind
                        ) {
                          return
                        }
                        updateSource(source.id, {
                          provider: pool.provider,
                          managementKind: pool.management_kind,
                        })
                      }}
                    />
                  ) : (
                    <MachineSourceCombobox
                      orgId={orgId}
                      projectId={projectId}
                      value={source.name}
                      onChange={(name) => {
                        updateSource(source.id, { name })
                      }}
                      onUnavailableChange={(unavailable) => {
                        reportAvailability(source.id, unavailable)
                      }}
                      onMachinesGranted={(names) => {
                        const [first, ...rest] = names
                        if (first === undefined) return
                        onSourcesChange([
                          ...sources.map((candidate) =>
                            candidate.id === source.id ? { ...candidate, name: first } : candidate,
                          ),
                          ...rest.map((name) => ({ ...newMachineSource('machine'), name })),
                        ])
                      }}
                    />
                  )}
                  {unavailableIds.has(source.id) && (
                    <p className="text-destructive text-sm">
                      {source.kind === 'pool'
                        ? 'This machine pool is no longer available to the project. Pick another pool or remove the source.'
                        : 'This machine is no longer available to the project. Pick another machine or remove the source.'}
                    </p>
                  )}
                </Field>
                <div className="hidden items-end sm:flex">
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    aria-label="Remove machine source"
                    onClick={() => {
                      onSourcesChange(sources.filter((candidate) => candidate.id !== source.id))
                    }}
                  >
                    <Trash2Icon />
                  </Button>
                </div>
              </div>
              <div className="grid gap-4 sm:grid-cols-3">
                {source.kind === 'pool' && (
                  <>
                    <Field>
                      <FieldLabel htmlFor={`${source.id}-initial`}>Initial machines</FieldLabel>
                      <Input
                        id={`${source.id}-initial`}
                        type="number"
                        min={1}
                        value={source.initialNumMachines}
                        placeholder="1"
                        onChange={(event) => {
                          updateSource(source.id, { initialNumMachines: event.target.value })
                        }}
                      />
                    </Field>
                    <Field>
                      <FieldLabel htmlFor={`${source.id}-max`}>Max machines</FieldLabel>
                      <Input
                        id={`${source.id}-max`}
                        type="number"
                        min={1}
                        value={source.maxMachines}
                        placeholder="1"
                        onChange={(event) => {
                          updateSource(source.id, { maxMachines: event.target.value })
                        }}
                      />
                    </Field>
                  </>
                )}
                <Field className={source.kind === 'machine' ? 'sm:col-span-3' : ''}>
                  <FieldLabel htmlFor={`${source.id}-cwd`}>Working directory (optional)</FieldLabel>
                  <Input
                    id={`${source.id}-cwd`}
                    value={source.defaultCwd}
                    placeholder="/workspace"
                    onChange={(event) => {
                      updateSource(source.id, { defaultCwd: event.target.value })
                    }}
                  />
                </Field>
              </div>
              <SourceOverridesSection
                orgId={orgId}
                projectId={projectId}
                source={source}
                onChange={(patch) => {
                  updateSource(source.id, patch)
                }}
              />
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="sm:hidden"
                onClick={() => {
                  onSourcesChange(sources.filter((candidate) => candidate.id !== source.id))
                }}
              >
                <Trash2Icon />
                Remove source
              </Button>
            </div>
          ))
        )}
      </div>
    </Field>
  )
}
