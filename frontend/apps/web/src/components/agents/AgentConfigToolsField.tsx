import type { ToolCatalog, ToolCatalogEntry, ToolPermissionSelection } from '@omnara/sdk'
import { ChevronDownIcon, Trash2Icon } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { builtInToolsets } from './builtInTools'

export interface BasicTool {
  name: string
  permission: ToolPermissionSelection | null
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
  const catalogTools = (catalog?.built_in_tools ?? []).filter((entry) => entry.name !== 'skill')
  const catalogByName = new Map(catalogTools.map((entry) => [entry.name, entry]))
  const availableTools = catalogTools.filter((entry) =>
    tools.every((tool) => tool.name !== entry.name),
  )
  const selectedToolNames = new Set(tools.map((tool) => tool.name))
  const hasAvailableToolsets = builtInToolsets.some(
    (toolset) =>
      toolset.tools.every((toolName) => catalogByName.has(toolName)) &&
      toolset.tools.some((toolName) => !selectedToolNames.has(toolName)),
  )

  function addToolset(toolNames: readonly string[]) {
    const additions: BasicTool[] = []
    for (const name of toolNames) {
      if (selectedToolNames.has(name)) {
        continue
      }
      const entry = catalogByName.get(name)
      if (entry == null) {
        return
      }
      additions.push({
        name,
        permission: structuredClone(entry.default_permission),
      })
    }
    onToolsChange([...tools, ...additions])
  }

  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>Tools</FieldLabel>
          <FieldDescription>Built-in tools the agent can call.</FieldDescription>
        </div>
        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button type="button" size="sm" variant="outline" disabled={!hasAvailableToolsets}>
                Toolsets
                <ChevronDownIcon />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {builtInToolsets.map((toolset) => {
                const isAvailable = toolset.tools.every((toolName) => catalogByName.has(toolName))
                const isComplete = toolset.tools.every((toolName) =>
                  selectedToolNames.has(toolName),
                )
                return (
                  <DropdownMenuItem
                    key={toolset.name}
                    disabled={!isAvailable || isComplete}
                    onSelect={() => {
                      addToolset(toolset.tools)
                    }}
                  >
                    {toolset.name}
                  </DropdownMenuItem>
                )
              })}
            </DropdownMenuContent>
          </DropdownMenu>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={availableTools.length === 0}
              >
                Add tools
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
        </div>
      </div>
      <div className="space-y-2">
        {tools.length === 0 ? (
          <div className="border-border bg-background/60 text-muted-foreground flex min-h-16 items-center justify-center rounded-md border border-dashed px-4 text-sm">
            No tools
          </div>
        ) : (
          tools.map((tool) => {
            const entry = catalogByName.get(tool.name)
            return (
              <div
                key={tool.name}
                className="border-border bg-background flex items-center gap-3 rounded-md border px-3 py-2"
              >
                <span className="min-w-0 flex-1 truncate font-mono text-sm">{tool.name}</span>
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
          })
        )}
      </div>
    </Field>
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
      <SelectTrigger size="sm" className="w-36">
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
