import { useLocation, useNavigate } from '@tanstack/react-router'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SkillsSection } from '@/components/overview/SkillsSection'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useActiveOrg } from '@/lib/use-active-org'

export function SkillsPage() {
  const { activeOrg } = useActiveOrg()
  const navigate = useNavigate()
  const ownerParam = useLocation({
    select: (location) => new URLSearchParams(location.searchStr).get('owner'),
  })
  const owner = ownerParam === 'organization' ? 'organization' : 'user'

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name, to: '/' }, { label: 'Skills' }]} />
      <Tabs
        value={owner}
        onValueChange={(nextOwner) => {
          void navigate({
            href: nextOwner === 'organization' ? '/skills?owner=organization' : '/skills',
          })
        }}
        className="gap-6"
      >
        <TabsList aria-label="Skill owner">
          <TabsTrigger value="user">User</TabsTrigger>
          <TabsTrigger value="organization">Organization</TabsTrigger>
        </TabsList>
        <TabsContent value="user">
          <SkillsSection owner={{ kind: 'user' }} />
        </TabsContent>
        <TabsContent value="organization">
          <SkillsSection />
        </TabsContent>
      </Tabs>
    </div>
  )
}
