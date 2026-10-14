import { DetailList } from '@/components/data-table/DetailList'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SectionTitle } from '@/components/layout/SectionTitle'
import { DeleteOrganizationSection } from '@/components/settings/DeleteOrganizationSection'
import { formatDateTime } from '@/lib/format'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrganizationSettingsPage() {
  const { activeOrg } = useActiveOrg()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: activeOrg.name, to: '/' },
          { id: 'settings', label: 'Settings' },
        ]}
      />
      <section className="flex flex-col gap-3">
        <SectionTitle title="Organization" />
        <DetailList
          items={[
            { label: 'Name', value: activeOrg.name },
            { label: 'Organization ID', value: activeOrg.id, mono: true },
            { label: 'Your role', value: <span className="capitalize">{activeOrg.role}</span> },
            { label: 'Created', value: formatDateTime(activeOrg.created_at) },
          ]}
        />
      </section>
      <DeleteOrganizationSection />
    </div>
  )
}
