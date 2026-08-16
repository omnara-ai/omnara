import { type MachinePool, type VisibleMachine } from '@omnara/sdk'
import { Trash2Icon } from 'lucide-react'

import {
  type BasicMachineSource,
  type MachineSourceKind,
  newMachineSource,
} from '@/components/agents/agentConfigBasicSerialization'
import {
  MachineSourceCombobox,
  PoolSourceCombobox,
} from '@/components/agents/AgentConfigMachineSourceComboboxes'
import { SourceOverridesSection } from '@/components/agents/AgentConfigMachineSourceOverrides'
import { emptyProviderOptions } from '@/components/machines/machineOverrides'
import { GrantMachineButton } from '@/components/projects/GrantMachineButton'
import { GrantMachinePoolButton } from '@/components/projects/GrantMachinePoolButton'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'

export function AgentConfigMachineSourcesField({
  orgId,
  projectId,
  sources,
  onSourcesChange,
}: {
  orgId: string
  projectId: string
  sources: BasicMachineSource[]
  onSourcesChange: (sources: BasicMachineSource[]) => void
}) {
  function updateSource(id: string, patch: Partial<BasicMachineSource>) {
    onSourcesChange(sources.map((source) => (source.id === id ? { ...source, ...patch } : source)))
  }

  function hasSource(kind: MachineSourceKind, name: string) {
    return sources.some((source) => source.kind === kind && source.name === name)
  }

  function addGrantedPool(pool: MachinePool) {
    if (hasSource('pool', pool.name)) return
    onSourcesChange([
      ...sources,
      {
        ...newMachineSource('pool'),
        name: pool.name,
        provider: pool.provider,
        managementKind: pool.management_kind,
      },
    ])
  }

  function addGrantedMachines(machines: VisibleMachine[]) {
    const existingNames = new Set(
      sources.flatMap((source) => (source.kind === 'machine' ? [source.name] : [])),
    )
    const added: BasicMachineSource[] = []
    for (const machine of machines) {
      if (existingNames.has(machine.display_name)) continue
      existingNames.add(machine.display_name)
      added.push({ ...newMachineSource('machine'), name: machine.display_name })
    }
    if (added.length > 0) onSourcesChange([...sources, ...added])
  }

  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>Machine sources</FieldLabel>
          <FieldDescription>Machines the agent can run commands on.</FieldDescription>
        </div>
        <div className="flex items-center gap-2">
          <GrantMachinePoolButton onGranted={addGrantedPool} />
          <GrantMachineButton onGranted={addGrantedMachines} />
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
                      value={
                        source.name === ''
                          ? null
                          : {
                              name: source.name,
                              provider: source.provider,
                              managementKind: source.managementKind,
                            }
                      }
                      onChange={(selection) => {
                        const name = selection?.name ?? ''
                        if (name === source.name) return
                        updateSource(source.id, {
                          name,
                          provider: selection?.provider ?? '',
                          managementKind: selection?.managementKind ?? '',
                          machineCpu: '',
                          machineMemoryGb: '',
                          providerOptions: emptyProviderOptions,
                          envRows: [],
                          secretEnvRows: [],
                        })
                      }}
                    />
                  ) : (
                    <MachineSourceCombobox
                      orgId={orgId}
                      projectId={projectId}
                      value={source.name === '' ? null : { name: source.name }}
                      onChange={(selection) => {
                        updateSource(source.id, { name: selection?.name ?? '' })
                      }}
                    />
                  )}
                  <ResourceNameFieldError
                    value={source.name}
                    fieldLabel={source.kind === 'pool' ? 'Machine pool name' : 'Machine name'}
                  />
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
