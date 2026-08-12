import { useMe } from '@omnara/react'
import { useRouterState } from '@tanstack/react-router'
import { ArrowUpRight, BookOpen } from 'lucide-react'
import { type ReactNode, useEffect } from 'react'

import { NavUser } from '@/components/app-shell/NavUser'
import { OrganizationNav } from '@/components/app-shell/OrganizationNav'
import { OrgSwitcher } from '@/components/app-shell/OrgSwitcher'
import { ProjectsNav } from '@/components/app-shell/ProjectsNav'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from '@/components/ui/sidebar'
import { recordRecentPage } from '@/lib/recent-pages'
import { useActiveOrg } from '@/lib/use-active-org'

export function AppShell({ children }: { children: ReactNode }) {
  const { activeOrg } = useActiveOrg()
  const { data: me } = useMe()
  const pathname = useRouterState({ select: (state) => state.location.pathname })

  useEffect(() => {
    recordRecentPage(activeOrg.id, me.user.id, pathname)
  }, [activeOrg.id, me.user.id, pathname])

  return (
    <SidebarProvider className="h-svh">
      <Sidebar collapsible="none">
        <SidebarHeader>
          <OrgSwitcher />
        </SidebarHeader>
        <SidebarContent>
          <OrganizationNav />
          <ProjectsNav />
        </SidebarContent>
        <SidebarFooter>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton asChild>
                <a href="https://docs.omnara.com" target="_blank" rel="noreferrer">
                  <BookOpen />
                  <span>Documentation</span>
                  <ArrowUpRight className="text-muted-foreground ml-auto size-3.5" />
                </a>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
          <NavUser />
        </SidebarFooter>
      </Sidebar>

      <SidebarInset>
        <div className="min-h-0 flex-1 overflow-auto p-6">{children}</div>
      </SidebarInset>
    </SidebarProvider>
  )
}
