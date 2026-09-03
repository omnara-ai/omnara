import type { McpServerTool, ToolPermissionProfile } from '@omnara/sdk'
import { useState } from 'react'

import type { BasicMcpTool } from '@/components/agents/useAgentBuilderForm'
import { Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

const inheritValue = 'inherit'

export function AgentConfigMcpToolOverrideList({
  tools,
  discovered,
  permissionProfile,
  toolCountLabel,
  onToolsChange,
}: {
  tools: BasicMcpTool[]
  discovered: McpServerTool[]
  permissionProfile?: ToolPermissionProfile
  toolCountLabel: string | null
  onToolsChange: (tools: BasicMcpTool[]) => void
}) {
  const [openDescription, setOpenDescription] = useState<string | null>(null)
  if (tools.length === 0) {
    return (
      <p className="text-muted-foreground px-4 py-3 text-sm">
        {toolCountLabel == null
          ? 'No overrides.'
          : `No overrides — all ${toolCountLabel} follow the settings above.`}
      </p>
    )
  }
  const discoveredByName = new Map(discovered.map((tool) => [tool.name, tool]))
  return (
    <div className="divide-y">
      {tools.map((tool) => (
        <ToolOverrideRow
          key={tool.name}
          tool={tool}
          description={discoveredByName.get(tool.name)?.description}
          permissionProfile={permissionProfile}
          descriptionOpen={openDescription === tool.name}
          onDescriptionOpenChange={(open) => {
            setOpenDescription(open ? tool.name : null)
          }}
          onChange={(patch) => {
            onToolsChange(
              tools.map((candidate) =>
                candidate.name === tool.name ? { ...candidate, ...patch } : candidate,
              ),
            )
          }}
          onRemove={() => {
            onToolsChange(tools.filter((candidate) => candidate.name !== tool.name))
          }}
        />
      ))}
    </div>
  )
}

function ToolOverrideRow({
  tool,
  description,
  permissionProfile,
  descriptionOpen,
  onDescriptionOpenChange,
  onChange,
  onRemove,
}: {
  tool: BasicMcpTool
  description: string | undefined
  permissionProfile?: ToolPermissionProfile
  descriptionOpen: boolean
  onDescriptionOpenChange: (open: boolean) => void
  onChange: (patch: Partial<Omit<BasicMcpTool, 'name'>>) => void
  onRemove: () => void
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 px-3 py-2 sm:flex-nowrap sm:gap-3">
      <div
        className="-my-2 flex min-w-0 flex-1 basis-full items-center self-stretch py-2 sm:basis-auto"
        onPointerEnter={() => {
          onDescriptionOpenChange(true)
        }}
        onPointerLeave={() => {
          onDescriptionOpenChange(false)
        }}
      >
        {description ? (
          <Tooltip open={descriptionOpen}>
            <TooltipTrigger asChild>
              <button
                type="button"
                className="bg-muted block max-w-full cursor-default truncate rounded-md px-2 py-1 text-left font-mono text-xs outline-none focus-visible:ring-2"
                aria-label={`About ${tool.name}`}
                onFocus={() => {
                  onDescriptionOpenChange(true)
                }}
                onBlur={() => {
                  onDescriptionOpenChange(false)
                }}
              >
                {tool.name}
              </button>
            </TooltipTrigger>
            <TooltipContent side="right" className="max-w-sm px-4 py-2 text-sm leading-relaxed">
              {description}
            </TooltipContent>
          </Tooltip>
        ) : (
          <span className="bg-muted truncate rounded-md px-2 py-1 font-mono text-xs">
            {tool.name}
          </span>
        )}
      </div>
      <Select
        value={tool.enabled == null ? inheritValue : String(tool.enabled)}
        onValueChange={(value) => {
          onChange({ enabled: value === inheritValue ? null : value === 'true' })
        }}
      >
        <SelectTrigger
          size="sm"
          className="min-w-0 flex-1 sm:w-36 sm:flex-none"
          aria-label={`${tool.name} enabled`}
        >
          <SelectValue>
            {tool.enabled == null ? 'Default' : tool.enabled ? 'Enabled' : 'Disabled'}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={inheritValue}>Default</SelectItem>
          <SelectItem value="true">Enabled</SelectItem>
          <SelectItem value="false">Disabled</SelectItem>
        </SelectContent>
      </Select>
      <Select
        value={tool.permission?.mode ?? inheritValue}
        disabled={permissionProfile == null}
        onValueChange={(mode) => {
          onChange({ permission: mode === inheritValue ? null : { mode, parameters: {} } })
        }}
      >
        <SelectTrigger
          size="sm"
          className="min-w-0 flex-1 sm:w-40 sm:flex-none"
          aria-label={`${tool.name} permission`}
        >
          <SelectValue>
            {tool.permission == null
              ? 'Default'
              : permissionModeLabel(permissionProfile, tool.permission.mode)}
          </SelectValue>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={inheritValue}>Default</SelectItem>
          {permissionProfile?.permission_modes.map((mode) => (
            <SelectItem key={mode.name} value={mode.name}>
              {mode.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={`Remove ${tool.name} override`}
        onClick={onRemove}
      >
        <Trash2Icon />
      </Button>
    </div>
  )
}

function permissionModeLabel(profile: ToolPermissionProfile | undefined, value: string) {
  return profile?.permission_modes.find((mode) => mode.name === value)?.label ?? value
}
