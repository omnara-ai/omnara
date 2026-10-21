import type { AgentInteraction, ResolveAgentInteractionRequest } from '@omnara/sdk'

export type Resolve = (
  interaction: AgentInteraction,
  body: ResolveAgentInteractionRequest,
) => Promise<void>

export function keyedContextItems(interaction: AgentInteraction) {
  const occurrences = new Map<string, number>()
  return (interaction.request.context ?? []).map((item) => {
    const contentKey = JSON.stringify([item.label, item.value])
    const occurrence = (occurrences.get(contentKey) ?? 0) + 1
    occurrences.set(contentKey, occurrence)
    return {
      item,
      key: `${interaction.id}:context:${contentKey}:${String(occurrence)}`,
    }
  })
}

export function subagentSourceLabel(interaction: AgentInteraction): string | null {
  if (interaction.agent_name == null) return null
  return `From subagent ${interaction.agent_name}`
}
