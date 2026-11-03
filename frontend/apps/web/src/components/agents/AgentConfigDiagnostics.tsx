import { type ConfigDiagnostic, configPath } from '@/components/agents/agentConfigCompatibility'

export function AgentConfigDiagnostics({ issues }: { issues: readonly ConfigDiagnostic[] }) {
  const visible = issues.filter((issue) => issue.kind !== 'advanced' || issue.error)
  const unique = new Map(
    visible.map((issue) => [JSON.stringify([issue.path, issue.kind, issue.message]), issue]),
  )
  return [...unique].map(([key, issue]) => (
    <p
      key={key}
      className={issue.error ? 'text-destructive text-sm' : 'text-muted-foreground text-sm'}
      role={issue.error ? 'alert' : undefined}
    >
      {configPath(issue.path)}: {issue.message}
    </p>
  ))
}
