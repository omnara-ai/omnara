import { IntegrationAppsSection } from '@/components/integrations/IntegrationAppsSection'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationAppsPage() {
  const { activeOrg } = useActiveOrg()
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: activeOrg.name, to: '/' },
          { id: 'apps', label: 'Apps' },
        ]}
      />
      <IntegrationAppsSection
        key={activeOrg.id}
        orgId={activeOrg.id}
        canManage={canManageOrg(activeOrg.role)}
      />
    </div>
  )
}
