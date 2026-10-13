import type { Skill } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { agentCardLinkClass } from '@/components/agents/AgentCardList'

/**
 * A skill tile: name, then the description. The name links to the skill's page, under a
 * project when `projectId` is set and the org-level page otherwise.
 */
export function SkillCard({
  skill,
  projectId,
  source,
  actions,
}: {
  skill: Skill
  projectId?: string
  /** Where a shared skill comes from, shown after the name. */
  source?: string
  actions?: ReactNode
}) {
  return (
    <article className="bg-card hover:border-foreground/20 has-[a:focus-visible]:ring-ring/50 group/skill relative flex flex-col gap-1 rounded-xl border p-4 transition-colors has-[a:focus-visible]:ring-[3px]">
      <div className="flex min-h-7 items-center justify-between gap-2">
        <div className="flex min-w-0 items-baseline gap-2">
          {projectId === undefined ? (
            <Link
              to="/skills/$skillId"
              params={{ skillId: skill.id }}
              className={agentCardLinkClass}
            >
              {skill.name}
            </Link>
          ) : (
            <Link
              to="/projects/$projectId/skills/$skillId"
              params={{ projectId, skillId: skill.id }}
              className={agentCardLinkClass}
            >
              {skill.name}
            </Link>
          )}
          {source && <span className="text-muted-foreground shrink-0 text-xs">{source}</span>}
        </div>
        {actions && (
          // Revealed on hover where hovering is possible; always shown on touch screens.
          <div className="[@media(hover:hover)]:not-group-hover/skill:not-group-focus-within/skill:not-has-[[aria-expanded=true]]:opacity-0 relative -my-1 -mr-2 shrink-0 transition-opacity">
            {actions}
          </div>
        )}
      </div>
      <p className="text-muted-foreground line-clamp-2 text-sm">{skill.description}</p>
    </article>
  )
}
