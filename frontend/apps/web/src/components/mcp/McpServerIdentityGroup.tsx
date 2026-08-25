import { useServerInfo, useServerInfoLookup, useServers } from '@omnara/react'
import { type FocusEvent, type KeyboardEvent, useId, useRef, useState } from 'react'

import { mcpServerNameMaxLength } from '@/components/agents/useAgentBuilderForm'
import {
  registryServerEntries,
  type RegistryServerEntry,
  registryServerLabel,
  registryServerSearchFilters,
  registryServerSuggestedName,
} from '@/components/mcp/mcpRegistry'
import { McpServerIcon } from '@/components/mcp/McpServerIcon'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useDebouncedValue } from '@/hooks/use-resource-list'
import { cn } from '@/lib/utils'

const inputClassName =
  'placeholder:text-muted-foreground h-9 min-w-0 flex-1 bg-transparent px-3 text-base outline-none md:text-sm'

export function McpServerIdentityGroup({
  idPrefix,
  name,
  nameInvalid,
  url,
  onChange,
}: {
  idPrefix: string
  name: string
  nameInvalid: boolean
  url: string
  onChange: (patch: { name?: string; url?: string }) => void
}) {
  const listId = useId()
  const [open, setOpen] = useState(false)
  const [highlighted, setHighlighted] = useState<number | null>(null)
  const generatedName = useRef('')
  const lookupToken = useRef(0)
  const debouncedName = useDebouncedValue(name)
  const debouncedUrl = useDebouncedValue(url)
  const filters = registryServerSearchFilters(debouncedName)
  const searching = filters.q !== undefined
  const serversQuery = useServers({ filters, pageSize: 25, enabled: open && searching })
  const results = registryServerEntries(useInfiniteQueryItems(serversQuery))
  const info = useServerInfo(debouncedUrl)
  const lookupServerInfo = useServerInfoLookup()
  const selected = info.data ?? null
  const showList = open && searching
  const activeIndex =
    highlighted === null || results.length === 0 ? null : Math.min(highlighted, results.length - 1)

  function nameReplaceable() {
    return name.trim() === '' || name === generatedName.current
  }

  function select(entry: RegistryServerEntry) {
    lookupToken.current += 1
    const suggested = registryServerSuggestedName(entry.server)
    const replaceName = nameReplaceable() && suggested !== ''
    onChange({
      name: replaceName ? suggested : undefined,
      url: entry.remote.url,
    })
    if (replaceName) {
      generatedName.current = suggested
    }
    setOpen(false)
    setHighlighted(null)
  }

  async function suggestNameFor(candidateUrl: string) {
    const token = (lookupToken.current += 1)
    const server = await lookupServerInfo(candidateUrl)
    if (token !== lookupToken.current) return
    const suggested = server ? registryServerSuggestedName(server) : ''
    if (suggested !== '') {
      generatedName.current = suggested
      onChange({ name: suggested })
    }
  }

  function onBlur(event: FocusEvent<HTMLDivElement>) {
    if (!event.currentTarget.contains(event.relatedTarget)) {
      setOpen(false)
      setHighlighted(null)
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') {
      setOpen(false)
      setHighlighted(null)
      return
    }
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setOpen(true)
      if (results.length > 0) {
        setHighlighted(activeIndex === null ? 0 : (activeIndex + 1) % results.length)
      }
      return
    }
    if (!showList || results.length === 0) return
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setHighlighted(
        activeIndex === null
          ? results.length - 1
          : (activeIndex - 1 + results.length) % results.length,
      )
    } else if (event.key === 'Enter' && activeIndex !== null) {
      const entry = results[activeIndex]
      if (entry) {
        event.preventDefault()
        select(entry)
      }
    }
  }

  return (
    <div className="relative" onBlur={onBlur}>
      <div
        className={cn(
          'border-input dark:bg-input/30 flex h-9 w-full items-stretch divide-x rounded-md border transition-[color,box-shadow]',
          'focus-within:border-ring focus-within:ring-ring/50 focus-within:ring-[3px]',
          nameInvalid &&
            'border-destructive focus-within:border-destructive focus-within:ring-destructive/20 dark:focus-within:ring-destructive/40',
        )}
      >
        <div className="flex w-10 shrink-0 items-center justify-center">
          <McpServerIcon server={selected} url={debouncedUrl} />
        </div>
        <input
          id={`${idPrefix}-name`}
          role="combobox"
          aria-expanded={showList}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={
            showList && activeIndex !== null ? `${listId}-${activeIndex}` : undefined
          }
          required
          maxLength={mcpServerNameMaxLength}
          aria-invalid={nameInvalid}
          value={name}
          placeholder="Name"
          autoComplete="off"
          className={cn(inputClassName, 'sm:max-w-56')}
          onKeyDown={onKeyDown}
          onFocus={() => {
            setOpen(true)
          }}
          onChange={(event) => {
            lookupToken.current += 1
            setHighlighted(null)
            setOpen(true)
            onChange({ name: event.target.value })
          }}
        />
        <label htmlFor={`${idPrefix}-url`} className="sr-only">
          URL
        </label>
        <input
          id={`${idPrefix}-url`}
          required
          value={url}
          placeholder="https://example.com/mcp"
          autoComplete="off"
          className={inputClassName}
          onFocus={() => {
            setOpen(false)
          }}
          onBlur={() => {
            if (nameReplaceable()) {
              void suggestNameFor(url)
            }
          }}
          onChange={(event) => {
            onChange({ url: event.target.value })
          }}
        />
      </div>
      {showList && (
        <div
          id={listId}
          role="listbox"
          aria-label="MCP registry results"
          className="bg-popover text-popover-foreground absolute left-0 right-0 top-full z-50 mt-1 max-h-80 overflow-y-auto rounded-md border p-1 shadow-md"
        >
          <div className="text-muted-foreground border-b px-2 py-1.5 text-xs">
            Searching the MCP registry by name, description, and URL. Select a server to fill in its
            endpoint.
          </div>
          {results.length === 0 ? (
            <div className="text-muted-foreground px-2 py-2 text-sm">
              {serversQuery.isError
                ? 'Could not load registry results.'
                : serversQuery.isFetching
                  ? 'Searching…'
                  : 'No registry servers match.'}
            </div>
          ) : (
            results.map(({ key, server, remote }, index) => {
              return (
                <button
                  key={key}
                  type="button"
                  id={`${listId}-${index}`}
                  role="option"
                  aria-selected={index === activeIndex}
                  tabIndex={-1}
                  className={cn(
                    'flex w-full cursor-default items-center gap-3 rounded-sm px-2 py-2 text-left text-sm',
                    index === activeIndex && 'bg-accent text-accent-foreground',
                  )}
                  onMouseDown={(event) => {
                    event.preventDefault()
                  }}
                  onMouseEnter={() => {
                    setHighlighted(index)
                  }}
                  onClick={() => {
                    select({ key, server, remote })
                  }}
                >
                  <McpServerIcon server={server} url={remote.url} className="size-6" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline gap-2">
                      <span className="truncate font-medium">{registryServerLabel(server)}</span>
                      <span className="text-muted-foreground truncate text-xs">{server.name}</span>
                    </div>
                    {server.description && (
                      <div className="text-muted-foreground line-clamp-1 text-xs">
                        {server.description}
                      </div>
                    )}
                    <div className="text-muted-foreground truncate text-xs">{remote.url}</div>
                  </div>
                </button>
              )
            })
          )}
          {results.length > 0 && serversQuery.hasNextPage && (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground w-full px-2 py-2 text-left text-xs disabled:opacity-50"
              disabled={serversQuery.isFetchingNextPage}
              onMouseDown={(event) => {
                event.preventDefault()
              }}
              onClick={() => {
                void serversQuery.fetchNextPage()
              }}
            >
              {serversQuery.isFetchingNextPage ? 'Loading more…' : 'Load more results'}
            </button>
          )}
        </div>
      )}
    </div>
  )
}
