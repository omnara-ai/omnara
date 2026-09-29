import { Link, useNavigate, useSearch } from '@tanstack/react-router'

import { AgentProfilesSection } from '@/components/agents/AgentProfilesSection'
import { AgentsSection } from '@/components/agents/AgentsSection'
import { PillTabs } from '@/components/agents/PillTabs'
import { SectionTitle } from '@/components/layout/SectionTitle'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { type Guide, guides } from '@/lib/docs'

type AgentsTab = 'profiles' | 'instances'

const headings = {
  profiles: {
    title: 'Agent profiles',
    subtitle: 'Profiles provide reusable templates for agents',
    guide: guides.agentProfiles,
  },
  instances: {
    title: 'Agent instances',
    subtitle: 'Individual agent chat instances',
    guide: guides.agents,
  },
} satisfies Record<AgentsTab, { title: string; subtitle: string; guide: Guide }>

export function ProjectAgentsPage() {
  const search = useSearch({ strict: false })
  const tab: AgentsTab = search.tab ?? 'profiles'
  const heading = headings[tab]
  const navigate = useNavigate()

  return (
    <ProjectPageFrame title="Agents">
      {({ activeOrg, projectId, project }) => (
        <div className="flex flex-col gap-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex min-w-0 flex-col gap-1">
              <SectionTitle title={heading.title} guide={heading.guide} />
              <p className="text-muted-foreground text-sm">{heading.subtitle}</p>
            </div>
            <div className="flex items-center gap-2">
              <PillTabs
                value={tab}
                onValueChange={(value) => {
                  void navigate({
                    to: '/projects/$projectId/agents',
                    params: { projectId },
                    search: value === 'profiles' ? {} : { tab: value },
                  })
                }}
                tabs={[
                  { value: 'profiles', label: 'Profiles' },
                  { value: 'instances', label: 'Instances' },
                ]}
              />
              {project?.access.can_manage && (
                <Button asChild size="sm">
                  <Link to="/projects/$projectId/agents/new" params={{ projectId }}>
                    New agent
                  </Link>
                </Button>
              )}
            </div>
          </div>
          {tab === 'profiles' ? (
            <AgentProfilesSection
              orgId={activeOrg.id}
              projectId={projectId}
              canOperate={project?.access.can_operate ?? false}
            />
          ) : (
            <AgentsSection
              orgId={activeOrg.id}
              projectId={projectId}
              canManage={project?.access.can_manage ?? false}
              emptyMessage="No agents yet. Launch one from a profile, or create one with New agent."
            />
          )}
        </div>
      )}
    </ProjectPageFrame>
  )
}
