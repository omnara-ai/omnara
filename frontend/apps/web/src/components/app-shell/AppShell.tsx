import { useParams } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import { agentPaneWidth } from '@/components/agents/agent-pane'
import { NavUser } from '@/components/app-shell/NavUser'
import { OrganizationNav } from '@/components/app-shell/OrganizationNav'
import { OrgSwitcher } from '@/components/app-shell/OrgSwitcher'
import { PendingInvitationsNav } from '@/components/app-shell/PendingInvitationsNav'
import { ProjectNav } from '@/components/app-shell/ProjectNav'
import { ProjectSwitcher } from '@/components/app-shell/ProjectSwitcher'
import { ArrowUpRight, BookOpen } from '@/components/icons'
import { BreadcrumbSlotContext } from '@/components/layout/breadcrumb-slot-context'
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
import type { CssVariables } from '@/lib/css'

function AppSidebar({ children }: { children: ReactNode }) {
  const { isMobile, setOpenMobile } = useSidebar()

  return (
    <Sidebar
      collapsible={isMobile ? 'offcanvas' : 'none'}
      className="md:border-sidebar-border/60 md:border-r"
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

const insetStyle: CssVariables = { '--agent-pane-width': agentPaneWidth }

function ScopedNav() {
  const projectId = useParams({ strict: false, select: (params) => params.projectId })
  return projectId ? <ProjectNav projectId={projectId} /> : <OrganizationNav />
}

export function AppShell({ children }: { children: ReactNode }) {
  const [breadcrumbSlot, setBreadcrumbSlot] = useState<HTMLDivElement | null>(null)

  return (
    <SidebarProvider className="h-svh" keyboardShortcut={false}>
      <AppSidebar>
        <SidebarHeader className="h-12 shrink-0 justify-center border-b py-0">
          <OrgSwitcher />
        </SidebarHeader>
        <SidebarContent className="pt-2">
          <PendingInvitationsNav />
          <ScopedNav />
        </SidebarContent>
        <SidebarFooter>
          <SidebarMenu className="gap-1">
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

      <SidebarInset
        style={insetStyle}
        className="group/inset md:[&:has([data-side=right][data-state=expanded])>header]:pr-[calc(var(--agent-pane-width)+1rem)]"
      >
        {/* A page marked data-transparent-header draws under the header, which floats over it. */}
        <header className="bg-background grid h-12 shrink-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-2 border-b px-4 group-has-[[data-transparent-header]]/inset:absolute group-has-[[data-transparent-header]]/inset:inset-x-0 group-has-[[data-transparent-header]]/inset:top-0 group-has-[[data-transparent-header]]/inset:z-10 group-has-[[data-transparent-header]]/inset:bg-transparent md:grid-cols-[1fr_minmax(0,auto)_1fr]">
          <div className="flex min-w-0 items-center gap-2">
            <SidebarTrigger className="size-10 sm:size-10 md:hidden" />
            <ProjectSwitcher />
          </div>
          <div ref={setBreadcrumbSlot} className="flex min-w-0 justify-end md:justify-center" />
        </header>
        <BreadcrumbSlotContext value={breadcrumbSlot}>
          <div className="relative min-h-0 flex-1 overflow-auto p-4 pt-6 sm:p-6 sm:pt-9">
            {children}
          </div>
        </BreadcrumbSlotContext>
      </SidebarInset>
    </SidebarProvider>
  )
}
