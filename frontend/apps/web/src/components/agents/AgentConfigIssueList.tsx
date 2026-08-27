import type { ErrorIssue } from '@omnara/sdk'

import { formatIssuePath } from '@/lib/agent-config-issues'
import { cn } from '@/lib/utils'

export function AgentConfigIssueList({
  issues,
  onSelect,
  className,
}: {
  issues: readonly ErrorIssue[]
  onSelect?: (issue: ErrorIssue) => void
  className?: string
}) {
  if (issues.length === 0) return null

  return (
    <ul
      role="alert"
      className={cn(
        'border-destructive/30 bg-destructive/5 flex flex-col gap-1 rounded-md border px-3 py-2 text-sm',
        className,
      )}
    >
      {issues.map((issue, index) => {
        const path = formatIssuePath(issue.path)
        const content = (
          <>
            {issue.line !== undefined && (
              <span className="text-muted-foreground font-mono text-xs">Line {issue.line}</span>
            )}
            {path !== '' && <span className="font-mono text-xs">{path}</span>}
            <span className="text-destructive">{issue.message}</span>
          </>
        )
        const selectable = onSelect !== undefined && issue.line !== undefined
        return (
          <li key={`${issue.path}-${issue.line ?? 0}-${index}`}>
            {selectable ? (
              <button
                type="button"
                className="flex w-full items-baseline gap-2 text-left hover:underline"
                onClick={() => {
                  onSelect(issue)
                }}
              >
                {content}
              </button>
            ) : (
              <span className="flex items-baseline gap-2">{content}</span>
            )}
          </li>
        )
      })}
    </ul>
  )
}
