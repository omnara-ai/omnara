import { useNavigate, useSearch } from '@tanstack/react-router'

import { OrgAgentProfilesSection } from '@/components/agents/AgentProfilesSection'
import { OrgAgentsSection } from '@/components/agents/AgentsSection'
import { OrgCreateAgentProfileButton } from '@/components/agents/CreateAgentProfileButton'
import { PillTabs } from '@/components/agents/PillTabs'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SectionTitle } from '@/components/layout/SectionTitle'
import { type Guide, guides } from '@/lib/docs'
import { useActiveOrg } from '@/lib/use-active-org'

type AgentsTab = 'profiles' | 'instances'

const headings = {
  profiles: {
    title: 'Agent profiles',
    guide: guides.agentProfiles,
  },
  instances: {
    title: 'Agent instances',
    guide: guides.agents,
  },
} satisfies Record<AgentsTab, { title: string; guide: Guide }>

export function OrgAgentsPage() {
  const { activeOrg } = useActiveOrg()
  const search = useSearch({ from: '/authenticated/onboarded/agents' })
  const tab: AgentsTab = search.tab ?? 'profiles'
  const heading = headings[tab]
  const navigate = useNavigate()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ id: 'agents', label: 'Agents' }]} />
      <div className="flex flex-col gap-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <SectionTitle title={heading.title} guide={heading.guide} />
          <div className="flex items-center gap-2">
            <PillTabs
              value={tab}
              onValueChange={(value) => {
                void navigate({ to: '/agents', search: value === 'profiles' ? {} : { tab: value } })
              }}
              tabs={[
                { value: 'profiles', label: 'Profiles' },
                { value: 'instances', label: 'Instances' },
              ]}
            />
            <OrgCreateAgentProfileButton
              orgId={activeOrg.id}
              label="New agent"
              offerNewProject={false}
            />
          </div>
        </div>
        {tab === 'profiles' ? (
          <OrgAgentProfilesSection orgId={activeOrg.id} />
        ) : (
          <OrgAgentsSection
            orgId={activeOrg.id}
            emptyMessage="No agents yet. Create an agent profile, then launch agents from it."
            emptyAction={<OrgCreateAgentProfileButton orgId={activeOrg.id} />}
          />
        )}
      </div>
    </div>
  )
}
