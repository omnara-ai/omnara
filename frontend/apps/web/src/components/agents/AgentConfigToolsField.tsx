import type { ToolCatalog, ToolCatalogEntry } from '@omnara/sdk'
import { useState } from 'react'

import {
  type PermissionSelection,
  permissionSelection,
} from '@/components/agents/agentConfigBasicExtract'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { PermissionModeGroup } from '@/components/agents/PermissionModeGroup'
import {
  disabledPermissionOption,
  permissionModeOptions,
} from '@/components/agents/permissionModeOptions'
import { ChevronDownIcon, PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

export interface BasicTool {
  name: string
  enabled?: boolean
  permission: PermissionSelection | null
  deferred?: boolean
}

const toolDescriptions = new Map([
  ['run_command', 'Run shell commands on an attached machine.'],
  ['write_process', 'Send input to a command that is still running.'],
  ['stop_process', 'Stop a command that is still running.'],
  ['read_process', 'Read output from a command, including after it finishes.'],
  ['list_processes', 'List commands and processes that are currently running.'],
  ['create_machine', 'Create another machine for the agent to use.'],
  ['delete_machine', 'Delete a machine created for the agent.'],
  ['list_machines', 'List the machines available to the agent.'],
  ['inspect_machine', 'View details about a machine available to the agent.'],
  ['read_file', "Read a text file in Omnara's virtual filesystem."],
  ['search_files', "Search text inside files in Omnara's virtual filesystem."],
  ['upload_file', "Copy a file into Omnara's virtual filesystem."],
  ['download_file', "Copy a file from Omnara's virtual filesystem to a machine."],
  ['ask_question', 'Ask the user a question and wait for their response.'],
  ['web_search', 'Search the public web for current information.'],
  ['web_fetch', 'Read the contents of a public webpage.'],
])

export function AgentConfigToolsField({
  catalog,
  tools,
  resolvedTools,
  onToolsChange,
}: {
  catalog?: ToolCatalog
  tools: BasicTool[]
  resolvedTools?: { name: string; enabled: boolean }[]
  onToolsChange: (tools: BasicTool[]) => void
}) {
  const catalogTools = catalog?.built_in_tools ?? []
  const catalogByName = new Map(catalogTools.map((entry) => [entry.name, entry]))
  const displayedTools: BasicTool[] = [
    ...tools,
    ...(resolvedTools ?? [])
      .filter((tool) => !tools.some((configured) => configured.name === tool.name))
      .map((tool) => ({ name: tool.name, enabled: tool.enabled, permission: null })),
  ]
  const includedTools = displayedTools
    .filter((tool) => catalogByName.get(tool.name)?.implicit)
    .sort((a, b) => a.name.localeCompare(b.name))
  const visibleTools = catalog
    ? tools.filter(
        (tool) => !catalogByName.get(tool.name)?.implicit && tool.name !== 'set_integration_target',
      )
    : []
  const availableTools = catalogTools.filter(
    (entry) =>
      !entry.implicit &&
      entry.name !== 'set_integration_target' &&
      tools.every((tool) => tool.name !== entry.name),
  )

  return (
    <AgentConfigSectionCard
      title="Tools"
      action={
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="text-muted-foreground size-10 sm:size-8"
              disabled={availableTools.length === 0}
              aria-label="Add tools"
            >
              <PlusIcon />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="max-h-72 overflow-y-auto">
            {availableTools.map((entry) => (
              <DropdownMenuItem
                key={entry.name}
                onSelect={() => {
                  onToolsChange([
                    ...tools,
                    {
                      name: entry.name,
                      permission: permissionSelection(entry.default_permission),
                    },
                  ])
                }}
              >
                {entry.name}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      }
    >
      {visibleTools.length > 0 ? (
        <div>
          {visibleTools.map((tool) => {
            const entry = catalogByName.get(tool.name)
            return (
              <div
                key={tool.name}
                className="flex flex-wrap items-center gap-2 px-4 py-2.5 sm:flex-nowrap sm:gap-3 sm:px-5"
              >
                <ToolName name={tool.name} entry={entry} />
                <PermissionModeSelect
                  toolName={tool.name}
                  entry={entry}
                  allowDisable={tool.enabled === false}
                  value={
                    tool.enabled === false
                      ? 'disabled'
                      : (tool.permission?.mode ?? entry?.default_permission.mode ?? '')
                  }
                  onChange={(mode) => {
                    onToolsChange(
                      tools.map((currentTool) =>
                        currentTool.name === tool.name
                          ? {
                              ...currentTool,
                              enabled: mode === 'disabled' ? false : undefined,
                              permission:
                                mode === 'disabled'
                                  ? currentTool.permission
                                  : { mode, parameters: {} },
                            }
                          : currentTool,
                      ),
                    )
                  }}
                />
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  aria-label={`Remove ${tool.name}`}
                  onClick={() => {
                    onToolsChange(tools.filter((currentTool) => currentTool.name !== tool.name))
                  }}
                >
                  <Trash2Icon />
                </Button>
              </div>
            )
          })}
        </div>
      ) : null}
      <AgentConfigIncludedTools
        catalog={catalog}
        tools={includedTools}
        onToolChange={(updated) => {
          onToolsChange(
            tools.some((tool) => tool.name === updated.name)
              ? tools.map((tool) => (tool.name === updated.name ? updated : tool))
              : [...tools, updated],
          )
        }}
      />
    </AgentConfigSectionCard>
  )
}

function AgentConfigIncludedTools({
  catalog,
  tools,
  onToolChange,
}: {
  catalog?: ToolCatalog
  tools: BasicTool[]
  onToolChange: (tool: BasicTool) => void
}) {
  if (tools.length === 0) return null
  const catalogByName = new Map(catalog?.built_in_tools.map((entry) => [entry.name, entry]))

  return (
    <Collapsible>
      <Tooltip>
        <CollapsibleTrigger asChild>
          <TooltipTrigger className="text-muted-foreground group flex w-fit items-center gap-2 px-4 py-3 text-left text-sm sm:px-5">
            <ChevronDownIcon className="size-4 transition-transform group-data-[state=open]:rotate-180" />
            Built-in tools
          </TooltipTrigger>
        </CollapsibleTrigger>
        <TooltipContent
          side="right"
          className="max-w-xs text-wrap px-4 py-2 text-left text-sm leading-relaxed"
        >
          Tools added automatically based on the agent&apos;s configuration.
        </TooltipContent>
      </Tooltip>
      <CollapsibleContent className="collapsible-animate-height">
        {tools.map((tool) => {
          const { name } = tool
          const entry = catalogByName.get(name)
          return (
            <div key={name} className="flex flex-wrap items-center gap-2 px-4 py-2 sm:px-5">
              <ToolName name={name} entry={entry} />
              <PermissionModeSelect
                toolName={name}
                entry={entry}
                allowDisable
                value={
                  tool.enabled === false
                    ? 'disabled'
                    : (tool.permission?.mode ?? entry?.default_permission.mode ?? '')
                }
                onChange={(mode) => {
                  onToolChange({
                    ...tool,
                    enabled: mode === 'disabled' ? false : undefined,
                    permission: mode === 'disabled' ? tool.permission : { mode, parameters: {} },
                  })
                }}
              />
            </div>
          )
        })}
      </CollapsibleContent>
    </Collapsible>
  )
}

function PermissionModeSelect({
  toolName,
  entry,
  value,
  allowDisable = false,
  onChange,
}: {
  toolName: string
  entry?: ToolCatalogEntry
  value: string
  allowDisable?: boolean
  onChange: (mode: string) => void
}) {
  const options = permissionModeOptions(entry?.permission_modes)
  return (
    <PermissionModeGroup
      label={`${toolName} permission`}
      options={allowDisable ? [...options, disabledPermissionOption] : options}
      value={value}
      disabled={entry == null || (!allowDisable && options.length === 1)}
      onChange={onChange}
    />
  )
}

function ToolName({ name, entry }: { name: string; entry?: ToolCatalogEntry }) {
  const [open, setOpen] = useState(false)
  const description = toolDescriptions.get(name) ?? entry?.description
  return (
    <div
      className="-my-2.5 flex min-w-0 flex-1 basis-full items-center self-stretch py-2.5 sm:basis-auto"
      onPointerEnter={() => {
        setOpen(true)
      }}
      onPointerLeave={() => {
        setOpen(false)
      }}
    >
      {description ? (
        <Tooltip open={open}>
          <TooltipTrigger asChild>
            <button
              type="button"
              className="bg-muted block max-w-full cursor-default truncate rounded-md px-2 py-1 text-left font-mono text-xs outline-none focus-visible:ring-2"
              aria-label={`About ${name}`}
              onFocus={() => {
                setOpen(true)
              }}
              onBlur={() => {
                setOpen(false)
              }}
            >
              {name}
            </button>
          </TooltipTrigger>
          <TooltipContent side="right" className="max-w-sm px-4 py-2 text-sm leading-relaxed">
            {description}
          </TooltipContent>
        </Tooltip>
      ) : (
        <span className="bg-muted truncate rounded-md px-2 py-1 font-mono text-xs">{name}</span>
      )}
    </div>
  )
}
