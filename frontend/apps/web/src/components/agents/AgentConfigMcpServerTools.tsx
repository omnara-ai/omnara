import { useMcpServerTools } from '@omnara/react'
import {
  ApiError,
  type McpServerTool,
  type McpServerToolsRequest,
  schemas,
  type ToolPermissionProfile,
} from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import {
  mcpToolNameAddable,
  type UnexposableMcpTool,
  unexposableMcpTools,
} from '@/components/agents/agentConfigMcp'
import {
  AgentConfigMcpToolList,
  type McpToolPatch,
  type McpToolRow,
} from '@/components/agents/AgentConfigMcpToolList'
import { mcpServerAvailability } from '@/components/agents/mcpAvailability'
import { mcpServerToolsRequest } from '@/components/agents/mcpServerToolsRequest'
import {
  type BasicMcpServer,
  type BasicMcpTool,
  type McpAuthType,
} from '@/components/agents/useAgentBuilderForm'
import { SearchIcon, TriangleAlert } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { useDebouncedValue } from '@/hooks/use-resource-list'

export function AgentConfigMcpServerTools({
  orgId,
  projectId,
  server,
  permissionProfile,
  defaults,
  onToolsChange,
  onAuthTypeChange,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  permissionProfile?: ToolPermissionProfile
  defaults: ReactNode
  onToolsChange: (tools: BasicMcpTool[]) => void
  onAuthTypeChange: (authType: McpAuthType) => void
}) {
  const [query, setQuery] = useState('')
  const request = parseRequestKey(useDebouncedValue(JSON.stringify(mcpServerToolsRequest(server))))
  const discovery = useMcpServerTools(orgId, projectId, request, {
    onError: (error) => {
      const detected = detectedAuthType(error)
      if (server.authType === 'none' && detected != null) onAuthTypeChange(detected)
    },
  })
  const discovered = discovery.data?.tools ?? []
  const discovering = discovery.isPending && request != null
  const unexposableTools = unexposableMcpTools(
    server,
    discovered.map((tool) => tool.name),
  )
  const discoveryFailure = visibleDiscoveryFailure(discovery, server.authType)

  const rows = toolRows(server.tools, discovered)
  const visibleRows = rows.filter((row) => matchesToolSearch(row, query))
  const typedName = query.trim()
  const typedNameAddable =
    mcpToolNameAddable(typedName) && !rows.some((row) => row.name === typedName)
  const inputId = `${server.id}-tool-search`

  function addTool(name: string) {
    onToolsChange([...server.tools, { name, enabled: null, permission: null }])
    setQuery('')
  }

  function changeTool(name: string, patch: McpToolPatch) {
    const current = server.tools.find((tool) => tool.name === name)
    const next = { ...(current ?? { name, enabled: null, permission: null }), ...patch }
    const matchesDefaults = next.enabled == null && next.permission == null && next.deferred == null
    if (matchesDefaults && discovered.some((tool) => tool.name === name)) {
      onToolsChange(server.tools.filter((tool) => tool.name !== name))
    } else if (current == null) {
      onToolsChange([...server.tools, next])
    } else {
      onToolsChange(server.tools.map((tool) => (tool.name === name ? next : tool)))
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-3 sm:flex-nowrap">
        <Field className="min-w-0 flex-1 basis-full sm:basis-auto">
          <div className="flex items-baseline justify-between gap-2">
            <FieldLabel htmlFor={inputId}>Tools</FieldLabel>
            {rows.length > 0 && (
              <span className="text-muted-foreground text-xs tabular-nums" aria-live="polite">
                {typedName === ''
                  ? `${rows.length} ${rows.length === 1 ? 'tool' : 'tools'}`
                  : `${visibleRows.length} of ${rows.length}`}
              </span>
            )}
          </div>
          <div className="relative">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2" />
            <Input
              id={inputId}
              type="search"
              className="pl-9"
              placeholder="Filter tools"
              autoComplete="off"
              value={query}
              onChange={(event) => {
                setQuery(event.target.value)
              }}
              onKeyDown={(event) => {
                if (event.key !== 'Enter') return
                event.preventDefault()
                if (typedNameAddable) addTool(typedName)
              }}
            />
          </div>
        </Field>
        {defaults}
      </div>
      <AgentConfigMcpToolList
        rows={visibleRows}
        showRemove={rows.some((row) => !row.discovered)}
        permissionProfile={permissionProfile}
        defaultPermissionMode={
          server.permission?.mode ?? permissionProfile?.default_permission.mode ?? ''
        }
        serverAvailability={mcpServerAvailability(server)}
        onToolChange={changeTool}
        onToolRemove={(name) => {
          onToolsChange(server.tools.filter((tool) => tool.name !== name))
        }}
        empty={
          typedName === '' ? (
            discovering ? (
              'Discovering tools…'
            ) : (
              'No tools discovered. Type a tool name above to configure it manually.'
            )
          ) : typedNameAddable ? (
            <span className="inline-flex flex-wrap items-center justify-center gap-2">
              No tools match.
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => {
                  addTool(typedName)
                }}
              >
                Add “{typedName}”
              </Button>
            </span>
          ) : (
            'No tools match.'
          )
        }
      />
      {unexposableTools.length > 0 && <UnexposableTools tools={unexposableTools} />}
      {discoveryFailure && (
        <DiscoveryFailure
          error={discoveryFailure}
          server={server}
          onRetry={() => {
            void discovery.refetch()
          }}
          onAuthTypeChange={onAuthTypeChange}
        />
      )}
    </div>
  )
}

