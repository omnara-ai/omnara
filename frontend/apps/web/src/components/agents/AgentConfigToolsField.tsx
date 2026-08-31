import type { ToolCatalog, ToolCatalogEntry, ToolPermissionSelection } from '@omnara/sdk'
import { useState } from 'react'

import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
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
  permission: ToolPermissionSelection | null
}

const hiddenToolNames = new Set(['skill', 'send_integration_message', 'set_integration_target'])
const toolDescriptions: Record<string, string> = {
  run_command: 'Run shell commands on an attached machine.',
  write_process: 'Send input to a command that is still running.',
  stop_process: 'Stop a command that is still running.',
  read_process: 'Read output from a command, including after it finishes.',
  list_processes: 'List commands and processes that are currently running.',
  create_machine: 'Create another machine for the agent to use.',
  delete_machine: 'Delete a machine created for the agent.',
  list_machines: 'List the machines available to the agent.',
  inspect_machine: 'View details about a machine available to the agent.',
  upload_artifact: 'Create an artifact from a regular file on an attached machine.',
  download_artifact: 'Copy an artifact to an attached machine.',
  ask_question: 'Ask the user a question and wait for their response.',
  web_search: 'Search the public web for current information.',
  web_fetch: 'Read the contents of a public webpage.',
}

export function AgentConfigToolsField({
  catalog,
  tools,
  onToolsChange,
}: {
  catalog?: ToolCatalog
  tools: BasicTool[]
  onToolsChange: (tools: BasicTool[]) => void
}) {
  const catalogTools = (catalog?.built_in_tools ?? []).filter(
    (entry) => !hiddenToolNames.has(entry.name),
  )
  const visibleTools = tools.filter((tool) => !hiddenToolNames.has(tool.name))
  const catalogByName = new Map(catalogTools.map((entry) => [entry.name, entry]))
  const availableTools = catalogTools.filter((entry) =>
    tools.every((tool) => tool.name !== entry.name),
  )
  const [openDescription, setOpenDescription] = useState<string | null>(null)

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
                      permission: structuredClone(entry.default_permission),
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
            const description = toolDescriptions[tool.name]
            return (
              <div
                key={tool.name}
                className="flex flex-wrap items-center gap-2 px-4 py-2.5 sm:flex-nowrap sm:gap-3 sm:px-5"
              >
                <div
                  className="-my-2.5 flex min-w-0 flex-1 basis-full items-center self-stretch py-2.5 sm:basis-auto"
                  onPointerEnter={() => {
                    setOpenDescription(tool.name)
                  }}
                  onPointerLeave={() => {
                    setOpenDescription(null)
                  }}
                >
                  {description ? (
                    <Tooltip open={openDescription === tool.name}>
                      <TooltipTrigger asChild>
                        <button
                          type="button"
                          className="bg-muted block max-w-full cursor-default truncate rounded-md px-2 py-1 text-left font-mono text-xs outline-none focus-visible:ring-2"
                          aria-label={`About ${tool.name}`}
                          onFocus={() => {
                            setOpenDescription(tool.name)
                          }}
                          onBlur={() => {
                            setOpenDescription(null)
                          }}
                        >
                          {tool.name}
                        </button>
                      </TooltipTrigger>
                      <TooltipContent
                        side="right"
                        className="max-w-sm px-4 py-2 text-sm leading-relaxed"
                      >
                        {description}
                      </TooltipContent>
                    </Tooltip>
                  ) : (
                    <span className="bg-muted truncate rounded-md px-2 py-1 font-mono text-xs">
                      {tool.name}
                    </span>
                  )}
                </div>
                <PermissionModeSelect
                  entry={entry}
                  value={tool.permission?.mode ?? entry?.default_permission.mode ?? ''}
                  onChange={(mode) => {
                    onToolsChange(
                      tools.map((currentTool) =>
                        currentTool.name === tool.name
                          ? { ...currentTool, permission: { mode, parameters: {} } }
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
    </AgentConfigSectionCard>
  )
}

function PermissionModeSelect({
  entry,
  value,
  onChange,
}: {
  entry?: ToolCatalogEntry
  value: string
  onChange: (mode: string) => void
}) {
  return (
    <Select
      value={value}
      onValueChange={onChange}
      disabled={entry == null || entry.permission_modes.length === 1}
    >
      <SelectTrigger size="sm" className="min-w-0 flex-1 sm:w-36 sm:flex-none">
        <SelectValue>{permissionModeLabel(entry, value)}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        {entry?.permission_modes.map((mode) => (
          <SelectItem key={mode.name} value={mode.name}>
            {mode.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function permissionModeLabel(entry: ToolCatalogEntry | undefined, value: string) {
  return entry?.permission_modes.find((mode) => mode.name === value)?.label ?? value
}
