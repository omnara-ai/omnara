import { OrgApiKeysSection } from '@/components/api-tokens/OrgApiKeysSection'
import { PersonalAccessTokensSection } from '@/components/api-tokens/PersonalAccessTokensSection'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ApiTokensPage() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={[{ label: activeOrg.name, to: '/' }, { label: 'API Tokens' }]} />

      {canManage ? (
        <Tabs defaultValue="user" className="gap-6">
          <TabsList>
            <TabsTrigger value="user">User</TabsTrigger>
            <TabsTrigger value="org">Organization</TabsTrigger>
          </TabsList>
          <TabsContent value="user">
            <PersonalAccessTokensSection />
          </TabsContent>
          <TabsContent value="org">
            <OrgApiKeysSection orgId={activeOrg.id} />
          </TabsContent>
        </Tabs>
      ) : (
        <PersonalAccessTokensSection />
      )}
    </div>
  )
}
