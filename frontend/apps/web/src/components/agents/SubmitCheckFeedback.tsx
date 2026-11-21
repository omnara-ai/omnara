import { AgentConfigIssueList } from '@/components/agents/AgentConfigIssueList'

export function SubmitCheckFeedback({
  problems,
  unchanged = false,
}: {
  problems: readonly string[]
  unchanged?: boolean
}) {
  if (unchanged) {
    return (
      <p role="status" className="text-muted-foreground text-sm">
        No changes to save.
      </p>
    )
  }
  return (
    <AgentConfigIssueList
      issues={problems.map((message) => ({ path: '', message }))}
      className="max-h-[25vh] overflow-y-auto"
    />
  )
}
