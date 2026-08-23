import { useMe, usePendingInvitations } from '@omnara/react'
import type { OrgInvitation } from '@omnara/sdk'
import { Link, useNavigate } from '@tanstack/react-router'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { AppShell } from '@/components/app-shell/AppShell'
import { BrandMark } from '@/components/brand/OmnaraMark'
import { MailCheck } from '@/components/icons'
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
import { useActiveOrg } from '@/lib/use-active-org'

export function Invitations() {
  const { data: me } = useMe()

  if (me.orgs.length === 0) {
    return (
      <div className="flex min-h-svh flex-col items-center gap-8 p-6 sm:pt-12">
        <Link to="/onboarding" className="flex items-center gap-2 text-base font-semibold">
          <BrandMark />
          Omnara
        </Link>
        <main className="w-full">
          <InvitationsContent
            hasOrganizations={false}
            onAccepted={() => {
              window.location.assign('/')
            }}
          />
        </main>
      </div>
    )
  }

  return (
    <ActiveOrgProvider>
      <OrganizationInvitations />
    </ActiveOrgProvider>
  )
}

function OrganizationInvitations() {
  const navigate = useNavigate()
  const { setActiveOrgId } = useActiveOrg()

  return (
    <AppShell>
      <InvitationsContent
        hasOrganizations
        onAccepted={async (invitation) => {
          setActiveOrgId(invitation.org_id)
          await navigate({ to: '/' })
        }}
      />
    </AppShell>
  )
}

function InvitationsContent({
  hasOrganizations,
  onAccepted,
}: {
  hasOrganizations: boolean
  onAccepted: (invitation: OrgInvitation) => void | Promise<void>
}) {
  const { data } = usePendingInvitations()
  const invitations = data.data

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-8">
      {hasOrganizations && <PageBreadcrumb items={[{ id: 'invitations', label: 'Invitations' }]} />}

      <div className="flex flex-col gap-2">
        <h1 className="text-2xl font-semibold tracking-tight">Pending invitations</h1>
        <p className="text-muted-foreground text-sm">
          Review invitations to join other organizations.
        </p>
      </div>

      {invitations.length > 0 ? (
        <PendingInvitationList invitations={invitations} onAccepted={onAccepted} />
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
          More invitations are available. Respond to one to see more.
        </p>
      )}
    </div>
  )
}
