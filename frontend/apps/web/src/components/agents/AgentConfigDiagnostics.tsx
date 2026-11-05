import type { ConfigDiagnostic } from '@/components/agents/agentConfigCompatibility'

function fieldLabel(path: readonly string[]): string {
  const [field, ...rest] = path
  if (!field) return ''
  if (field === 'tools' && rest[0])
    return [`Tool “${rest[0]}”`, fieldLabel(rest.slice(1))].filter(Boolean).join(' → ')
  const label = field.replaceAll('_', ' ')
  return [label.charAt(0).toUpperCase() + label.slice(1), ...rest].join(' → ')
}

export function AgentConfigDiagnostics({
  issues,
  basePath = [],
}: {
  issues: readonly ConfigDiagnostic[]
  basePath?: readonly string[]
}) {
  const unique = new Map(
    issues.map((issue) => [JSON.stringify([issue.path, issue.kind, issue.message]), issue]),
  )
  return [...unique].map(([key, issue]) => {
    const path = basePath.every((part, index) => issue.path[index] === part)
      ? issue.path.slice(basePath.length)
      : issue.path
    const label = fieldLabel(path)
    return (
      <p
        key={key}
        className={issue.error ? 'text-destructive text-sm' : 'text-muted-foreground text-sm'}
        role={issue.error ? 'alert' : undefined}
      >
        {label && !issue.message.toLowerCase().startsWith(label.toLowerCase()) && `${label}: `}
        {issue.message}
      </p>
    )
  })
}
