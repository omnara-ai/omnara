import { useLocation, useNavigate } from '@tanstack/react-router'

import { SecretsSection } from '@/components/overview/SecretsSection'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

export function SecretsPage() {
  const navigate = useNavigate()
  const ownerParam = useLocation({
    select: (location) => new URLSearchParams(location.searchStr).get('owner'),
  })
  const owner = ownerParam === 'organization' ? 'organization' : 'user'

  const ownerTabs = (
    <TabsList aria-label="Secret owner">
      <TabsTrigger value="user">User</TabsTrigger>
      <TabsTrigger value="organization">Organization</TabsTrigger>
    </TabsList>
  )

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <Tabs
        value={owner}
        onValueChange={(nextOwner) => {
          void navigate({
            href: nextOwner === 'organization' ? '/secrets?owner=organization' : '/secrets',
          })
        }}
        className="gap-6"
      >
        <TabsContent value="user">
          <SecretsSection owner={{ kind: 'user' }} actions={ownerTabs} />
        </TabsContent>
        <TabsContent value="organization">
          <SecretsSection actions={ownerTabs} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
