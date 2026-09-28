import { type ReactNode, useEffect, useState } from 'react'

import {
  MachineSourceCombobox,
  PoolGrantSummary,
  PoolSourceCombobox,
  type ResolvedPoolGrant,
} from '@/components/agents/AgentConfigMachineSourceComboboxes'
import {
  SourceCapacityFields,
  SourceOverridesSection,
  SourceResourceFields,
} from '@/components/agents/AgentConfigMachineSourceOverrides'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { PillTabs } from '@/components/agents/PillTabs'
import { type BasicMachineSource, newMachineSource } from '@/components/agents/useAgentBuilderForm'
import { ChevronRightIcon, PlusIcon, Trash2Icon } from '@/components/icons'
import { KeyValueEditor } from '@/components/key-value/KeyValueEditor'
import { emptyProviderOptions } from '@/components/machines/machineOverrides'
import { Button } from '@/components/ui/button'
import { CollapseBody } from '@/components/ui/collapse-body'
import { Collapsible, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

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

  const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(new Set())
  function setExpanded(id: string, expanded: boolean) {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (expanded) {
        next.add(id)
      } else {
        next.delete(id)
      }
      return next
    })
  }
  function addSource() {
    const source = newMachineSource('pool')
    setExpanded(source.id, true)
    onSourcesChange([...sources, source])
  }

  const [resolvedGrants, setResolvedGrants] = useState<ReadonlyMap<string, ResolvedPoolGrant>>(
    new Map(),
  )
  const reportGrant = (id: string, grant: ResolvedPoolGrant | null) => {
    setResolvedGrants((prev) => {
      if (prev.get(id) === (grant ?? undefined)) return prev
      const next = new Map(prev)
      if (grant) {
        next.set(id, grant)
      } else {
        next.delete(id)
      }
      return next
    })
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
    <AgentConfigSectionCard
      title="Machine sources"
      action={
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-10 sm:size-8"
          aria-label="Add source"
          onClick={addSource}
        >
          <PlusIcon />
        </Button>
      }
    >
      {sources.length > 0 ? (
        <div className="flex flex-col gap-1 px-3 pb-3">
          {sources.map((source) => (
            <Collapsible
              key={source.id}
              open={expandedIds.has(source.id)}
              onOpenChange={(open) => {
                setExpanded(source.id, open)
              }}
              className="expanded-surface rounded-xl"
            >
              <SourceRowTooltip
                grant={resolvedGrants.get(source.id) ?? null}
                enabled={!expandedIds.has(source.id)}
              >
                <div className="flex items-center gap-2 py-2 pl-1 pr-2">
                  <CollapsibleTrigger asChild>
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      className="text-muted-foreground group size-10 sm:size-8"
                      aria-label="Toggle source details"
                    >
                      <ChevronRightIcon className="size-4 transition-transform group-data-[state=open]:rotate-90" />
                    </Button>
                  </CollapsibleTrigger>
                  <div>
                    <PillTabs
                      value={source.kind}
                      onValueChange={(kind) => {
                        setExpanded(source.id, true)
                        if (kind === source.kind) return
                        reportGrant(source.id, null)
                        onSourcesChange(
                          sources.map((candidate) =>
                            candidate.id === source.id
                              ? { ...newMachineSource(kind), id: source.id }
                              : candidate,
                          ),
                        )
                      }}
                      tabs={[
                        { value: 'pool', label: 'Sandboxes' },
                        { value: 'machine', label: 'BYO' },
                      ]}
                    />
                  </div>
                  <Field className="min-w-0 flex-1">
                    {source.kind === 'pool' ? (
                      <PoolSourceCombobox
                        id={`${source.id}-source`}
                        required
                        orgId={orgId}
                        projectId={projectId}
                        value={source.name}
                        onChange={(name, pool) => {
                          setExpanded(source.id, true)
                          if (name === source.name) return
                          updateSource(source.id, {
                            name,
                            provider: pool?.provider ?? '',
                            managementKind: pool?.management_kind ?? '',
                            machineCpu: '',
                            machineMemoryGb: '',
                            deleteAfterIdleMinutes: '',
                            providerOptions: emptyProviderOptions,
                            envRows: [],
                            secretEnvRows: [],
                          })
                        }}
                        onUnavailableChange={(unavailable) => {
                          reportAvailability(source.id, unavailable)
                        }}
                        onGrantResolved={(grant) => {
                          reportGrant(source.id, grant)
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
                          setExpanded(source.id, true)
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
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    className="text-muted-foreground size-10 sm:size-8"
                    aria-label="Remove machine source"
                    onClick={() => {
                      onSourcesChange(sources.filter((candidate) => candidate.id !== source.id))
                    }}
                  >
                    <Trash2Icon />
                  </Button>
                </div>
              </SourceRowTooltip>
              <CollapseBody open={expandedIds.has(source.id)}>
                <div className="flex flex-col gap-4 px-3 pb-5 pt-5 sm:pl-11">
                  <SourceResourceFields
                    source={source}
                    onChange={(patch) => {
                      updateSource(source.id, patch)
                    }}
                  />
                  <SourceCapacityFields
                    source={source}
                    onChange={(patch) => {
                      updateSource(source.id, patch)
                    }}
                  />
                  <Field>
                    <FieldLabel htmlFor={`${source.id}-cwd`}>Working directory</FieldLabel>
                    <Input
                      id={`${source.id}-cwd`}
                      value={source.defaultCwd}
                      placeholder="/workspace"
                      onChange={(event) => {
                        updateSource(source.id, { defaultCwd: event.target.value })
                      }}
                    />
                  </Field>
                  <KeyValueEditor
                    orgId={orgId}
                    projectId={projectId}
                    enabled
                    label="Environment variables"
                    itemLabel="Variable"
                    keyPlaceholder="NAME"
                    textRows={source.envRows}
                    secretRows={source.secretEnvRows}
                    onChange={({ textRows, secretRows }) => {
                      updateSource(source.id, { envRows: textRows, secretEnvRows: secretRows })
                    }}
                  />
                  <SourceOverridesSection
                    source={source}
                    onChange={(patch) => {
                      updateSource(source.id, patch)
                    }}
                  />
                </div>
              </CollapseBody>
            </Collapsible>
          ))}
        </div>
      ) : null}
    </AgentConfigSectionCard>
  )
}

function SourceRowTooltip({
  grant,
  enabled,
  children,
}: {
  grant: ResolvedPoolGrant | null
  enabled: boolean
  children: ReactNode
}) {
  const [hovered, setHovered] = useState(false)
  if (grant === null) return children
  return (
    <Tooltip open={hovered && enabled}>
      <TooltipTrigger asChild>
        <div
          onPointerEnter={() => {
            setHovered(true)
          }}
          onPointerLeave={() => {
            setHovered(false)
          }}
        >
          {children}
        </div>
      </TooltipTrigger>
      <TooltipContent side="bottom" className="px-3 py-2">
        <PoolGrantSummary {...grant} />
      </TooltipContent>
    </Tooltip>
  )
}
