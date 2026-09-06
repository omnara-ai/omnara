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
  SidebarTrigger,
  useSidebar,
} from '@/components/ui/sidebar'

function AppSidebar({ children }: { children: ReactNode }) {
  const { isMobile, setOpenMobile } = useSidebar()

  return (
    <Sidebar
      collapsible={isMobile ? 'offcanvas' : 'none'}
      onClick={(event) => {
        if (isMobile && event.target instanceof Element && event.target.closest('a')) {
          setOpenMobile(false)
        }
      }}
    >
      {children}
    </Sidebar>
  )
}

export function AppShell({ children }: { children: ReactNode }) {
  return (
    <SidebarProvider className="h-svh" keyboardShortcut={false}>
      <AppSidebar>
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
      </AppSidebar>

      <SidebarInset>
        <div className="bg-background flex h-12 shrink-0 items-center border-b px-4 md:hidden">
          <SidebarTrigger className="size-10 sm:size-10" />
        </div>
        <div className="relative min-h-0 flex-1 overflow-auto p-4 sm:p-6">{children}</div>
      </SidebarInset>
    </SidebarProvider>
  )
}
