import { OrgApiKeysSection } from '@/components/api-tokens/OrgApiKeysSection'
import { PersonalAccessTokensSection } from '@/components/api-tokens/PersonalAccessTokensSection'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ApiTokensPage() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const ownerTabs = (
    <TabsList aria-label="Token owner">
      <TabsTrigger value="user">User</TabsTrigger>
      <TabsTrigger value="org">Organization</TabsTrigger>
    </TabsList>
  )

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ id: 'api-tokens', label: 'API Tokens' }]} />

      {canManage ? (
        <Tabs defaultValue="user" className="gap-6">
          <TabsContent value="user">
            <PersonalAccessTokensSection actions={ownerTabs} />
          </TabsContent>
          <TabsContent value="org">
            <OrgApiKeysSection orgId={activeOrg.id} actions={ownerTabs} />
          </TabsContent>
        </Tabs>
      ) : (
        <PersonalAccessTokensSection />
      )}
    </div>
  )
}
