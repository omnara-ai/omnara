import type { McpServerTool, ToolPermissionProfile } from '@omnara/sdk'
import { useState } from 'react'

import {
  type McpAvailability,
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
  serverAvailability,
  onToolsChange,
}: {
  tools: BasicMcpTool[]
  discovered: McpServerTool[]
  permissionProfile?: ToolPermissionProfile
  serverAvailability: McpAvailability
  onToolsChange: (tools: BasicMcpTool[]) => void
}) {
  const [openDescription, setOpenDescription] = useState<string | null>(null)
  const discoveredByName = new Map(discovered.map((tool) => [tool.name, tool]))
  return (
    <div className="overflow-hidden rounded-xl border">
      <div className="after:border-border/50 after:bg-muted/50 after:shadow-xs relative isolate after:pointer-events-none after:absolute after:-inset-x-px after:-top-px after:-z-10 after:hidden after:h-[calc(2.25rem+2px)] after:rounded-xl after:border sm:after:block">
        <Table className="block sm:table sm:table-fixed">
          <TableHeader className="hidden bg-transparent sm:table-header-group [&_tr]:border-0">
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-[calc(2.25rem+3px)] px-4 pb-[3px]">Tool</TableHead>
              <TableHead className="h-[calc(2.25rem+3px)] w-36 px-4 pb-[3px]">Visibility</TableHead>
              <TableHead className="h-[calc(2.25rem+3px)] w-32 px-4 pb-[3px]">Permission</TableHead>
              <TableHead className="h-[calc(2.25rem+3px)] w-14 px-2 pb-[3px]">
                <span className="sr-only">Remove</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody className="block sm:table-row-group">
            {tools.length === 0 ? (
              <TableRow className="block sm:table-row">
                <TableCell
                  colSpan={4}
                  className="text-muted-foreground block whitespace-normal px-4 py-3 text-center sm:table-cell"
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
                  serverAvailability={serverAvailability}
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
  serverAvailability,
  descriptionOpen,
  onDescriptionOpenChange,
  onChange,
  onRemove,
}: {
  tool: BasicMcpTool
  description: string | undefined
  permissionProfile?: ToolPermissionProfile
  serverAvailability: McpAvailability
  descriptionOpen: boolean
  onDescriptionOpenChange: (open: boolean) => void
  onChange: (patch: Partial<Omit<BasicMcpTool, 'name'>>) => void
  onRemove: () => void
}) {
  return (
    <TableRow className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 p-3 hover:bg-transparent sm:table-row sm:p-0">
      <TableCell
        className="col-span-2 block min-w-0 p-0 sm:table-cell sm:px-4 sm:py-2"
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
      <TableCell className="block p-0 sm:table-cell sm:p-2">
        <PermissionModeGroup
          label={`${tool.name} visibility`}
          options={mcpToolAvailabilityOptions}
          value={mcpToolAvailability(tool, serverAvailability)}
          onChange={(value) => {
            const availability = parseMcpToolAvailability(value)
            if (availability != null) onChange(mcpToolAvailabilityPatch(availability))
          }}
        />
      </TableCell>
      <TableCell className="col-span-2 block p-0 sm:table-cell sm:p-2">
        <PermissionModeGroup
          label={`${tool.name} permission`}
          options={[
            inheritPermissionOption,
            ...permissionModeOptions(permissionProfile?.permission_modes, tool.permission?.mode),
          ]}
          value={tool.permission?.mode ?? inheritValue}
          disabled={permissionProfile == null}
          onChange={(mode) => {
            onChange({ permission: mode === inheritValue ? null : { mode, parameters: {} } })
          }}
        />
      </TableCell>
      <TableCell className="col-start-3 row-start-1 block p-0 sm:table-cell sm:px-2 sm:py-2">
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="size-10 sm:size-8"
          aria-label={`Remove ${tool.name} override`}
          onClick={onRemove}
        >
          <Trash2Icon />
        </Button>
      </TableCell>
    </TableRow>
  )
}
