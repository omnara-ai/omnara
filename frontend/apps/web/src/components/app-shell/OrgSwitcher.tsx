import { usePendingInvitationsQuery } from '@omnara/react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { useState } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'
import { Check, ChevronsUpDown, Mail, Plus } from '@/components/icons'
import { CreateOrgDialog } from '@/components/org/CreateOrgDialog'
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
import { useActiveOrg } from '@/lib/use-active-org'

export function OrgSwitcher() {
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const { orgs, activeOrg, setActiveOrgId } = useActiveOrg()
  const { data: pendingInvitations } = usePendingInvitationsQuery()
  const [newOrgOpen, setNewOrgOpen] = useState(false)
  const pendingCount = pendingInvitations?.data.length ?? 0
  const pendingCountLabel = pendingInvitations?.next_cursor
    ? `${pendingCount}+`
    : String(pendingCount)

  async function switchOrganization(id: string) {
    if (id === activeOrg.id) return
    setActiveOrgId(id)
    await navigate({ to: '/', replace: true })
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
              {orgs.map((org) => (
                <DropdownMenuItem
                  key={org.id}
                  className="gap-2"
                  onClick={() => {
                    void switchOrganization(org.id)
                  }}
                >
                  <span className="flex-1 truncate">{org.name}</span>
                  {org.id === activeOrg.id && <Check className="size-4 shrink-0" />}
                </DropdownMenuItem>
              ))}
              <DropdownMenuSeparator />
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
    </>
  )
}
