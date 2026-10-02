import { useLocation, useNavigate } from '@tanstack/react-router'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SkillsSection } from '@/components/overview/SkillsSection'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export function SkillsPage() {
  const navigate = useNavigate()
  const ownerParam = useLocation({
    select: (location) => new URLSearchParams(location.searchStr).get('owner'),
  })
  const owner = ownerParam === 'organization' ? 'organization' : 'user'

  const ownerTabs = (
    <TabsList aria-label="Skill owner">
      <TabsTrigger value="user">User</TabsTrigger>
      <TabsTrigger value="organization">Organization</TabsTrigger>
    </TabsList>
  )

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ id: 'skills', label: 'Skills' }]} />
      <Tabs
        value={owner}
        onValueChange={(nextOwner) => {
          void navigate({
            href: nextOwner === 'organization' ? '/skills?owner=organization' : '/skills',
          })
        }}
        className="gap-6"
      >
        <TabsContent value="user">
          <SkillsSection owner={{ kind: 'user' }} actions={ownerTabs} />
        </TabsContent>
        <TabsContent value="organization">
          <SkillsSection actions={ownerTabs} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
