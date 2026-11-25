import type { ToolPermissionProfile } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import {
  type McpAvailability,
  mcpAvailabilityOptions,
  mcpToolAvailability,
  mcpToolAvailabilityPatch,
  parseMcpAvailability,
} from '@/components/agents/mcpAvailability'
import { PermissionModeGroup } from '@/components/agents/PermissionModeGroup'
import { permissionModeOptions } from '@/components/agents/permissionModeOptions'
import type { BasicMcpTool } from '@/components/agents/useAgentBuilderForm'
import { Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

export interface McpToolRow {
  name: string
  title?: string
  description?: string
  discovered: boolean
  tool?: BasicMcpTool
}

export type McpToolPatch = Partial<Omit<BasicMcpTool, 'name'>>

export function AgentConfigMcpToolList({
  rows,
  empty,
  showRemove,
  permissionProfile,
  defaultPermissionMode,
  serverAvailability,
  onToolChange,
  onToolRemove,
}: {
  rows: McpToolRow[]
  empty: ReactNode
  showRemove: boolean
  permissionProfile?: ToolPermissionProfile
  defaultPermissionMode: string
  serverAvailability: McpAvailability
  onToolChange: (name: string, patch: McpToolPatch) => void
  onToolRemove: (name: string) => void
}) {
  const [openDescription, setOpenDescription] = useState<string | null>(null)
  const columns = showRemove ? 4 : 3
  return (
    <div className="max-h-96 overflow-y-auto rounded-xl border">
      <table className="block w-full text-sm sm:table sm:table-fixed">
        <TableHeader className="bg-muted sticky top-0 z-10 hidden sm:table-header-group">
          <TableRow className="hover:bg-transparent">
            <TableHead className="px-4">Tool</TableHead>
            <TableHead className="w-36 px-4">Visibility</TableHead>
            <TableHead className="w-32 px-4">Permission</TableHead>
            {showRemove && (
              <TableHead className="w-14 px-2">
                <span className="sr-only">Remove</span>
              </TableHead>
            )}
          </TableRow>
        </TableHeader>
        <TableBody className="block sm:table-row-group">
          {rows.length === 0 ? (
            <TableRow className="block hover:bg-transparent sm:table-row">
              <TableCell
                colSpan={columns}
                className="text-muted-foreground block px-4 py-3 text-center whitespace-normal sm:table-cell"
              >
                {empty}
              </TableCell>
            </TableRow>
          ) : (
            rows.map((row) => (
              <ToolRow
                key={row.name}
                row={row}
                showRemove={showRemove}
                permissionProfile={permissionProfile}
                defaultPermissionMode={defaultPermissionMode}
                serverAvailability={serverAvailability}
                descriptionOpen={openDescription === row.name}
                onDescriptionOpenChange={(open) => {
                  setOpenDescription(open ? row.name : null)
                }}
                onChange={(patch) => {
                  onToolChange(row.name, patch)
                }}
                onRemove={() => {
                  onToolRemove(row.name)
                }}
              />
            ))
          )}
        </TableBody>
      </table>
    </div>
  )
}

function ToolRow({
  row,
  showRemove,
  permissionProfile,
  defaultPermissionMode,
  serverAvailability,
  descriptionOpen,
  onDescriptionOpenChange,
  onChange,
  onRemove,
}: {
  row: McpToolRow
  showRemove: boolean
  permissionProfile?: ToolPermissionProfile
  defaultPermissionMode: string
  serverAvailability: McpAvailability
  descriptionOpen: boolean
  onDescriptionOpenChange: (open: boolean) => void
  onChange: (patch: McpToolPatch) => void
  onRemove: () => void
}) {
  const { name, description, tool } = row
  const permissionMode = tool?.permission?.mode ?? defaultPermissionMode
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
                aria-label={`About ${name}`}
                onFocus={() => {
                  onDescriptionOpenChange(true)
                }}
                onBlur={() => {
                  onDescriptionOpenChange(false)
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
          <span className="bg-muted inline-block max-w-full truncate rounded-md px-2 py-1 align-middle font-mono text-xs">
            {name}
          </span>
        )}
      </TableCell>
      <TableCell className="block p-0 sm:table-cell sm:p-2">
        <PermissionModeGroup
          label={`${name} visibility`}
          options={mcpAvailabilityOptions}
          value={mcpToolAvailability(tool, serverAvailability)}
          onChange={(value) => {
            const availability = parseMcpAvailability(value)
            if (availability != null) {
              onChange(mcpToolAvailabilityPatch(availability, serverAvailability))
            }
          }}
        />
      </TableCell>
      <TableCell className="col-span-2 block p-0 sm:table-cell sm:p-2">
        <PermissionModeGroup
          label={`${name} permission`}
          options={permissionModeOptions(permissionProfile?.permission_modes, permissionMode)}
          value={permissionMode}
          disabled={permissionProfile == null}
          onChange={(mode) => {
            onChange({
              permission: mode === defaultPermissionMode ? null : { mode, parameters: {} },
            })
          }}
        />
      </TableCell>
      {showRemove && (
        <TableCell className="col-start-3 row-start-1 block p-0 sm:table-cell sm:px-2 sm:py-2">
          {!row.discovered && (
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="size-10 sm:size-8"
              aria-label={`Remove ${name}`}
              onClick={onRemove}
            >
              <Trash2Icon />
            </Button>
          )}
        </TableCell>
      )}
    </TableRow>
  )
}
