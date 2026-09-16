import type { ToolCatalog, ToolCatalogEntry } from '@omnara/sdk'
import { useState } from 'react'

import {
  type PermissionSelection,
  permissionSelection,
} from '@/components/agents/agentConfigBasicExtract'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { ChevronDownIcon, PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

export interface BasicTool {
  name: string
  enabled?: boolean
  permission: PermissionSelection | null
}

const toolDescriptions = new Map([
  ['ask_question', 'Ask the user a question and wait for their response.'],
  ['web_search', 'Search the public web for current information.'],
  ['web_fetch', 'Read the contents of a public webpage.'],
])

export function AgentConfigToolsField({
  catalog,
  tools,
  onToolsChange,
}: {
  catalog?: ToolCatalog
  tools: BasicTool[]
  onToolsChange: (tools: BasicTool[]) => void
}) {
  const catalogTools = catalog?.built_in_tools ?? []
  const catalogByName = new Map(catalogTools.map((entry) => [entry.name, entry]))
  const includedTools = tools.filter((tool) => catalogByName.get(tool.name)?.automatically_added)
  const visibleTools = catalog
    ? tools.filter(
        (tool) =>
          !catalogByName.get(tool.name)?.automatically_added &&
          tool.name !== 'set_integration_target',
      )
    : []
  const availableTools = catalogTools.filter(
    (entry) =>
      !entry.automatically_added &&
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
        <div className="divide-y">
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
          onToolsChange(tools.map((tool) => (tool.name === updated.name ? updated : tool)))
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
    <Collapsible className="border-t first:border-t-0">
      <CollapsibleTrigger className="text-muted-foreground group flex w-full items-center gap-2 px-4 py-3 text-left text-sm sm:px-5">
        <ChevronDownIcon className="size-4 transition-transform group-data-[state=open]:rotate-180" />
        Other tools
      </CollapsibleTrigger>
      <CollapsibleContent className="divide-y">
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
  return (
    <Select
      value={value}
      onValueChange={(mode) => {
        if (mode !== '') onChange(mode)
      }}
      disabled={entry == null || (!allowDisable && entry.permission_modes.length === 1)}
    >
      <SelectTrigger
        size="sm"
        className="min-w-0 flex-1 sm:w-36 sm:flex-none"
        aria-label={`${toolName} permission`}
      >
        <SelectValue>
          {allowDisable && value === 'disabled' ? 'Disabled' : permissionModeLabel(entry, value)}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {entry?.permission_modes.map((mode) => (
          <SelectItem key={mode.name} value={mode.name}>
            {mode.label}
          </SelectItem>
        ))}
        {allowDisable && <SelectItem value="disabled">Disabled</SelectItem>}
      </SelectContent>
    </Select>
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

function permissionModeLabel(entry: ToolCatalogEntry | undefined, value: string) {
  return entry?.permission_modes.find((mode) => mode.name === value)?.label ?? value
}
