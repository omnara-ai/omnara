import { useMe } from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { AppShell } from '@/components/app-shell/AppShell'
import { BrandMark } from '@/components/brand/OmnaraMark'
import { DetailList } from '@/components/data-table/DetailList'
import { type Crumb, PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SectionTitle } from '@/components/layout/SectionTitle'
import { DeleteAccountSection } from '@/components/settings/DeleteAccountSection'
import { safeReturnTo } from '@/lib/auth-return-to'
import { useActiveOrg } from '@/lib/use-active-org'

const accountCrumb: Crumb = { id: 'account', label: 'Account' }

// Users without an organization can still reach their account (to delete it),
// so this page renders outside the app shell until they join or create one.
export function AccountSettingsPage() {
  const { data: me } = useMe()

  if (me.orgs.length === 0) {
    // Carry onboarding's return_to (e.g. a CLI device approval) back so it can finish there.
    const returnTo = safeReturnTo(new URLSearchParams(window.location.search).get('return_to'))
    return (
      <div className="flex min-h-svh flex-col items-center gap-8 p-6 sm:pt-12">
        <Link
          to="/onboarding"
          search={returnTo === '/' ? {} : { return_to: returnTo }}
          className="type-card-title flex items-center gap-2 text-base"
        >
          <BrandMark />
          Omnara
        </Link>
        <main className="w-full">
          <AccountSettingsContent breadcrumb={[accountCrumb]} />
        </main>
      </div>
    )
  }

  return (
    <ActiveOrgProvider>
      <AppShell>
        <OrganizationAccountSettings />
      </AppShell>
    </ActiveOrgProvider>
  )
}

function OrganizationAccountSettings() {
  const { activeOrg } = useActiveOrg()
  return (
    <AccountSettingsContent
      breadcrumb={[{ id: 'organization', label: activeOrg.name, to: '/' }, accountCrumb]}
    />
  )
}

function AccountSettingsContent({ breadcrumb }: { breadcrumb: Crumb[] }) {
  const { data: me } = useMe()

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb items={breadcrumb} />
      <section className="flex flex-col gap-3">
        <SectionTitle title="Account" />
        <DetailList
          items={[
            { label: 'Name', value: me.user.display_name },
            { label: 'Email', value: me.user.email },
            { label: 'User ID', value: me.user.id, mono: true },
          ]}
        />
      </section>
      <DeleteAccountSection />
    </div>
  )
}
