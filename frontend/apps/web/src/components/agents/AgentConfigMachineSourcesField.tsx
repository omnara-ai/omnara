import { PlusIcon, Trash2Icon } from 'lucide-react'
import { useEffect, useState } from 'react'

import {
  MachineSourceCombobox,
  PoolSourceCombobox,
} from '@/components/agents/AgentConfigMachineSourceComboboxes'
import { SourceOverridesSection } from '@/components/agents/AgentConfigMachineSourceOverrides'
import { type BasicMachineSource, newMachineSource } from '@/components/agents/useAgentBuilderForm'
import { emptyProviderOptions } from '@/components/machines/machineOverrides'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field, FieldLabel, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'

export function AgentConfigMachineSourcesField({
  orgId,
  projectId,
  sources,
  onSourcesChange,
  onUnavailableIdsChange,
  showMissingToolsWarning,
  onAddMissingTools,
}: {
  orgId: string
  projectId: string
  sources: BasicMachineSource[]
  onSourcesChange: (sources: BasicMachineSource[]) => void
  onUnavailableIdsChange: (ids: string[]) => void
  showMissingToolsWarning: boolean
  onAddMissingTools: () => void
}) {
  function updateSource(id: string, patch: Partial<BasicMachineSource>) {
    onSourcesChange(sources.map((source) => (source.id === id ? { ...source, ...patch } : source)))
  }

  const [unavailableIds, setUnavailableIds] = useState<ReadonlySet<string>>(new Set())
  const reportAvailability = (id: string, unavailable: boolean) => {
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
  }
  useEffect(() => {
    onUnavailableIdsChange(
      sources.flatMap((source) => (unavailableIds.has(source.id) ? [source.id] : [])),
    )
  }, [onUnavailableIdsChange, sources, unavailableIds])

  return (
    <Field className="gap-5">
      <div className="flex items-center justify-between gap-3">
        <FieldLabel>Machine sources</FieldLabel>
        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                size="icon"
                variant="outline"
                className="bg-muted/40 size-8"
                aria-label="Add source"
              >
                <PlusIcon />
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
                BYO machine
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
      {showMissingToolsWarning && (
        <div
          role="alert"
          className="flex items-center justify-between gap-3 rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2"
        >
          <p className="text-sm">Some machine tools are missing.</p>
          <Button type="button" size="sm" variant="outline" onClick={onAddMissingTools}>
            Add missing tools
          </Button>
        </div>
      )}
      {sources.length > 0 && (
        <div className="space-y-3">
          {sources.map((source) => (
            <div
              key={source.id}
              className="border-border bg-muted/40 space-y-4 rounded-lg border p-4"
            >
              <div className="grid gap-4 sm:grid-cols-[1fr_auto]">
                <Field>
                  <RequiredFieldLabel htmlFor={`${source.id}-source`}>
                    {source.kind === 'pool' ? 'Machine pool' : 'BYO machine'}
                  </RequiredFieldLabel>
                  {source.kind === 'pool' ? (
                    <PoolSourceCombobox
                      id={`${source.id}-source`}
                      required
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
                          machineMemoryGb: '',
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
                      id={`${source.id}-source`}
                      required
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
                        const used = new Set(
                          sources.flatMap((candidate) =>
                            candidate.kind === 'machine' ? [candidate.name] : [],
                          ),
                        )
                        const [first, ...rest] = [...new Set(names)].filter(
                          (name) => !used.has(name),
                        )
                        if (first === undefined) return
                        if (source.name === '') {
                          onSourcesChange([
                            ...sources.map((candidate) =>
                              candidate.id === source.id
                                ? { ...candidate, name: first }
                                : candidate,
                            ),
                            ...rest.map((name) => ({ ...newMachineSource('machine'), name })),
                          ])
                          return
                        }
                        onSourcesChange([
                          ...sources,
                          ...[first, ...rest].map((name) => ({
                            ...newMachineSource('machine'),
                            name,
                          })),
                        ])
                      }}
                    />
                  )}
                  <ResourceNameFieldError
                    value={source.name}
                    fieldLabel={source.kind === 'pool' ? 'Machine pool name' : 'Machine name'}
                  />
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
                        min={0}
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
                        min={0}
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
          ))}
        </div>
      )}
    </Field>
  )
}
