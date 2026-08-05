import { useLocation, useNavigate } from '@tanstack/react-router'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SecretsSection } from '@/components/overview/SecretsSection'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useActiveOrg } from '@/lib/use-active-org'

export function SecretsPage() {
  const { activeOrg } = useActiveOrg()
  const navigate = useNavigate()
  const ownerParam = useLocation({
    select: (location) => new URLSearchParams(location.searchStr).get('owner'),
  })
  const owner = ownerParam === 'organization' ? 'organization' : 'user'

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name, to: '/' }, { label: 'Secrets' }]} />
      <Tabs
        value={owner}
        onValueChange={(nextOwner) => {
          void navigate({
            href: nextOwner === 'organization' ? '/secrets?owner=organization' : '/secrets',
          })
        }}
        className="gap-6"
      >
        <TabsList aria-label="Secret owner">
          <TabsTrigger value="user">User</TabsTrigger>
          <TabsTrigger value="organization">Organization</TabsTrigger>
        </TabsList>
        <TabsContent value="user">
          <SecretsSection owner={{ kind: 'user' }} />
        </TabsContent>
        <TabsContent value="organization">
          <SecretsSection />
        </TabsContent>
      </Tabs>
    </div>
  )
}
