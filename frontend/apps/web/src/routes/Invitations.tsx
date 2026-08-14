import { useMe, usePendingInvitations } from '@omnara/react'
import { Link } from '@tanstack/react-router'
import { MailCheck } from 'lucide-react'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { AppShell } from '@/components/app-shell/AppShell'
import { BrandMark } from '@/components/brand/OmnaraMark'
import { PendingInvitationList } from '@/components/invitations/PendingInvitationList'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'

export function Invitations() {
  const { data: me } = useMe()

  if (me.orgs.length === 0) {
    return (
      <div className="flex min-h-svh flex-col items-center gap-8 p-6 sm:pt-12">
        <Link to="/onboarding" className="flex items-center gap-2 text-base font-semibold">
          <BrandMark />
          Omnara
        </Link>
        <InvitationsContent hasOrganizations={false} />
      </div>
    )
  }

  return (
    <ActiveOrgProvider>
      <AppShell>
        <InvitationsContent hasOrganizations />
      </AppShell>
    </ActiveOrgProvider>
  )
}

function InvitationsContent({ hasOrganizations }: { hasOrganizations: boolean }) {
  const { data } = usePendingInvitations()
  const invitations = data.data

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-col gap-8">
      {hasOrganizations && <PageBreadcrumb items={[{ label: 'Invitations' }]} />}

      <div className="flex flex-col gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">Pending invitations</h1>
        <p className="text-muted-foreground text-sm">
          Review invitations to join other Omnara organizations.
        </p>
      </div>

      {invitations.length > 0 ? (
        <PendingInvitationList invitations={invitations} />
      ) : (
        <Empty className="border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <MailCheck />
            </EmptyMedia>
            <EmptyTitle>No pending invitations</EmptyTitle>
            <EmptyDescription>
              New organization invitations will appear here when they arrive.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button asChild variant="outline">
              <Link to={hasOrganizations ? '/' : '/onboarding'}>
                {hasOrganizations ? 'Back to dashboard' : 'Create an organization'}
              </Link>
            </Button>
          </EmptyContent>
        </Empty>
      )}

      {data.next_cursor && (
        <p className="text-muted-foreground text-sm">
          More invitations are available. Respond to one to load the next invitation.
        </p>
      )}
    </main>
  )
}
