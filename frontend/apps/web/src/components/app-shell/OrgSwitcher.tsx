import { usePendingInvitationsQuery } from '@omnara/react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { Check, ChevronsUpDown, Copy, Mail, Plus, UserPlus } from 'lucide-react'
import { useEffect, useState } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'
import { CreateOrgDialog } from '@/components/org/CreateOrgDialog'
import { InviteMemberDialog } from '@/components/org/InviteMemberDialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
} from '@/components/ui/sidebar'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrgSwitcher() {
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const { orgs, activeOrg, setActiveOrgId } = useActiveOrg()
  const { data: pendingInvitations } = usePendingInvitationsQuery()
  const canManage = canManageOrg(activeOrg.role)
  const [newOrgOpen, setNewOrgOpen] = useState(false)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [copiedOrgID, setCopiedOrgID] = useState<string | null>(null)
  const pendingCount = pendingInvitations?.data.length ?? 0
  const pendingCountLabel = pendingInvitations?.next_cursor
    ? `${pendingCount}+`
    : String(pendingCount)

  useEffect(() => {
    if (copiedOrgID === null) return

    const timeout = window.setTimeout(() => {
      setCopiedOrgID(null)
    }, 1500)

    return () => {
      window.clearTimeout(timeout)
    }
  }, [copiedOrgID])

  async function switchOrganization(id: string) {
    if (id === activeOrg.id) return
    setActiveOrgId(id)
    await navigate({ to: '/', replace: true })
  }

  async function copyActiveOrganizationID() {
    try {
      await navigator.clipboard.writeText(activeOrg.id)
      setCopiedOrgID(activeOrg.id)
    } catch {
      setCopiedOrgID(null)
    }
  }

  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton
                size="lg"
                className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
              >
                <BrandMark className="aspect-square size-8" />
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">{activeOrg.name}</span>
                  <span className="text-muted-foreground truncate text-xs capitalize">
                    {activeOrg.role}
                  </span>
                </div>
                <ChevronsUpDown className="ml-auto size-4" />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              className="w-(--radix-dropdown-menu-trigger-width) min-w-60 rounded-lg"
              align="start"
              side="bottom"
              sideOffset={4}
            >
              <DropdownMenuLabel className="text-muted-foreground text-xs">
                Organizations
              </DropdownMenuLabel>
              {orgs.map((org) => {
                const selected = org.id === activeOrg.id

                return (
                  <DropdownMenuItem
                    key={org.id}
                    className={selected ? 'flex-col items-stretch gap-0.5 py-2' : 'gap-2'}
                    aria-label={
                      selected
                        ? copiedOrgID === org.id
                          ? `Copied organization ID ${org.id}`
                          : `${org.name}, selected. Copy organization ID ${org.id}`
                        : `Switch to ${org.name}`
                    }
                    onSelect={(event) => {
                      if (selected) {
                        event.preventDefault()
                        void copyActiveOrganizationID()
                        return
                      }
                      void switchOrganization(org.id)
                    }}
                  >
                    <span className="flex w-full min-w-0 items-center gap-2">
                      <span className="flex-1 truncate">{org.name}</span>
                      {selected && <Check className="size-4 shrink-0" />}
                    </span>
                    {selected && (
                      <span className="text-muted-foreground flex min-w-0 items-center gap-1 text-xs">
                        <code className="min-w-0 truncate font-mono text-[11px] font-normal">
                          {org.id}
                        </code>
                        {copiedOrgID === org.id ? (
                          <Check className="text-primary size-3" />
                        ) : (
                          <Copy className="size-3" />
                        )}
                      </span>
                    )}
                  </DropdownMenuItem>
                )
              })}
              <DropdownMenuSeparator />
              {canManage && (
                <DropdownMenuItem
                  onClick={() => {
                    setInviteOpen(true)
                  }}
                >
                  <UserPlus />
                  Invite members
                </DropdownMenuItem>
              )}
              <DropdownMenuItem
                onClick={() => {
                  setNewOrgOpen(true)
                }}
              >
                <Plus />
                New organization
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
        {pendingCount > 0 && (
          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={pathname === '/invitations'}>
              <Link to="/invitations" aria-label={`Pending invitations, ${pendingCountLabel}`}>
                <Mail />
                <span>Pending invitations</span>
              </Link>
            </SidebarMenuButton>
            <SidebarMenuBadge aria-hidden="true" className="bg-primary text-primary-foreground">
              {pendingCountLabel}
            </SidebarMenuBadge>
          </SidebarMenuItem>
        )}
      </SidebarMenu>

      <CreateOrgDialog open={newOrgOpen} onOpenChange={setNewOrgOpen} />
      {canManage && (
        <InviteMemberDialog open={inviteOpen} onOpenChange={setInviteOpen} orgId={activeOrg.id} />
      )}
    </>
  )
}