function toolRows(tools: BasicMcpTool[], discovered: McpServerTool[]): McpToolRow[] {
  const configured = new Map(tools.map((tool) => [tool.name, tool]))
  const discoveredNames = new Set(discovered.map((tool) => tool.name))
  return [
    ...discovered.map((tool) => ({
      name: tool.name,
      title: tool.title ?? undefined,
      description: tool.description ?? undefined,
      discovered: true,
      tool: configured.get(tool.name),
    })),
    ...tools
      .filter((tool) => !discoveredNames.has(tool.name))
      .map((tool) => ({ name: tool.name, discovered: false, tool })),
  ]
}

function matchesToolSearch(row: McpToolRow, search: string) {
  const needle = search.trim().toLowerCase()
  return (
    needle === '' ||
    row.name.toLowerCase().includes(needle) ||
    (row.title?.toLowerCase().includes(needle) ?? false) ||
    (row.description?.toLowerCase().includes(needle) ?? false)
  )
}

function visibleDiscoveryFailure(
  discovery: ReturnType<typeof useMcpServerTools>,
  authType: McpAuthType,
) {
  if (!discovery.isError) return null
  if (authType !== 'none') return discovery.error
  return detectedAuthType(discovery.error) == null ? discovery.error : null
}

function parseRequestKey(key: string): McpServerToolsRequest | null {
  if (key === '' || key === 'null') return null
  const parsed = schemas.zMcpServerToolsRequest.safeParse(JSON.parse(key))
  return parsed.success ? parsed.data : null
}

function DiscoveryFailure({
  error,
  server,
  onRetry,
  onAuthTypeChange,
}: {
  error: unknown
  server: BasicMcpServer
  onRetry: () => void
  onAuthTypeChange: (authType: McpAuthType) => void
}) {
  const detected = detectedAuthType(error)
  const switchable = detected != null && detected !== server.authType
  return (
    <div role="alert" className="text-warning flex items-start gap-2 text-sm">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="font-medium">{discoveryFailureTitle(error, server, detected)}</p>
        <p className="text-muted-foreground break-words">
          {error instanceof ApiError ? error.message : 'Unexpected error.'}
        </p>
      </div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="shrink-0"
        onClick={() => {
          if (switchable) onAuthTypeChange(detected)
          else onRetry()
        }}
      >
        {switchable ? (detected === 'oauth' ? 'Use OAuth' : 'Use bearer secret') : 'Retry'}
      </Button>
    </div>
  )
}

function UnexposableTools({ tools }: { tools: UnexposableMcpTool[] }) {
  return (
    <div role="alert" className="text-destructive flex items-start gap-2 text-sm">
      <TriangleAlert className="mt-0.5 size-4 shrink-0" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="font-medium">
          {tools.length === 1
            ? 'One tool cannot be exposed to the model, so the agent will fail to connect to this server. Fix the name or disable the tool.'
            : `${tools.length} tools cannot be exposed to the model, so the agent will fail to connect to this server. Fix the names or disable the tools.`}
        </p>
        <ul className="text-muted-foreground list-disc space-y-0.5 pl-5">
          {tools.map((tool) => (
            <li key={tool.name} className="break-words">
              {tool.error}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function detectedAuthType(cause: unknown): McpAuthType | null {
  if (!(cause instanceof ApiError) || cause.status !== 422) return null
  const parsed = schemas.zMcpServerAuthRequiredError.safeParse(cause.body)
  return parsed.success ? (parsed.data.auth?.type ?? null) : null
}

function discoveryFailureTitle(
  cause: unknown,
  server: BasicMcpServer,
  detected: McpAuthType | null,
) {
  if (detected === 'oauth') {
    return server.authType === 'oauth'
      ? 'The server rejected the selected OAuth secret. Log in again to refresh it.'
      : 'This server uses OAuth. Switch authentication to an OAuth secret and log in.'
  }
  if (detected === 'bearer') {
    return server.authType === 'bearer' || server.authType === 'sigv4'
      ? 'The server rejected the selected secret.'
      : 'This server expects a bearer token. Switch authentication to a bearer secret.'
  }
  if (cause instanceof ApiError && cause.status === 424) {
    return 'The MCP server failed or did not respond. Try again.'
  }
  if (cause instanceof ApiError && cause.status === 422) {
    return 'The MCP server did not accept the request.'
  }
  if (cause instanceof ApiError && cause.status === 404) {
    return 'The selected secret is not available to this project.'
  }
  return 'Could not discover tools.'
}
