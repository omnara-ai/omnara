import { useOrgUsage } from '@omnara/react'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { UsageReportView } from '@/components/usage/UsageReport'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationUsagePage() {
  const { activeOrg } = useActiveOrg()
  const query = useOrgUsage(activeOrg.id)

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: activeOrg.name, to: '/' },
          { id: 'usage', label: 'Usage' },
        ]}
      />
      <section className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="type-title">Usage</h1>
          <p className="text-muted-foreground text-sm">
            Model tokens and provider-reported cost across every agent in {activeOrg.name}.
          </p>
        </div>
        <UsageReportView query={query} />
      </section>
    </div>
  )
}
