import type { McpServerTool, ToolPermissionProfile } from '@omnara/sdk'
import { useState } from 'react'

import {
  mcpToolAvailability,
  mcpToolAvailabilityOptions,
  mcpToolAvailabilityPatch,
  parseMcpToolAvailability,
} from '@/components/agents/mcpAvailability'
import { PermissionModeGroup } from '@/components/agents/PermissionModeGroup'
import {
  inheritPermissionOption,
  permissionModeOptions,
} from '@/components/agents/permissionModeOptions'
import type { BasicMcpTool } from '@/components/agents/useAgentBuilderForm'
import { Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

const inheritValue = 'inherit'

export function AgentConfigMcpToolOverrideList({
  tools,
  discovered,
  permissionProfile,
  onToolsChange,
}: {
  tools: BasicMcpTool[]
  discovered: McpServerTool[]
  permissionProfile?: ToolPermissionProfile
  onToolsChange: (tools: BasicMcpTool[]) => void
}) {
  const [openDescription, setOpenDescription] = useState<string | null>(null)
  const discoveredByName = new Map(discovered.map((tool) => [tool.name, tool]))
  return (
    <div className="overflow-hidden rounded-xl border">
      <div className="after:border-border/50 after:bg-muted/50 after:shadow-xs relative isolate after:pointer-events-none after:absolute after:-inset-x-px after:-top-px after:-z-10 after:h-[calc(2.25rem+2px)] after:rounded-xl after:border">
        <Table className="table-fixed">
          <TableHeader className="bg-transparent [&_tr]:border-0">
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-[calc(2.25rem+3px)] px-4 pb-[3px]">Tool</TableHead>
              <TableHead className="h-[calc(2.25rem+3px)] w-36 px-4 pb-[3px]">Visibility</TableHead>
              <TableHead className="h-[calc(2.25rem+3px)] w-36 px-4 pb-[3px]">Permission</TableHead>
              <TableHead className="h-[calc(2.25rem+3px)] w-14 px-2 pb-[3px]">
                <span className="sr-only">Remove</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tools.length === 0 ? (
              <TableRow>
                <TableCell
                  colSpan={4}
                  className="text-muted-foreground whitespace-normal px-4 py-3 text-center"
                >
                  No tool overrides
                </TableCell>
              </TableRow>
            ) : (
              tools.map((tool) => (
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
              ))
            )}
          </TableBody>
        </Table>
      </div>
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
    <TableRow className="hover:bg-transparent">
      <TableCell
        className="px-4"
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
      </TableCell>
      <TableCell>
        <PermissionModeGroup
          label={`${tool.name} visibility`}
          options={mcpToolAvailabilityOptions}
          value={mcpToolAvailability(tool)}
          onChange={(value) => {
            const availability = parseMcpToolAvailability(value)
            if (availability != null) onChange(mcpToolAvailabilityPatch(availability))
          }}
        />
      </TableCell>
      <TableCell>
        <PermissionModeGroup
          label={`${tool.name} permission`}
          options={[
            inheritPermissionOption,
            ...permissionModeOptions(permissionProfile?.permission_modes),
          ]}
          value={tool.permission?.mode ?? inheritValue}
          disabled={permissionProfile == null}
          onChange={(mode) => {
            onChange({ permission: mode === inheritValue ? null : { mode, parameters: {} } })
          }}
        />
      </TableCell>
      <TableCell className="px-2">
        <Button
          type="button"
          size="icon"
          variant="ghost"
          aria-label={`Remove ${tool.name} override`}
          onClick={onRemove}
        >
          <Trash2Icon />
        </Button>
      </TableCell>
    </TableRow>
  )
}
