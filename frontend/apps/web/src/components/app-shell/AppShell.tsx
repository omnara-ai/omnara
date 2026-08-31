import type { ReactNode } from 'react'

import { NavUser } from '@/components/app-shell/NavUser'
import { OrganizationNav } from '@/components/app-shell/OrganizationNav'
import { OrgSwitcher } from '@/components/app-shell/OrgSwitcher'
import { ProjectsNav } from '@/components/app-shell/ProjectsNav'
import { ArrowUpRight, BookOpen } from '@/components/icons'
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

export function AppShell({ children }: { children: ReactNode }) {
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
        <div className="relative min-h-0 flex-1 overflow-auto p-6">{children}</div>
      </SidebarInset>
    </SidebarProvider>
  )
}
