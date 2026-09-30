import { usePendingInvitationsQuery } from '@omnara/react'
import { Link, useRouterState } from '@tanstack/react-router'

import { Mail } from '@/components/icons'
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
} from '@/components/ui/sidebar'

export function PendingInvitationsNav() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const { data: pendingInvitations } = usePendingInvitationsQuery()
  const pendingCount = pendingInvitations?.data.length ?? 0
  const pendingCountLabel = pendingInvitations?.next_cursor
    ? `${pendingCount}+`
    : String(pendingCount)

  if (pendingCount === 0) return null

  return (
    <SidebarGroup className="pb-0">
      <SidebarGroupContent>
        <SidebarMenu>
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
        </SidebarMenu>
      </SidebarGroupContent>
    </SidebarGroup>
  )
}
