import type { ToolCatalog, ToolCatalogEntry, ToolPermissionSelection } from '@omnara/sdk'
import { InfoIcon, PlusIcon, Trash2Icon } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Field, FieldLabel } from '@/components/ui/field'
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

  return (
    <Field className="gap-5">
      <div className="flex items-center justify-between gap-3">
        <FieldLabel>Tools</FieldLabel>
        <div className="flex items-center gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                size="icon"
                variant="outline"
                className="bg-muted/40 size-8"
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
        </div>
      </div>
      {visibleTools.length > 0 && (
        <div className="border-border bg-muted/40 divide-y overflow-hidden rounded-xl border">
          {visibleTools.map((tool) => {
            const entry = catalogByName.get(tool.name)
            return (
              <div key={tool.name} className="flex items-center gap-3 px-3 py-2">
                <div className="flex min-w-0 flex-1 items-center gap-1">
                  <span className="truncate font-mono text-sm">{tool.name}</span>
                  {entry?.description && (
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          type="button"
                          size="icon"
                          variant="ghost"
                          className="text-muted-foreground size-7 shrink-0"
                          aria-label={`About ${tool.name}`}
                        >
                          <InfoIcon className="size-4" />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent side="top" className="max-w-sm">
                        {entry.description}
                      </TooltipContent>
                    </Tooltip>
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
      )}
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
