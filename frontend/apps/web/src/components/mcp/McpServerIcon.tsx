import type { McpRegistryServer } from '@omnara/sdk'
import { useState } from 'react'

import { Server } from '@/components/icons'
import { registryServerIconCandidates } from '@/components/mcp/mcpRegistry'
import { cn } from '@/lib/utils'

export function McpServerIcon({
  server,
  url = '',
  className,
}: {
  server: Pick<McpRegistryServer, 'name' | 'icons'> | null | undefined
  url?: string
  className?: string
}) {
  const [failed, setFailed] = useState<ReadonlySet<string>>(() => new Set())
  const src = registryServerIconCandidates(server, url).find((candidate) => !failed.has(candidate))
  if (src) {
    return (
      <img
        src={src}
        alt=""
        className={cn('size-5 shrink-0 rounded-sm object-contain', className)}
        onError={() => {
          setFailed((previous) => new Set(previous).add(src))
        }}
      />
    )
  }
  return (
    <Server aria-hidden="true" className={cn('text-muted-foreground size-5 shrink-0', className)} />
  )
}
